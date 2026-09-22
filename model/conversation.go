package model

import (
	"strings"
	"time"
)

// Conversation is the user-facing chat identity. It sits above Session:
// the user picks a conversation; the linked Session holds messages, tools,
// files and any sub-agent workers.
//
// ConversationID is a per-user numeric increment (FormatID(Seq)). Parent
// identity is UserID, never concatenated into the id.
// ConversationRunState is the last visible turn for this conversation. Hosts
// persist it on the conversation row so a closed page can reconnect and show
// the right loading state without guessing from the transcript.
type ConversationRunState struct {
	Phase         string    `json:"phase,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Active        bool      `json:"active"`
	UserMessageID string    `json:"user_message_id,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type Conversation struct {
	ConversationID string
	UserID         string
	// SessionID is the main session this conversation is attached to.
	SessionID      string
	Title          string
	TitleUpdatedAt time.Time
	// replaceTitle is set only by SetChosenTitle. Automatic writers, including
	// summarization, leave it false so PutConversation keeps a stored title.
	replaceTitle bool
	Model        string
	Archived     bool
	Seq          int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	RunState     *ConversationRunState `json:"run_state,omitempty"`
}

// GenerateConversationID returns the per-user numeric conversation id.
// userID is ignored; parent identity is stored on Conversation.UserID.
//
// Deprecated: the concatenated form `{UserID}-c{seq}` is no longer produced.
func GenerateConversationID(userID string, seq int) string {
	_ = strings.TrimSpace(userID)
	return FormatID(seq)
}

// NewConversation creates a conversation row pointing at an existing main session.
func NewConversation(userID, conversationID, sessionID, title, modelName string, seq int) *Conversation {
	now := time.Now()
	conversation := &Conversation{
		ConversationID: conversationID,
		UserID:         userID,
		SessionID:      sessionID,
		Title:          strings.TrimSpace(title),
		Model:          strings.TrimSpace(modelName),
		Seq:            seq,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if !UnsetTitle(conversation.Title) {
		conversation.TitleUpdatedAt = now
	}
	return conversation
}

// HasChosenTitle reports whether a title has ever been selected. Checking the
// current value also keeps legacy conversations safe after this field is added.
func (c *Conversation) HasChosenTitle() bool {
	return c != nil && (!c.TitleUpdatedAt.IsZero() || !UnsetTitle(c.Title))
}

// SetChosenTitle records an explicit rename. Summarization and other automatic
// writers must not call it; they only fill a title that is still empty.
func (c *Conversation) SetChosenTitle(title string, at time.Time) {
	if c == nil {
		return
	}
	c.Title = strings.TrimSpace(title)
	if at.IsZero() {
		at = time.Now()
	}
	c.TitleUpdatedAt = at
	c.replaceTitle = true
}

// PreserveChosenTitle keeps a name that was already selected when dst would
// otherwise replace it. Automatic writers (run-state, activity touch, a
// concurrent summary) load a stale conversation and PutConversation the whole
// row; they must not rotate or clear a chosen title. A later TitleUpdatedAt
// replaces the stored name only when dst came from SetChosenTitle.
func PreserveChosenTitle(dst, stored *Conversation) {
	if dst == nil || stored == nil || !stored.HasChosenTitle() {
		return
	}
	if dst.replaceTitle && dst.HasChosenTitle() && dst.TitleUpdatedAt.After(stored.TitleUpdatedAt) {
		return
	}
	dst.Title = stored.Title
	dst.TitleUpdatedAt = stored.TitleUpdatedAt
}

// MissingTitlePlan describes the only title writes summarization may make.
// A conversation or session title that is already set is never a write target.
type MissingTitlePlan struct {
	Generate          bool
	WriteSession      bool
	WriteConversation bool
	Existing          string
}

// PlanMissingTitles chooses a conversation name at most once.
// A conversation that already has a name is never regenerated or replaced.
// The model is called only when that conversation exists and still has no
// name, and the linked session cannot supply one. A missing conversation row
// is not a reason to invent a title.
func PlanMissingTitles(sessionTitle string, conversation *Conversation) MissingTitlePlan {
	if conversation == nil || conversation.HasChosenTitle() {
		if conversation != nil && conversation.HasChosenTitle() && UnsetTitle(sessionTitle) && !UnsetTitle(conversation.Title) {
			return MissingTitlePlan{WriteSession: true, Existing: strings.TrimSpace(conversation.Title)}
		}
		return MissingTitlePlan{}
	}
	if title := strings.TrimSpace(sessionTitle); !UnsetTitle(title) {
		return MissingTitlePlan{WriteConversation: true, Existing: title}
	}
	return MissingTitlePlan{
		Generate:          true,
		WriteSession:      true,
		WriteConversation: true,
	}
}

// KeepStoredSessionTitle copies a title that is already stored onto session.
// An empty stored title does not clear a title held in memory.
func KeepStoredSessionTitle(session, stored *Session) {
	if session == nil || stored == nil || UnsetTitle(stored.Title) {
		return
	}
	session.Title = stored.Title
}

// ApplyMissingTitle writes a generated title only onto sides that still have
// no title. It returns the conversation title to sync (empty when the
// conversation must stay unchanged) and the generated title that was actually
// stored (empty when no new title was written).
func ApplyMissingTitle(session *Session, conversation *Conversation, generated string) (syncTitle, applied string) {
	if session == nil {
		return "", ""
	}
	plan := PlanMissingTitles(session.Title, conversation)
	generated = strings.TrimSpace(generated)
	if plan.Generate {
		if generated == "" {
			return "", ""
		}
		if plan.WriteSession {
			session.Title = generated
		}
		if plan.WriteConversation {
			syncTitle = generated
		}
		return syncTitle, generated
	}
	if plan.WriteSession && !UnsetTitle(plan.Existing) {
		session.Title = plan.Existing
	}
	if plan.WriteConversation && !UnsetTitle(plan.Existing) {
		syncTitle = plan.Existing
	}
	return syncTitle, ""
}

// IsSubAgent reports whether this session is a worker of another session.
func (s *Session) IsSubAgent() bool {
	return s != nil && strings.TrimSpace(s.ParentSessionID) != ""
}

// CanCreateSubAgent is true only for a conversation's main session.
func (s *Session) CanCreateSubAgent() bool {
	return s != nil && s.AgentType == AgentTypeConversation && strings.TrimSpace(s.ParentSessionID) == ""
}
