package model

import (
	"fmt"
	"strings"
	"time"
)

// Conversation is the user-facing chat identity. It sits above Session:
// the user picks a conversation; the linked Session holds messages, tools,
// files and any sub-agent workers. IDs never contain a title slug.
//
// Format: {UserID}-c{Seq}  e.g. alice-c0001
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
}

// GenerateConversationID builds a stable conversation id with no slug.
func GenerateConversationID(userID string, seq int) string {
	return fmt.Sprintf("%s-c%04d", strings.TrimSpace(userID), seq)
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
