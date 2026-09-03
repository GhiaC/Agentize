package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghiac/agentize/model"
)

func scanConversation(data string, createdAt, updatedAt int64) (*model.Conversation, error) {
	c := &model.Conversation{}
	if err := json.Unmarshal([]byte(data), c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal conversation: %w", err)
	}
	c.CreatedAt = time.Unix(createdAt, 0)
	c.UpdatedAt = time.Unix(updatedAt, 0)
	return c, nil
}

func (s *SQLiteStore) GetConversation(conversationID string) (*model.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := s.errIfAmbiguousLocked("conversations", "conversation_id", conversationID); err != nil {
		return nil, err
	}

	var data string
	var createdAt, updatedAt int64
	err := s.db.QueryRow(
		`SELECT data, created_at, updated_at FROM conversations WHERE conversation_id = ?`,
		conversationID,
	).Scan(&data, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation: %w", err)
	}
	return scanConversation(data, createdAt, updatedAt)
}

func (s *SQLiteStore) GetUserConversation(userID, conversationID string) (*model.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	var createdAt, updatedAt int64
	err := s.db.QueryRow(
		`SELECT data, created_at, updated_at FROM conversations WHERE user_id = ? AND conversation_id = ?`,
		userID, conversationID,
	).Scan(&data, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation not found: %s", conversationID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation: %w", err)
	}
	return scanConversation(data, createdAt, updatedAt)
}

func (s *SQLiteStore) PutConversation(conversation *model.Conversation) error {
	fillConversationIDs(conversation)
	if err := validateConversation(conversation); err != nil {
		return err
	}
	if _, err := s.GetOrCreateUser(conversation.UserID); err != nil {
		return fmt.Errorf("ensure user for conversation: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = time.Now()
	}
	if conversation.UpdatedAt.IsZero() {
		conversation.UpdatedAt = time.Now()
	}
	data, err := json.Marshal(conversation)
	if err != nil {
		return fmt.Errorf("failed to marshal conversation: %w", err)
	}
	archived := 0
	if conversation.Archived {
		archived = 1
	}
	_, err = s.execWrite(
		`INSERT OR REPLACE INTO conversations (
			conversation_id, user_id, session_id, conversation_seq, title, model, archived, data, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		conversation.ConversationID,
		conversation.UserID,
		conversation.SessionID,
		conversation.Seq,
		conversation.Title,
		conversation.Model,
		archived,
		string(data),
		conversation.CreatedAt.Unix(),
		conversation.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to store conversation: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteConversation(conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.execWrite(`DELETE FROM conversations WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return fmt.Errorf("failed to delete conversation: %w", err)
	}
	auditDeletion("conversation", conversationID, "")
	return nil
}

func (s *SQLiteStore) ListConversations(userID string) ([]*model.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT data, created_at, updated_at FROM conversations WHERE user_id = ? ORDER BY updated_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}
	defer rows.Close()
	return scanConversationRows(rows)
}

func (s *SQLiteStore) ListAllConversations() ([]*model.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT data, created_at, updated_at FROM conversations ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list all conversations: %w", err)
	}
	defer rows.Close()
	return scanConversationRows(rows)
}

func scanConversationRows(rows *sql.Rows) ([]*model.Conversation, error) {
	out := make([]*model.Conversation, 0)
	for rows.Next() {
		var data string
		var createdAt, updatedAt int64
		if err := rows.Scan(&data, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan conversation: %w", err)
		}
		c, err := scanConversation(data, createdAt, updatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLiteStore) GetConversationBySession(sessionID string) (*model.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := s.errIfAmbiguousLocked("conversations", "session_id", sessionID); err != nil {
		return nil, err
	}

	var data string
	var createdAt, updatedAt int64
	err := s.db.QueryRow(
		`SELECT data, created_at, updated_at FROM conversations WHERE session_id = ?`,
		sessionID,
	).Scan(&data, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation by session: %w", err)
	}
	return scanConversation(data, createdAt, updatedAt)
}

func (s *SQLiteStore) GetUserConversationBySession(userID, sessionID string) (*model.Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	var createdAt, updatedAt int64
	err := s.db.QueryRow(
		`SELECT data, created_at, updated_at FROM conversations WHERE user_id = ? AND session_id = ?`,
		userID, sessionID,
	).Scan(&data, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query conversation by session: %w", err)
	}
	return scanConversation(data, createdAt, updatedAt)
}

func (s *SQLiteStore) GetNextConversationSeq(userID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var maxSeq sql.NullInt64
	err := s.db.QueryRow(
		`SELECT MAX(conversation_seq) FROM conversations WHERE user_id = ?`,
		userID,
	).Scan(&maxSeq)
	if err != nil {
		return 0, fmt.Errorf("failed to get max conversation seq: %w", err)
	}
	if maxSeq.Valid {
		return int(maxSeq.Int64) + 1, nil
	}
	return 1, nil
}

func (s *SQLiteStore) TouchConversationBySession(sessionID string) error {
	conv, err := s.GetConversationBySession(sessionID)
	if err != nil || conv == nil {
		return err
	}
	bumpConversationActivity(conv)
	return s.PutConversation(conv)
}

func bumpConversationActivity(c *model.Conversation) {
	now := time.Now().UTC().Truncate(time.Second)
	prev := c.UpdatedAt.UTC().Truncate(time.Second)
	if !now.After(prev) {
		now = prev.Add(time.Second)
	}
	c.UpdatedAt = now
}
