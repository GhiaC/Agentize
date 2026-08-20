package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
)

var (
	// ErrSubAgentNesting is returned when a sub-agent session tries to create another.
	ErrSubAgentNesting = errors.New("sub-agents cannot create sub-agents")
	// ErrNotConversationSession is returned when CreateSubAgent is called on a
	// session that is not a conversation's main session.
	ErrNotConversationSession = errors.New("only a conversation main session can create a sub-agent")
)

// CreateConversationInput is the host-facing constructor for a top-level chat.
type CreateConversationInput struct {
	UserID string
	Title  string
	Model  string
}

// CreateConversation creates a Conversation row and a linked main Session.
// SessionID stays on the existing {user}-{agentType}-s{seq} scheme; the
// user-facing id is {user}-c{seq} with no title slug.
func (e *Engine) CreateConversation(input CreateConversationInput) (*model.Conversation, error) {
	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		return nil, fmt.Errorf("user id is required")
	}
	if e.Sessions == nil {
		return nil, errors.New("session store is not configured")
	}

	seq, err := e.Sessions.GetNextConversationSeq(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next conversation seq: %w", err)
	}
	session, err := e.createTypedSession(userID, model.AgentTypeConversation, strings.TrimSpace(input.Title), strings.TrimSpace(input.Model), "")
	if err != nil {
		return nil, err
	}

	conversationID := model.GenerateConversationID(userID, seq)
	conv := model.NewConversation(userID, conversationID, session.SessionID, session.Title, session.Model, seq)
	if err := e.Sessions.PutConversation(conv); err != nil {
		_ = e.Sessions.Delete(session.SessionID)
		return nil, fmt.Errorf("failed to store conversation: %w", err)
	}
	log.Log.Infof("[Engine] ✅ Created conversation | ConversationID: %s | SessionID: %s", conv.ConversationID, conv.SessionID)
	return conv, nil
}

func (e *Engine) createTypedSession(userID string, agentType model.AgentType, title, modelName, parentSessionID string) (*model.Session, error) {
	seq, err := e.Sessions.GetNextSessionSeq(userID, agentType)
	if err != nil {
		return nil, fmt.Errorf("failed to get next session seq: %w", err)
	}
	sessionID := model.GenerateSessionID(userID, agentType, seq)
	session := model.NewSessionWithID(userID, sessionID, agentType)
	session.Title = title
	session.Model = modelName
	session.ParentSessionID = parentSessionID

	if e.Repo != nil {
		if rootNode, err := e.Repo.LoadNode("root"); err == nil {
			session.NodeDigests = []model.NodeDigest{summarizeNode(rootNode)}
		}
	}
	if err := e.Sessions.Put(session); err != nil {
		return nil, fmt.Errorf("failed to persist session: %w", err)
	}
	return session, nil
}

// CreateSubAgent creates a worker session owned by a conversation's main session.
// The worker cannot create further sub-agents.
func (e *Engine) CreateSubAgent(parentSessionID, title, modelName string) (*model.Session, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil, fmt.Errorf("parent session id is required")
	}
	parent, err := e.Sessions.Get(parentSessionID)
	if err != nil {
		return nil, fmt.Errorf("parent session not found: %w", err)
	}
	if parent.IsSubAgent() {
		return nil, ErrSubAgentNesting
	}
	if !parent.CanCreateSubAgent() {
		return nil, ErrNotConversationSession
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = parent.Model
	}
	return e.createTypedSession(parent.UserID, model.AgentTypeSub, strings.TrimSpace(title), strings.TrimSpace(modelName), parent.SessionID)
}

// GetConversation returns a conversation after verifying ownership.
func (e *Engine) GetConversation(userID, conversationID string) (*model.Conversation, error) {
	conv, err := e.Sessions.GetConversation(strings.TrimSpace(conversationID))
	if err != nil {
		return nil, err
	}
	if conv.UserID != strings.TrimSpace(userID) {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	return conv, nil
}

// ListConversations returns the user's conversations, last used first.
func (e *Engine) ListConversations(userID string) ([]*model.Conversation, error) {
	return e.Sessions.ListConversations(strings.TrimSpace(userID))
}

// RenameConversation updates only the conversation title and the linked session title.
func (e *Engine) RenameConversation(userID, conversationID, title string) error {
	conv, err := e.GetConversation(userID, conversationID)
	if err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("title is required")
	}
	conv.Title = title
	conv.UpdatedAt = time.Now()
	if err := e.Sessions.PutConversation(conv); err != nil {
		return err
	}
	session, err := e.Sessions.Get(conv.SessionID)
	if err != nil {
		return err
	}
	session.Title = title
	session.UpdatedAt = time.Now()
	return e.Sessions.Put(session)
}

// SetConversationModel updates the conversation model and the linked main session model.
func (e *Engine) SetConversationModel(userID, conversationID, modelName string) error {
	conv, err := e.GetConversation(userID, conversationID)
	if err != nil {
		return err
	}
	modelName = strings.TrimSpace(modelName)
	conv.Model = modelName
	if err := e.Sessions.PutConversation(conv); err != nil {
		return err
	}
	session, err := e.Sessions.Get(conv.SessionID)
	if err != nil {
		return err
	}
	session.Model = modelName
	session.UpdatedAt = time.Now()
	return e.Sessions.Put(session)
}

// SetConversationArchived toggles the archived flag without touching messages.
func (e *Engine) SetConversationArchived(userID, conversationID string, archived bool) error {
	conv, err := e.GetConversation(userID, conversationID)
	if err != nil {
		return err
	}
	conv.Archived = archived
	return e.Sessions.PutConversation(conv)
}

// DeleteConversation removes the conversation, its main session, and any
// sub-agent sessions that belong to that main session.
func (e *Engine) DeleteConversation(userID, conversationID string) error {
	conv, err := e.GetConversation(userID, conversationID)
	if err != nil {
		return err
	}
	sessions, err := e.Sessions.List(conv.UserID)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if session.ParentSessionID == conv.SessionID {
			_ = e.Sessions.Delete(session.SessionID)
		}
	}
	if err := e.Sessions.Delete(conv.SessionID); err != nil {
		return err
	}
	return e.Sessions.DeleteConversation(conv.ConversationID)
}

// ProcessConversation sends a user message through the conversation's main session.
func (e *Engine) ProcessConversation(ctx context.Context, userID, conversationID, message string) (string, int, error) {
	conv, err := e.GetConversation(userID, conversationID)
	if err != nil {
		return "", 0, err
	}
	return e.ProcessMessage(ctx, conv.SessionID, message)
}

func (e *Engine) touchOwningConversation(session *model.Session) {
	if session == nil {
		return
	}
	sessionID := session.SessionID
	if session.ParentSessionID != "" {
		sessionID = session.ParentSessionID
	}
	if err := e.Sessions.TouchConversationBySession(sessionID); err != nil {
		log.Log.Warnf("[Engine] ⚠️  Failed to touch conversation | SessionID: %s | Error: %v", sessionID, err)
	}
}
