package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ghiac/agentize/model"
)

const fileOperationsSchema = `
CREATE TABLE IF NOT EXISTS file_counters (user_id TEXT PRIMARY KEY, seq BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS file_operations (
 user_id TEXT NOT NULL, operation_key TEXT NOT NULL, revision BIGINT NOT NULL,
 data TEXT NOT NULL, PRIMARY KEY (user_id, operation_key));
`

func fileSequenceSeed(files []*model.UserFile) int64 {
	var max int64
	for _, file := range files {
		if file == nil {
			continue
		}
		if n, err := strconv.ParseInt(file.FileID, 10, 64); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

func (s *SQLiteStore) NextUserFileID(ctx context.Context, userID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", fmt.Errorf("file owner is required")
	}
	var existing int64
	if err := s.db.QueryRowContext(ctx, `SELECT seq FROM file_counters WHERE user_id=?`, userID).Scan(&existing); err == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		var next int
		if err := s.db.QueryRowContext(ctx, `UPDATE file_counters SET seq=seq+1 WHERE user_id=? RETURNING seq`, userID).Scan(&next); err != nil {
			return "", err
		}
		return model.FormatID(next), nil
	} else if err != sql.ErrNoRows {
		return "", err
	}
	files, err := s.GetUserFilesByUser(userID)
	if err != nil {
		return "", err
	}
	seed := fileSequenceSeed(files)
	if user, err := s.GetUser(userID); err != nil {
		return "", err
	} else if user != nil && int64(user.FileSeq) >= seed {
		seed = int64(user.FileSeq) + 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var seq int
	err = s.db.QueryRowContext(ctx, `INSERT INTO file_counters (user_id,seq) VALUES (?,?)
 ON CONFLICT(user_id) DO UPDATE SET seq = CASE WHEN file_counters.seq < excluded.seq THEN excluded.seq ELSE file_counters.seq + 1 END RETURNING seq`, userID, seed).Scan(&seq)
	if err != nil {
		return "", fmt.Errorf("allocate file id: %w", err)
	}
	return model.FormatID(seq), nil
}

func validFileOperation(op *model.FileOperation) bool {
	return op != nil && strings.TrimSpace(op.UserID) != "" && op.Key != "" && op.Kind != "" && op.State != ""
}

func (s *SQLiteStore) ClaimFileOperation(ctx context.Context, op *model.FileOperation) (bool, *model.FileOperation, error) {
	if !validFileOperation(op) {
		return false, nil, fmt.Errorf("invalid file operation")
	}
	op.Revision = 1
	op.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(op)
	if err != nil {
		return false, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.execFileWrite(ctx, `INSERT INTO file_operations (user_id,operation_key,revision,data) VALUES (?,?,?,?) ON CONFLICT(user_id,operation_key) DO NOTHING`, op.UserID, op.Key, op.Revision, string(raw))
	if err != nil {
		return false, nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, nil, err
	}
	if n == 1 {
		return true, op, nil
	}
	var data string
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM file_operations WHERE user_id=? AND operation_key=?`, op.UserID, op.Key).Scan(&data); err != nil {
		return false, nil, err
	}
	var old model.FileOperation
	if err := json.Unmarshal([]byte(data), &old); err != nil {
		return false, nil, err
	}
	return false, &old, nil
}

func (s *SQLiteStore) UpdateFileOperation(ctx context.Context, op *model.FileOperation, revision int64) (bool, error) {
	if !validFileOperation(op) || revision < 1 {
		return false, fmt.Errorf("invalid file operation revision")
	}
	copy := *op
	copy.Revision = revision + 1
	copy.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(&copy)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.execFileWrite(ctx, `UPDATE file_operations SET revision=?,data=? WHERE user_id=? AND operation_key=? AND revision=?`, copy.Revision, string(raw), op.UserID, op.Key, revision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if n == 1 {
		*op = copy
	}
	return n == 1, err
}

func (s *SQLiteStore) ListFileOperations(ctx context.Context, userID string) ([]*model.FileOperation, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("file owner is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM file_operations WHERE user_id=? ORDER BY operation_key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.FileOperation
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var op model.FileOperation
		if err := json.Unmarshal([]byte(data), &op); err != nil {
			return nil, err
		}
		out = append(out, &op)
	}
	return out, rows.Err()
}

func (s *DBStore) NextUserFileID(ctx context.Context, userID string) (string, error) {
	return s.sqliteStore.NextUserFileID(ctx, userID)
}
func (s *DBStore) ClaimFileOperation(ctx context.Context, op *model.FileOperation) (bool, *model.FileOperation, error) {
	return s.sqliteStore.ClaimFileOperation(ctx, op)
}
func (s *DBStore) UpdateFileOperation(ctx context.Context, op *model.FileOperation, revision int64) (bool, error) {
	return s.sqliteStore.UpdateFileOperation(ctx, op, revision)
}
func (s *DBStore) ListFileOperations(ctx context.Context, userID string) ([]*model.FileOperation, error) {
	return s.sqliteStore.ListFileOperations(ctx, userID)
}

func fileDetailsJSON(details model.FileDetails) string {
	raw, _ := json.Marshal(details)
	return string(raw)
}

func (s *SQLiteStore) CompleteFileUpload(ctx context.Context, op *model.FileOperation, revision int64, file *model.UserFile) (bool, error) {
	if !validFileOperation(op) || file == nil || file.UserID != op.UserID || file.Details.IngestionKey != op.Key {
		return false, fmt.Errorf("invalid upload finalization")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUserFileQuota(file); err != nil {
		return false, err
	}
	copy := *op
	copy.State = "ready"
	copy.File = file
	copy.Revision = revision + 1
	copy.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(&copy)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE file_operations SET revision=?,data=? WHERE user_id=? AND operation_key=? AND revision=?`, copy.Revision, string(raw), op.UserID, op.Key, revision)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO user_files (`+userFileColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, file.FileID, file.UserID, file.SessionID, file.Name, file.MIMEType, file.Size, file.StorageKey, string(file.Source), file.ParentFileID, file.Summary, file.CreatedAt.Unix(), fileDetailsJSON(file.Details))
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	*op = copy
	return true, nil
}
func (s *DBStore) CompleteFileUpload(ctx context.Context, op *model.FileOperation, revision int64, file *model.UserFile) (bool, error) {
	return s.sqliteStore.CompleteFileUpload(ctx, op, revision, file)
}

func (s *SQLiteStore) execFileWrite(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	for attempt := 0; ; attempt++ {
		result, err := s.db.ExecContext(ctx, query, args...)
		if err == nil || !isSQLiteBusy(err) || attempt >= 8 {
			return result, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
}
