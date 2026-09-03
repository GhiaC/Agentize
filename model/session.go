package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// SummaryEntries is the append-only memory produced by successive
// summarization cycles.  Its JSON decoder accepts the legacy scalar string so
// existing SQLite, PostgreSQL and MongoDB session rows migrate on read without
// a destructive data migration.
type SummaryEntries []string

// ContextDelta is a validated append-only proposal for cross-conversation user
// memory. PendingUserContext makes scheduler delivery retryable across crashes.
type ContextDelta struct {
	Summary SummaryEntries `json:"summary"`
	Tags    []string       `json:"tags"`
}

// MarshalJSON always emits an array. In particular, an uninitialized/nil
// value is persisted as [] rather than null so the durable schema has one
// stable shape after legacy rows are rewritten.
func (s SummaryEntries) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(s))
}

func (s *SummaryEntries) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = nil
		return nil
	}
	var entries []string
	if err := json.Unmarshal(data, &entries); err == nil {
		// Persisted entries are immutable history. Do not trim, reorder or
		// deduplicate them while loading.
		*s = SummaryEntries(entries)
		return nil
	}
	var legacy string
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("summary must be a string or string array: %w", err)
	}
	if legacy = strings.TrimSpace(legacy); legacy != "" {
		*s = SummaryEntries{legacy}
	} else {
		*s = nil
	}
	return nil
}

// Text renders entries for prompts and compact debug previews. Storage remains
// an array; presentation is deliberately kept at the edges.
func (s SummaryEntries) Text() string { return strings.Join(s, "\n") }

func (s SummaryEntries) Clone() SummaryEntries {
	if s == nil {
		return nil
	}
	return append(SummaryEntries(nil), s...)
}

// AppendSummaryEntries preserves every existing item byte-for-byte and appends
// only non-empty, non-duplicate new facts in their generated order.
func AppendSummaryEntries(existing SummaryEntries, additions ...string) SummaryEntries {
	out := existing.Clone()
	seen := make(map[string]struct{}, len(out))
	for _, entry := range out {
		seen[strings.ToLower(strings.TrimSpace(entry))] = struct{}{}
	}
	for _, entry := range additions {
		entry = strings.TrimSpace(entry)
		key := strings.ToLower(entry)
		if entry == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
	}
	return out
}

// AppendTags preserves the existing order and values and appends only new
// case-insensitive tags. A positive limit caps the final list.
func AppendTags(existing, additions []string, limit int) []string {
	out := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(out))
	for _, tag := range out {
		seen[strings.ToLower(strings.TrimSpace(tag))] = struct{}{}
	}
	for _, tag := range additions {
		tag = strings.TrimSpace(tag)
		key := strings.ToLower(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

// Context key for user ID
type userIDKey struct{}

// WithUserID adds user_id to context
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// GetUserIDFromContext retrieves user_id from context
func GetUserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDKey{}).(string)
	return userID, ok
}

// UserIDFrom returns the context user id, or empty when unset.
func UserIDFrom(ctx context.Context) string {
	userID, ok := GetUserIDFromContext(ctx)
	if !ok {
		return ""
	}
	return strings.TrimSpace(userID)
}

// AgentType represents the type of agent that owns a session
type AgentType string

const (
	AgentTypeCore AgentType = "core"
	AgentTypeHigh AgentType = "high"
	AgentTypeLow  AgentType = "low"
	AgentTypeUser AgentType = "user"
	// AgentTypeConversation is the main session attached to a Conversation row.
	// Conversation identity lives on the conversations table, not in this ID.
	AgentTypeConversation AgentType = "conv"
	// AgentTypeSub is a short-lived worker session owned by a main conversation
	// session. Sub-agents cannot create further sub-agents.
	AgentTypeSub AgentType = "sub"
	// AgentTypeWorkflow owns dedicated sessions used by deterministic Core
	// workflow schedules. It is intentionally distinct from the singleton Core
	// session type.
	AgentTypeWorkflow AgentType = "workflow"
)

// Session represents a user session in the agent system
// All fields are flattened for simple database storage and loading
type Session struct {
	// ==================== Identifiers ====================
	UserID    string
	SessionID string
	AgentType AgentType // core, high, low, conv, sub, user, workflow
	Model     string    // LLM model name (e.g., "gpt-4o", "gpt-4o-mini")
	// ParentSessionID is set only on sub-agent sessions and points at the
	// conversation's main session. Empty means this session may create
	// sub-agents (when it is a conversation main session).
	ParentSessionID string

	// ==================== Messages (flattened from ConversationState) ====================
	// Msgs contains the active conversation messages
	Msgs []openai.ChatCompletionMessage

	// ArchivedMsgs contains messages that have been summarized and moved out of active conversation
	// (Previously was both SummarizedMessages and ExMsgs - now unified)
	// When Msgs is empty, the scheduler uses ArchivedMsgs for summarization (e.g. re-summarize when Summary was lost).
	ArchivedMsgs []openai.ChatCompletionMessage

	// ==================== Runtime State (not persisted to database) ====================
	// InProgress indicates if a message is currently being processed
	InProgress bool `bson:"-" json:"-"`

	// Queue holds messages waiting to be processed
	Queue []openai.ChatCompletionMessage `bson:"-" json:"-"`

	// ==================== Knowledge/Tools ====================
	// NodeDigests stores lightweight information about visited nodes
	NodeDigests []NodeDigest

	// SystemPrompts is the ordered array most recently assembled for an LLM
	// request in this session. It is observability state, not transcript history.
	SystemPrompts          []SystemPromptEntry
	SystemPromptsUpdatedAt time.Time

	// ToolResults stores tool execution results by unique ID (for large results)
	ToolResults map[string]string

	// ==================== Timestamps ====================
	CreatedAt    time.Time
	UpdatedAt    time.Time // Also serves as LastActivity
	SummarizedAt time.Time // When the session was last summarized

	// ==================== Summarization ====================
	Tags    []string       // User-defined or auto-generated tags for categorization
	Title   string         // Session title (auto-generated or user-set)
	Summary SummaryEntries `json:"Summary"` // Append-only LLM-generated facts; legacy scalar JSON is accepted on load.
	// SummaryInitialized distinguishes a valid no-op [] result from legacy rows
	// that were marked summarized after an empty/invalid provider response.
	SummaryInitialized bool
	PendingUserContext *ContextDelta

	// ==================== Sequences ====================
	// Seq is this session's per-user number (SessionID is FormatID(Seq) for new
	// rows). Parent identity lives on UserID, never inside SessionID.
	Seq                 int
	MessageSeq          int            // Sequence counter for messages (per session)
	ToolSeq             int            // Deprecated: session-wide tool counter; new tool ids increment per message.
	ToolSeqByMessage    map[string]int // Per-message tool-call increment
	OpenedFileSeq       int            // Deprecated: opened-file ids now increment on User.FileSeq.
	UserFileSeq         int            // Deprecated: user-file ids now increment on User.FileSeq.
	SummarizationLogSeq int            // Sequence counter for summarization logs (per session)
	TraceSeq            int            // Sequence counter for route traces (per session)
	ResultSeq           int            // Sequence counter for buffered tool-result ids (per session)

	// ==================== Internal (not persisted) ====================
	seqMu sync.Mutex `bson:"-" json:"-"` // Mutex for thread-safe sequence operations
}

// NodeDigest is a lightweight representation of a node (for memory efficiency)
type NodeDigest struct {
	Path     string
	ID       string
	Title    string
	Hash     string
	LoadedAt time.Time
	Excerpt  string // First 100 chars of content
}

// NewSessionWithID creates a new session with a pre-generated session ID
// This is the preferred method when you have the session ID already (e.g., from store.GetNextSessionSeq)
func NewSessionWithID(userID string, sessionID string, agentType AgentType) *Session {
	now := time.Now()
	return &Session{
		UserID:              userID,
		SessionID:           sessionID,
		AgentType:           agentType,
		Model:               "",
		Msgs:                []openai.ChatCompletionMessage{},
		ArchivedMsgs:        []openai.ChatCompletionMessage{},
		InProgress:          false,
		Queue:               []openai.ChatCompletionMessage{},
		NodeDigests:         []NodeDigest{},
		SystemPrompts:       []SystemPromptEntry{},
		ToolResults:         make(map[string]string),
		CreatedAt:           now,
		UpdatedAt:           now,
		Tags:                []string{},
		Title:               "",
		Summary:             SummaryEntries{},
		SummaryInitialized:  false,
		Seq:                 SeqFromID(sessionID),
		MessageSeq:          0,
		ToolSeq:             0,
		ToolSeqByMessage:    make(map[string]int),
		OpenedFileSeq:       0,
		UserFileSeq:         0,
		SummarizationLogSeq: 0,
		TraceSeq:            0,
		ResultSeq:           0,
	}
}

// NewSessionForUser creates a new session for a user with a numeric sequential ID.
func NewSessionForUser(user *User, agentType AgentType) *Session {
	if user == nil {
		panic("NewSessionForUser: user cannot be nil")
	}

	seq := user.NextSessionSeq(agentType)
	sessionID := GenerateSessionID(user.UserID, agentType, seq)
	session := NewSessionWithID(user.UserID, sessionID, agentType)
	session.Seq = seq
	return session
}

// NewSessionWithType creates a new session for a user with a specific agent type
// This is a convenience function for tests and simple use cases
// For production, prefer using SessionHandler.CreateSession or NewSessionWithID
func NewSessionWithType(userID string, agentType AgentType) *Session {
	sessionID := GenerateSessionID(userID, agentType, 1)
	session := NewSessionWithID(userID, sessionID, agentType)
	session.Seq = 1
	return session
}

// GenerateSessionID returns the numeric session id for seq.
// userID and agentType are ignored; parent identity is stored on Session.UserID
// and Session.AgentType.
//
// Deprecated: the concatenated form `{UserID}-{AgentType}-s{seq}` is no longer
// produced. Callers should pass seq from GetNextSessionSeq / User.NextSessionSeq.
func GenerateSessionID(userID string, agentType AgentType, seq int) string {
	_ = userID
	_ = agentType
	return FormatID(seq)
}

// agentTypeShortCode returns short code for agent type.
//
// Deprecated: session ids no longer embed an agent-type fragment.
func agentTypeShortCode(agentType AgentType) string {
	s := string(agentType)
	if s == "" {
		return "unk"
	}
	return s
}

// NextMessageSeq increments and returns the next message sequence number
// Thread-safe via mutex
func (s *Session) NextMessageSeq() int {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return NextSeq(&s.MessageSeq)
}

// GenerateMessageID returns the next numeric message id in this session.
// Session identity is Session.SessionID, not concatenated into the message id.
func (s *Session) GenerateMessageID() string {
	id, _ := s.GenerateMessageIDWithSeq()
	return id
}

// GenerateMessageIDWithSeq returns the numeric message id and its per-session seq.
func (s *Session) GenerateMessageIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	seq := NextSeq(&s.MessageSeq)
	return FormatID(seq), seq
}

// NextToolSeq increments and returns the next session-wide tool sequence.
//
// Deprecated: tool ids increment per message via GenerateToolIDForMessage.
func (s *Session) NextToolSeq() int {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return NextSeq(&s.ToolSeq)
}

// GenerateToolID returns the next numeric tool id when the parent message is unknown.
//
// Deprecated: prefer GenerateToolIDForMessage so the counter is per message.
func (s *Session) GenerateToolID() string {
	return s.GenerateToolIDForMessage("")
}

// GenerateToolIDForMessage returns the next numeric tool-call id inside messageID.
// UserID, SessionID, and MessageID stay on ToolCall fields; they are not concatenated.
func (s *Session) GenerateToolIDForMessage(messageID string) string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	if s.ToolSeqByMessage == nil {
		s.ToolSeqByMessage = make(map[string]int)
	}
	key := strings.TrimSpace(messageID)
	n := s.ToolSeqByMessage[key]
	n = NextSeq(&n)
	s.ToolSeqByMessage[key] = n
	s.ToolSeq++
	return FormatID(n)
}

// GenerateToolIDWithSeq returns a numeric tool id and the session-wide tool seq.
//
// Deprecated: prefer GenerateToolIDForMessage.
func (s *Session) GenerateToolIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	seq := NextSeq(&s.ToolSeq)
	return FormatID(seq), seq
}

// GenerateFileID returns the next numeric opened-file id for this session.
//
// Deprecated: file ids increment per user via User.NextFileID.
func (s *Session) GenerateFileID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return FormatID(NextSeq(&s.OpenedFileSeq))
}

// GenerateUserFileID returns the next numeric user-file id.
//
// Deprecated: file ids increment per user via User.NextFileID.
func (s *Session) GenerateUserFileID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return FormatID(NextSeq(&s.UserFileSeq))
}

// GenerateFileIDWithSeq returns a numeric opened-file id and seq.
//
// Deprecated: file ids increment per user via User.NextFileID.
func (s *Session) GenerateFileIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	seq := NextSeq(&s.OpenedFileSeq)
	return FormatID(seq), seq
}

// GenerateSummarizationLogID returns the next numeric log id in this session.
// SessionID and UserID stay on the log row.
func (s *Session) GenerateSummarizationLogID() string {
	id, _ := s.GenerateSummarizationLogIDWithSeq()
	return id
}

// GenerateSummarizationLogIDWithSeq returns the numeric log id and per-session seq.
func (s *Session) GenerateSummarizationLogIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	seq := NextSeq(&s.SummarizationLogSeq)
	return FormatID(seq), seq
}

// GenerateRouteTraceID returns the next numeric route-trace id in this session.
func (s *Session) GenerateRouteTraceID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return FormatID(NextSeq(&s.TraceSeq))
}

// GenerateResultID returns the next numeric buffered-tool-result id in this session.
func (s *Session) GenerateResultID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	return FormatID(NextSeq(&s.ResultSeq))
}

// ==================== Backward Compatibility Methods ====================

// GetConversationState returns a ConversationState-like view of the session
// Deprecated: Access Msgs, InProgress, Queue, UpdatedAt directly on Session
func (s *Session) GetConversationState() *ConversationState {
	return &ConversationState{
		Msgs:         s.Msgs,
		InProgress:   s.InProgress,
		Queue:        s.Queue,
		LastActivity: s.UpdatedAt,
	}
}

// GetExMsgs returns ArchivedMsgs (for backward compatibility with debugger)
// Deprecated: Use ArchivedMsgs directly
func (s *Session) GetExMsgs() []openai.ChatCompletionMessage {
	return s.ArchivedMsgs
}

// GetSummarizedMessages returns ArchivedMsgs (for backward compatibility)
// Deprecated: Use ArchivedMsgs directly
func (s *Session) GetSummarizedMessages() []openai.ChatCompletionMessage {
	return s.ArchivedMsgs
}

// Clone creates a deep copy of the session
// This is safe to use when you need to copy a session without copying the mutex
func (s *Session) Clone() *Session {
	// Create a new session with the same values
	clone := &Session{
		UserID:              s.UserID,
		SessionID:           s.SessionID,
		AgentType:           s.AgentType,
		Model:               s.Model,
		ParentSessionID:     s.ParentSessionID,
		InProgress:          s.InProgress,
		CreatedAt:           s.CreatedAt,
		UpdatedAt:           s.UpdatedAt,
		SummarizedAt:        s.SummarizedAt,
		Title:               s.Title,
		Summary:             s.Summary.Clone(),
		SummaryInitialized:  s.SummaryInitialized,
		Seq:                 s.Seq,
		MessageSeq:          s.MessageSeq,
		ToolSeq:             s.ToolSeq,
		OpenedFileSeq:       s.OpenedFileSeq,
		UserFileSeq:         s.UserFileSeq,
		SummarizationLogSeq: s.SummarizationLogSeq,
		TraceSeq:            s.TraceSeq,
		ResultSeq:           s.ResultSeq,
		// seqMu is NOT copied - new mutex for the clone
	}

	// Copy slices
	if s.Msgs != nil {
		clone.Msgs = make([]openai.ChatCompletionMessage, len(s.Msgs))
		copy(clone.Msgs, s.Msgs)
	}
	if s.ArchivedMsgs != nil {
		clone.ArchivedMsgs = make([]openai.ChatCompletionMessage, len(s.ArchivedMsgs))
		copy(clone.ArchivedMsgs, s.ArchivedMsgs)
	}
	if s.Queue != nil {
		clone.Queue = make([]openai.ChatCompletionMessage, len(s.Queue))
		copy(clone.Queue, s.Queue)
	}
	if s.NodeDigests != nil {
		clone.NodeDigests = make([]NodeDigest, len(s.NodeDigests))
		copy(clone.NodeDigests, s.NodeDigests)
	}
	if s.Tags != nil {
		clone.Tags = make([]string, len(s.Tags))
		copy(clone.Tags, s.Tags)
	}

	// Copy maps
	if s.ToolResults != nil {
		clone.ToolResults = make(map[string]string, len(s.ToolResults))
		for k, v := range s.ToolResults {
			clone.ToolResults[k] = v
		}
	}
	if s.ToolSeqByMessage != nil {
		clone.ToolSeqByMessage = make(map[string]int, len(s.ToolSeqByMessage))
		for k, v := range s.ToolSeqByMessage {
			clone.ToolSeqByMessage[k] = v
		}
	}

	return clone
}

// LLMClientWithUserID wraps LLMClient to add user_id header to all requests
type LLMClientWithUserID struct {
	Client LLMClient
	UserID string
}

// CreateChatCompletion adds user_id header to the request
func (c *LLMClientWithUserID) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Add user_id to context if not already present
	if _, exists := GetUserIDFromContext(ctx); !exists {
		ctx = WithUserID(ctx, c.UserID)
	}

	// Create a copy of the request to modify headers
	// Note: openai.ChatCompletionRequest doesn't have a Headers field directly,
	// so we need to wrap the HTTP client. For now, we'll use context.
	// The actual header injection should be done at the HTTP client level.
	return c.Client.CreateChatCompletion(ctx, request)
}

// PopulateFields uses LLM to populate Title, Summary, and Tags fields of the session
// It requires an LLMClient and a model name
func (s *Session) PopulateFields(ctx context.Context, client LLMClient, model string) error {
	if client == nil {
		return fmt.Errorf("LLM client is required")
	}

	if model == "" {
		model = "openai/gpt-5-nano"
	}

	// Ensure user_id is in context
	ctx = WithUserID(ctx, s.UserID)

	// Get conversation text from messages
	// Uses ArchivedMsgs (previously summarized) + current Msgs
	var conversationText string
	allMessages := append(s.ArchivedMsgs, s.Msgs...)
	if len(allMessages) == 0 {
		return fmt.Errorf("no messages in session to populate fields")
	}

	// Format messages for LLM
	for _, msg := range allMessages {
		// Skip tool-related messages
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			continue
		}
		if msg.Content == "" {
			continue
		}
		conversationText += fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
	}

	// Refresh title on every population cycle.
	title, err := s.generateTitle(ctx, client, model, conversationText)
	if err != nil {
		return fmt.Errorf("failed to generate title: %w", err)
	}
	s.Title = title

	// PopulateFields is a compatibility path. Preserve earlier entries and add
	// its new compact result as one append-only item.
	if len(s.Summary) == 0 {
		summary, err := s.generateSummary(ctx, client, model, conversationText)
		if err != nil {
			return fmt.Errorf("failed to generate summary: %w", err)
		}
		s.Summary = AppendSummaryEntries(s.Summary, summary)
		s.SummaryInitialized = true
	}

	// Generate Tags
	if len(s.Tags) == 0 {
		tags, err := s.generateTags(ctx, client, model, conversationText)
		if err != nil {
			return fmt.Errorf("failed to generate tags: %w", err)
		}
		s.Tags = tags
	}

	// Update UpdatedAt timestamp
	s.UpdatedAt = time.Now()

	return nil
}

// generateTitle generates a title for the session
func (s *Session) generateTitle(ctx context.Context, client LLMClient, model string, conversationText string) (string, error) {
	systemPrompt := `Generate a short title (3-5 words) for this conversation.
The title should capture the main topic or purpose.
Return only the title, no quotes or extra text.

Example outputs:
- Kubernetes Pod Debugging
- API Authentication Design
- Database Migration Planning
- Quick Q&A Session`

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate a title for this conversation:\n\n" + conversationText},
		},
		MaxTokens: 20,
	}

	// Add user_id to request headers via context
	resp, err := client.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// generateSummary generates a summary for the session
func (s *Session) generateSummary(ctx context.Context, client LLMClient, model string, conversationText string) (string, error) {
	systemPrompt := `You are a conversation summarizer.
Generate a concise summary (2-3 sentences) that captures the main topics and outcomes of this conversation.

Requirements:
- Focus on key topics discussed and any decisions or conclusions reached
- Be specific about what was accomplished or discussed
- Maximum 200 characters
- Use present or past tense appropriately
- Do not include greetings or filler content

Example: "Debugged Kubernetes pod restart issue. Found memory limits too low. Applied fix and verified pod stability."`

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Summarize this conversation:\n\n" + conversationText},
		},
		MaxTokens: 200,
	}

	resp, err := client.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// TODO(TD-1): move generateTags into llmutils (tracked in CHANGELOG.md → Tracked technical debt).
// generateTags generates tags for the session
func (s *Session) generateTags(ctx context.Context, client LLMClient, model string, conversationText string) ([]string, error) {
	systemPrompt := `You are a conversation tagger.
Generate 2-5 relevant tags for this conversation that help categorize it.

Requirements:
- Tags should be short (1-3 words each)
- Focus on main topics, technologies, or problem domains
- Use lowercase, hyphenated format (e.g., "kubernetes", "api-design", "debugging")
- Return only the tags, comma-separated, no quotes or extra text
- Maximum 5 tags

Example outputs:
- kubernetes, debugging, pods
- api-design, authentication, security
- database, migration, postgresql`

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate tags for this conversation:\n\n" + conversationText},
		},
		MaxTokens: 50,
	}

	resp, err := client.CreateChatCompletion(ctx, request)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	// Parse tags from response
	tagsStr := strings.TrimSpace(resp.Choices[0].Message.Content)
	tagsStr = strings.Trim(tagsStr, "\"'")
	tags := strings.Split(tagsStr, ",")

	// Clean and trim tags
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		tag = strings.ToLower(tag)
		if tag != "" {
			result = append(result, tag)
		}
	}

	return result, nil
}
