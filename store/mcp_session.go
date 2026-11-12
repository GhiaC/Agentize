package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/ghiac/agentize/model"
	"time"
)

// ClaimMCPToolRun claims an idle session atomically with normal chat admission.
// External calls are never queued or replayed as messages after a crash. Only
// bounded metadata is recorded; arguments and file bytes are not admission data.
func (s *SQLiteStore) ClaimMCPToolRun(ctx context.Context, userID, sessionID, tool string) (*model.SessionRun, error) {
	if userID == "" || sessionID == "" || tool == "" {
		return nil, errors.New("MCP tool requires an owned session")
	}
	if !s.isPostgres() {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE user_id=? AND session_id=?`, userID, sessionID).Scan(&owner); err != nil {
		return nil, err
	}
	if err := ensureSessionExecutionContext(ctx, tx, userID, sessionID, s.isPostgres()); err != nil {
		return nil, err
	}
	var active string
	var expires int64
	if err := tx.QueryRowContext(ctx, `SELECT active_run_id,lease_expires FROM session_execution WHERE user_id=? AND session_id=?`, userID, sessionID).Scan(&active, &expires); err != nil {
		return nil, err
	}
	if active != "" && expires <= time.Now().Unix() {
		if _, err := tx.ExecContext(ctx, `UPDATE session_runs SET status=?,error='lease expired',updated_at=?,version=version+1 WHERE user_id=? AND session_id=? AND run_id=? AND status=?`, model.SessionRunInterrupted, time.Now().Unix(), userID, sessionID, active, model.SessionRunRunning); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE session_execution SET active_run_id='',lease_owner='',lease_expires=0,revision=revision+1 WHERE user_id=? AND session_id=?`, userID, sessionID); err != nil {
			return nil, err
		}
	}
	open, err := countOpenRuns(tx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, errors.New("conversation is busy")
	}
	var seq, fence int64
	if err := tx.QueryRowContext(ctx, `SELECT next_admission_seq,fence FROM session_execution WHERE user_id=? AND session_id=?`, userID, sessionID).Scan(&seq, &fence); err != nil {
		return nil, err
	}
	fence++
	now := time.Now().Unix()
	id := fmt.Sprintf("%d", seq)
	_, err = tx.ExecContext(ctx, `INSERT INTO session_runs (run_id,user_id,session_id,admission_seq,origin_kind,idempotency_key,content,metadata,status,phase,version,fence,attempts,error,created_at,updated_at) VALUES (?,?,?,?,'mcp-tool','',?,'{}',?,'',1,?,1,'',?,?)`, id, userID, sessionID, seq, tool, model.SessionRunRunning, fence, now, now)
	if err != nil {
		return nil, err
	}
	// MCP handlers stop at five minutes. A six-minute lease prevents another
	// replica from claiming the session during that bounded execution window.
	_, err = tx.ExecContext(ctx, `UPDATE session_execution SET next_admission_seq=?,active_run_id=?,lease_owner='mcp',lease_expires=?,fence=?,revision=revision+1 WHERE user_id=? AND session_id=?`, seq+1, id, now+360, fence, userID, sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &model.SessionRun{RunID: id, UserID: userID, SessionID: sessionID, Fence: fence, Status: model.SessionRunRunning, OriginKind: "mcp-tool"}, nil
}

// ClaimMCPToolRun forwards the explicit local library backend to its durable store.
func (s *DBStore) ClaimMCPToolRun(ctx context.Context, userID, sessionID, tool string) (*model.SessionRun, error) {
	return s.sqliteStore.ClaimMCPToolRun(ctx, userID, sessionID, tool)
}
