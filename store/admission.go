package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/model"
)

// ErrSessionQueueFull means the session already holds the maximum number of
// non-terminal runs. The input was not stored and must not be reported as queued.
var ErrSessionQueueFull = errors.New("session queue is full")

// ErrSessionRunFence is returned when a stale worker finalizes a run it no longer owns.
var ErrSessionRunFence = errors.New("session run fence mismatch")

const sessionRunLease = 2 * time.Minute

func (s *SQLiteStore) AdmitSessionRun(in model.SessionRunInput) (model.SessionAdmitResult, error) {
	in.UserID = strings.TrimSpace(in.UserID)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.Content = strings.TrimSpace(in.Content)
	in.OriginKind = strings.TrimSpace(in.OriginKind)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.UserID == "" || in.SessionID == "" {
		return model.SessionAdmitResult{}, fmt.Errorf("session admission requires owner and session")
	}
	if in.Content == "" {
		return model.SessionAdmitResult{}, fmt.Errorf("session admission requires content")
	}
	if in.OriginKind == "" {
		in.OriginKind = "user"
	}
	if !s.isPostgres() {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.SessionAdmitResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := ensureSessionExecution(tx, in.UserID, in.SessionID, s.isPostgres()); err != nil {
		return model.SessionAdmitResult{}, err
	}
	if in.IdempotencyKey != "" {
		existing, err := findRunByIdempotency(tx, in.UserID, in.SessionID, in.IdempotencyKey)
		if err != nil {
			return model.SessionAdmitResult{}, err
		}
		if existing != nil {
			count, err := countOpenRuns(tx, in.UserID, in.SessionID)
			if err != nil {
				return model.SessionAdmitResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return model.SessionAdmitResult{}, err
			}
			return model.SessionAdmitResult{Run: *existing, Outcome: model.SessionAdmitDuplicate, QueuedCount: count}, nil
		}
	}
	open, err := countOpenRuns(tx, in.UserID, in.SessionID)
	if err != nil {
		return model.SessionAdmitResult{}, err
	}
	if open >= model.SessionQueueCapacity {
		return model.SessionAdmitResult{}, ErrSessionQueueFull
	}
	var seq int64
	if err := tx.QueryRow(`SELECT next_admission_seq FROM session_execution WHERE user_id = ? AND session_id = ?`, in.UserID, in.SessionID).Scan(&seq); err != nil {
		return model.SessionAdmitResult{}, err
	}
	now := time.Now().Unix()
	runID := fmt.Sprintf("%d", seq)
	if _, err := tx.Exec(`
		INSERT INTO session_runs (
			run_id, user_id, session_id, admission_seq, origin_kind, idempotency_key, content,
			status, phase, version, fence, attempts, error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', 1, 0, 0, '', ?, ?)`,
		runID, in.UserID, in.SessionID, seq, in.OriginKind, in.IdempotencyKey, in.Content,
		model.SessionRunQueued, now, now,
	); err != nil {
		return model.SessionAdmitResult{}, err
	}
	if _, err := tx.Exec(`
		UPDATE session_execution
		SET next_admission_seq = ?, revision = revision + 1
		WHERE user_id = ? AND session_id = ?`, seq+1, in.UserID, in.SessionID); err != nil {
		return model.SessionAdmitResult{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO session_run_events (event_id, user_id, session_id, run_id, event_seq, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, 'admitted', '', ?)`,
		in.UserID+"/"+in.SessionID+"/"+runID+"/admitted", in.UserID, in.SessionID, runID, seq, now,
	); err != nil {
		return model.SessionAdmitResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.SessionAdmitResult{}, err
	}
	return model.SessionAdmitResult{
		Outcome:     model.SessionAdmitAccepted,
		QueuedCount: open,
		Run: model.SessionRun{
			RunID: runID, UserID: in.UserID, SessionID: in.SessionID, AdmissionSeq: seq,
			OriginKind: in.OriginKind, IdempotencyKey: in.IdempotencyKey, Content: in.Content,
			Status: model.SessionRunQueued, Version: 1, CreatedAt: time.Unix(now, 0), UpdatedAt: time.Unix(now, 0),
		},
	}, nil
}

func (s *SQLiteStore) ClaimHeadSessionRun(userID, sessionID, workerID string) (*model.SessionRun, error) {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	workerID = strings.TrimSpace(workerID)
	if userID == "" || sessionID == "" || workerID == "" {
		return nil, fmt.Errorf("session claim requires owner, session, and worker")
	}
	if !s.isPostgres() {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionExecution(tx, userID, sessionID, s.isPostgres()); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var active sql.NullString
	var leaseExpires int64
	if err := tx.QueryRow(`SELECT active_run_id, lease_expires FROM session_execution WHERE user_id = ? AND session_id = ?`, userID, sessionID).Scan(&active, &leaseExpires); err != nil {
		return nil, err
	}
	if active.Valid && active.String != "" && leaseExpires > now {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if active.Valid && active.String != "" && leaseExpires <= now {
		if _, err := tx.Exec(`
			UPDATE session_runs SET status = ?, error = ?, updated_at = ?, version = version + 1
			WHERE user_id = ? AND session_id = ? AND run_id = ? AND status = ?`,
			model.SessionRunInterrupted, "lease expired", now, userID, sessionID, active.String, model.SessionRunRunning,
		); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE session_execution SET active_run_id = '', lease_owner = '', lease_expires = 0, revision = revision + 1 WHERE user_id = ? AND session_id = ?`, userID, sessionID); err != nil {
			return nil, err
		}
	}
	row := tx.QueryRow(`
		SELECT run_id, user_id, session_id, admission_seq, origin_kind, idempotency_key, content, status, phase, version, fence, attempts, error, created_at, updated_at
		FROM session_runs
		WHERE user_id = ? AND session_id = ? AND status = ?
		ORDER BY admission_seq ASC
		LIMIT 1`, userID, sessionID, model.SessionRunQueued)
	run, err := scanSessionRun(row)
	if err == sql.ErrNoRows {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fence int64
	if err := tx.QueryRow(`SELECT fence FROM session_execution WHERE user_id = ? AND session_id = ?`, userID, sessionID).Scan(&fence); err != nil {
		return nil, err
	}
	fence++
	expires := time.Now().Add(sessionRunLease).Unix()
	if _, err := tx.Exec(`
		UPDATE session_runs
		SET status = ?, fence = ?, attempts = attempts + 1, updated_at = ?, version = version + 1
		WHERE user_id = ? AND session_id = ? AND run_id = ? AND status = ?`,
		model.SessionRunRunning, fence, now, userID, sessionID, run.RunID, model.SessionRunQueued,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		UPDATE session_execution
		SET active_run_id = ?, lease_owner = ?, lease_expires = ?, fence = ?, revision = revision + 1
		WHERE user_id = ? AND session_id = ?`,
		run.RunID, workerID, expires, fence, userID, sessionID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	run.Status = model.SessionRunRunning
	run.Fence = fence
	run.Attempts++
	return run, nil
}

func (s *SQLiteStore) HeartbeatSessionRun(userID, sessionID, runID, workerID string, fence int64) error {
	if !s.isPostgres() {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	expires := time.Now().Add(sessionRunLease).Unix()
	res, err := s.execWrite(`
		UPDATE session_execution
		SET lease_expires = ?
		WHERE user_id = ? AND session_id = ? AND active_run_id = ? AND lease_owner = ? AND fence = ?`,
		expires, userID, sessionID, runID, workerID, fence,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSessionRunFence
	}
	return nil
}

func (s *SQLiteStore) FinalizeSessionRun(userID, sessionID, runID string, fence int64, status, errText string) error {
	if !model.SessionRunTerminal(status) {
		return fmt.Errorf("session finalize requires a terminal status")
	}
	if status == model.SessionRunSucceeded && strings.TrimSpace(errText) != "" {
		return fmt.Errorf("succeeded run cannot carry an error")
	}
	if !s.isPostgres() {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currentFence int64
	var currentStatus string
	err = tx.QueryRow(`SELECT fence, status FROM session_runs WHERE user_id = ? AND session_id = ? AND run_id = ?`, userID, sessionID, runID).Scan(&currentFence, &currentStatus)
	if err != nil {
		return err
	}
	if model.SessionRunTerminal(currentStatus) {
		if currentStatus == status {
			return tx.Commit()
		}
		return ErrSessionRunFence
	}
	if currentFence != fence {
		return ErrSessionRunFence
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		UPDATE session_runs SET status = ?, error = ?, updated_at = ?, version = version + 1
		WHERE user_id = ? AND session_id = ? AND run_id = ? AND fence = ?`,
		status, strings.TrimSpace(errText), now, userID, sessionID, runID, fence,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE session_execution
		SET active_run_id = CASE WHEN active_run_id = ? THEN '' ELSE active_run_id END,
		    lease_owner = CASE WHEN active_run_id = ? THEN '' ELSE lease_owner END,
		    lease_expires = CASE WHEN active_run_id = ? THEN 0 ELSE lease_expires END,
		    revision = revision + 1
		WHERE user_id = ? AND session_id = ?`,
		runID, runID, runID, userID, sessionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO session_run_events (event_id, user_id, session_id, run_id, event_seq, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID+"/"+sessionID+"/"+runID+"/"+status, userID, sessionID, runID, now, status, truncateRunPayload(errText), now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) SnapshotSessionExecution(userID, sessionID string) (model.SessionExecutionSnapshot, error) {
	if !s.isPostgres() {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	var snap model.SessionExecutionSnapshot
	snap.UserID = userID
	snap.SessionID = sessionID
	err := s.db.QueryRow(`
		SELECT active_run_id, revision, fence, next_admission_seq
		FROM session_execution WHERE user_id = ? AND session_id = ?`, userID, sessionID).Scan(
		&snap.ActiveRunID, &snap.Revision, &snap.Fence, &snap.NextAdmission,
	)
	if err == sql.ErrNoRows {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	snap.QueuedCount, err = countOpenRuns(s.db, userID, sessionID)
	return snap, err
}

func (s *SQLiteStore) GetSessionRun(userID, sessionID, runID string) (*model.SessionRun, error) {
	if !s.isPostgres() {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}
	row := s.db.QueryRow(`
		SELECT run_id, user_id, session_id, admission_seq, origin_kind, idempotency_key, content, status, phase, version, fence, attempts, error, created_at, updated_at
		FROM session_runs WHERE user_id = ? AND session_id = ? AND run_id = ?`, userID, sessionID, runID)
	run, err := scanSessionRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return run, err
}

func (s *SQLiteStore) isPostgres() bool { return s.path == "postgresql" }

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func ensureSessionExecution(tx *sql.Tx, userID, sessionID string, postgres bool) error {
	if _, err := tx.Exec(`
		INSERT INTO session_execution (
			user_id, session_id, next_admission_seq, active_run_id, revision, lease_owner, lease_expires, fence
		) VALUES (?, ?, 1, '', 0, '', 0, 0)
		ON CONFLICT(user_id, session_id) DO NOTHING`, userID, sessionID); err != nil {
		return err
	}
	lock := `SELECT next_admission_seq FROM session_execution WHERE user_id = ? AND session_id = ?`
	if postgres {
		lock += ` FOR UPDATE`
	}
	var seq int64
	return tx.QueryRow(lock, userID, sessionID).Scan(&seq)
}

func findRunByIdempotency(tx *sql.Tx, userID, sessionID, key string) (*model.SessionRun, error) {
	row := tx.QueryRow(`
		SELECT run_id, user_id, session_id, admission_seq, origin_kind, idempotency_key, content, status, phase, version, fence, attempts, error, created_at, updated_at
		FROM session_runs
		WHERE user_id = ? AND session_id = ? AND idempotency_key = ?`, userID, sessionID, key)
	run, err := scanSessionRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return run, nil
}

func countOpenRuns(q queryRower, userID, sessionID string) (int, error) {
	var n int
	err := q.QueryRow(`
		SELECT COUNT(*) FROM session_runs
		WHERE user_id = ? AND session_id = ? AND status IN (?, ?, ?)`,
		userID, sessionID, model.SessionRunQueued, model.SessionRunRunning, model.SessionRunWaitingApproval,
	).Scan(&n)
	return n, err
}

type sessionRunScanner interface {
	Scan(dest ...any) error
}

func scanSessionRun(row sessionRunScanner) (*model.SessionRun, error) {
	var run model.SessionRun
	var created, updated int64
	err := row.Scan(
		&run.RunID, &run.UserID, &run.SessionID, &run.AdmissionSeq, &run.OriginKind, &run.IdempotencyKey,
		&run.Content, &run.Status, &run.Phase, &run.Version, &run.Fence, &run.Attempts, &run.Error, &created, &updated,
	)
	if err != nil {
		return nil, err
	}
	run.CreatedAt = time.Unix(created, 0)
	run.UpdatedAt = time.Unix(updated, 0)
	return &run, nil
}

func truncateRunPayload(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 500 {
		return text[:500]
	}
	return text
}
