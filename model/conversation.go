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
	SessionID string
	Title     string
	Model     string
	Archived  bool
	Seq       int
	CreatedAt time.Time
	UpdatedAt time.Time
	RunState  *ConversationRunState `json:"run_state,omitempty"`
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
	return &Conversation{
		ConversationID: conversationID,
		UserID:         userID,
		SessionID:      sessionID,
		Title:          strings.TrimSpace(title),
		Model:          strings.TrimSpace(modelName),
		Seq:            seq,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// IsSubAgent reports whether this session is a worker of another session.
func (s *Session) IsSubAgent() bool {
	return s != nil && strings.TrimSpace(s.ParentSessionID) != ""
}

// CanCreateSubAgent is true only for a conversation's main session.
func (s *Session) CanCreateSubAgent() bool {
	return s != nil && s.AgentType == AgentTypeConversation && strings.TrimSpace(s.ParentSessionID) == ""
}
