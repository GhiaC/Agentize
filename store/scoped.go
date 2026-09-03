package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/ghiac/agentize/model"
)

func errAmbiguousID(kind, id string) error {
	return fmt.Errorf("%s %q is not unique; look up with user id", kind, id)
}

func (s *SQLiteStore) errIfAmbiguousLocked(table, column, id string) error {
	query, ok := ambiguousCountSQL(table, column)
	if !ok {
		return fmt.Errorf("store: unknown uniqueness check %s.%s", table, column)
	}
	var n int
	if err := s.db.QueryRow(query, id).Scan(&n); err != nil {
		return fmt.Errorf("failed to check %s uniqueness: %w", table, err)
	}
	if n > 1 {
		return errAmbiguousID(table, id)
	}
	return nil
}

func ambiguousCountSQL(table, column string) (string, bool) {
	switch table + "." + column {
	case "sessions.session_id":
		return "SELECT COUNT(*) FROM sessions WHERE session_id = ?", true
	case "conversations.conversation_id":
		return "SELECT COUNT(*) FROM conversations WHERE conversation_id = ?", true
	case "conversations.session_id":
		return "SELECT COUNT(*) FROM conversations WHERE session_id = ?", true
	case "user_files.file_id":
		return "SELECT COUNT(*) FROM user_files WHERE file_id = ?", true
	case "workflow_runs.workflow_id":
		return "SELECT COUNT(*) FROM workflow_runs WHERE workflow_id = ?", true
	case "task_schedules.schedule_id":
		return "SELECT COUNT(*) FROM task_schedules WHERE schedule_id = ?", true
	case "messages.session_id":
		return "SELECT COUNT(DISTINCT user_id) FROM messages WHERE session_id = ?", true
	case "tool_calls.tool_id":
		return "SELECT COUNT(*) FROM tool_calls WHERE tool_id = ?", true
	case "tool_calls.session_id":
		return "SELECT COUNT(DISTINCT user_id) FROM tool_calls WHERE session_id = ?", true
	case "route_traces.trace_id":
		return "SELECT COUNT(*) FROM route_traces WHERE trace_id = ?", true
	case "summarization_logs.log_id":
		return "SELECT COUNT(*) FROM summarization_logs WHERE log_id = ?", true
	default:
		return "", false
	}
}

func (s *SQLiteStore) GetUserMessagesBySession(userID, sessionID string) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if userID == "" {
		if err := s.errIfAmbiguousLocked("messages", "session_id", sessionID); err != nil {
			return nil, err
		}
	}

	var all []*model.Message
	for offset := 0; ; offset += messagesPageSize {
		page, err := s.getMessagesBySessionPageLocked(userID, sessionID, messagesPageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < messagesPageSize {
			return all, nil
		}
	}
}

func (s *SQLiteStore) GetUserMessagesBySessionPage(userID, sessionID string, limit, offset int) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if userID == "" {
		if err := s.errIfAmbiguousLocked("messages", "session_id", sessionID); err != nil {
			return nil, err
		}
	}
	return s.getMessagesBySessionPageLocked(userID, sessionID, limit, offset)
}

func (s *SQLiteStore) GetUserFileForUser(userID, fileID string) (*model.UserFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(
		`SELECT `+userFileColumns+` FROM user_files WHERE user_id = ? AND file_id = ?`,
		userID, fileID,
	)
	f, err := scanUserFile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user file: %w", err)
	}
	return f, nil
}

func (s *SQLiteStore) GetUserWorkflowRun(userID, workflowID string) (*model.WorkflowRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	err := s.db.QueryRow(
		`SELECT data FROM workflow_runs WHERE user_id = ? AND workflow_id = ?`,
		userID, workflowID,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query workflow run: %w", err)
	}
	workflow := &model.WorkflowRun{}
	if err := json.Unmarshal([]byte(data), workflow); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workflow run: %w", err)
	}
	return workflow, nil
}

func (s *SQLiteStore) GetUserTaskSchedule(userID, scheduleID string) (*model.TaskSchedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	err := s.db.QueryRow(
		`SELECT data FROM task_schedules WHERE user_id = ? AND schedule_id = ?`,
		userID, scheduleID,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query task schedule: %w", err)
	}
	schedule := &model.TaskSchedule{}
	if err := json.Unmarshal([]byte(data), schedule); err != nil {
		return nil, fmt.Errorf("failed to unmarshal task schedule: %w", err)
	}
	return schedule, nil
}
