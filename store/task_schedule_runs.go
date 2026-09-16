package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
)

func requireOwnerID(kind, userID, id string) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("%s %q requires user id", kind, id)
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s id is required", kind)
	}
	return nil
}

func applySQLiteTaskScheduleRunUserIDs(tx *sql.Tx) error {
	if err := addColumns(tx, "task_schedule_runs", `user_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE task_schedule_runs
		SET user_id = COALESCE(json_extract(data, '$.user_id'), '')
		WHERE user_id = '' OR user_id IS NULL`); err != nil {
		return fmt.Errorf("backfill task_schedule_runs.user_id: %w", err)
	}
	return execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_task_schedule_runs_user_schedule_started
			ON task_schedule_runs(user_id, schedule_id, started_at DESC, run_id DESC)`,
	)
}

func upsertTaskScheduleRunSQL() string {
	return `INSERT INTO task_schedule_runs (run_id, schedule_id, user_id, status, data, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			schedule_id=excluded.schedule_id,
			user_id=excluded.user_id,
			status=excluded.status,
			data=excluded.data,
			completed_at=excluded.completed_at`
}

func upsertTaskScheduleSQL() string {
	return `INSERT INTO task_schedules
			(schedule_id, user_id, session_id, status, next_run_at, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, schedule_id) DO UPDATE SET
			user_id=excluded.user_id,
			session_id=excluded.session_id,
			status=excluded.status,
			next_run_at=excluded.next_run_at,
			data=excluded.data,
			updated_at=excluded.updated_at`
}

func taskScheduleRunArgs(run *model.TaskScheduleRun, data string) []interface{} {
	var completedAt int64
	if !run.CompletedAt.IsZero() {
		completedAt = run.CompletedAt.Unix()
	}
	return []interface{}{
		run.RunID, run.ScheduleID, run.UserID, string(run.Status), data, run.StartedAt.Unix(), completedAt,
	}
}
