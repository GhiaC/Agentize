package store

import (
	"database/sql"
	"fmt"
)

// applySQLiteScopedKeys rebuilds Agentize tables so numeric ids are unique per
// parent (user / session / message), not globally. Two users may both own
// session "2". This copies existing rows as-is; it does not rewrite concat ids.
func applySQLiteScopedKeys(tx *sql.Tx) error {
	if err := rebuildSQLiteTable(tx, "sessions",
		`CREATE TABLE sessions_scoped (
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			agent_type TEXT NOT NULL,
			session_seq INTEGER NOT NULL DEFAULT 0,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, session_id)
		)`,
		"session_id, user_id, agent_type, session_seq, data, created_at, updated_at",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user_agent ON sessions(user_id, agent_type)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_user_core ON sessions(user_id, agent_type) WHERE agent_type = 'core'`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "conversations",
		`CREATE TABLE conversations_scoped (
			user_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			conversation_seq INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			archived INTEGER NOT NULL DEFAULT 0,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, conversation_id)
		)`,
		"conversation_id, user_id, session_id, conversation_seq, title, model, archived, data, created_at, updated_at",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_conversations_user_updated ON conversations(user_id, updated_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_user_session ON conversations(user_id, session_id)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "messages",
		`CREATE TABLE messages_scoped (
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			seq_id INTEGER DEFAULT 0,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			model TEXT,
			agent_type TEXT DEFAULT '',
			content_type TEXT DEFAULT '',
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			request_model TEXT,
			max_tokens INTEGER,
			temperature REAL,
			has_tool_calls INTEGER DEFAULT 0,
			finish_reason TEXT,
			created_at INTEGER NOT NULL,
			metadata TEXT DEFAULT '',
			PRIMARY KEY (user_id, session_id, message_id)
		)`,
		"message_id, seq_id, user_id, session_id, role, content, model, agent_type, content_type, prompt_tokens, completion_tokens, total_tokens, request_model, max_tokens, temperature, has_tool_calls, finish_reason, created_at, metadata",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_messages_user_id ON messages(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq_id)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "opened_files",
		`CREATE TABLE opened_files_scoped (
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			file_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			file_name TEXT,
			opened_at INTEGER NOT NULL,
			closed_at INTEGER,
			is_open INTEGER DEFAULT 1,
			PRIMARY KEY (user_id, session_id, file_id)
		)`,
		"file_id, session_id, user_id, file_path, file_name, opened_at, closed_at, is_open",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_opened_files_session_id ON opened_files(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_opened_files_user_id ON opened_files(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_opened_files_file_path ON opened_files(file_path)`,
		`CREATE INDEX IF NOT EXISTS idx_opened_files_is_open ON opened_files(is_open)`,
		`CREATE INDEX IF NOT EXISTS idx_opened_files_session_open ON opened_files(session_id, is_open)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "user_files",
		`CREATE TABLE user_files_scoped (
			user_id TEXT NOT NULL,
			file_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			name TEXT,
			mime_type TEXT,
			size INTEGER DEFAULT 0,
			storage_key TEXT NOT NULL,
			source TEXT DEFAULT 'uploaded',
			parent_file_id TEXT DEFAULT '',
			summary TEXT,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, file_id)
		)`,
		"file_id, user_id, session_id, name, mime_type, size, storage_key, source, parent_file_id, summary, created_at",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_user_files_user_id ON user_files(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_files_session_id ON user_files(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_files_created_at ON user_files(created_at)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "tool_calls",
		`CREATE TABLE tool_calls_scoped (
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			message_id TEXT NOT NULL,
			tool_id TEXT NOT NULL,
			tool_call_id TEXT NOT NULL DEFAULT '',
			user_message_id TEXT DEFAULT '',
			agent_type TEXT DEFAULT '',
			function_name TEXT NOT NULL,
			display_label TEXT DEFAULT '',
			arguments TEXT NOT NULL,
			response TEXT DEFAULT '',
			response_length INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			status TEXT DEFAULT 'pending',
			error TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, session_id, message_id, tool_id)
		)`,
		"",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_message_id ON tool_calls(message_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_session_id ON tool_calls(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_user_id ON tool_calls(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_created_at ON tool_calls(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_user_message_id ON tool_calls(user_message_id)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "summarization_logs",
		`CREATE TABLE summarization_logs_scoped (
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			log_id TEXT NOT NULL,
			session_title TEXT,
			previous_summary TEXT,
			previous_tags TEXT,
			messages_before_count INTEGER DEFAULT 0,
			messages_after_count INTEGER DEFAULT 0,
			archived_messages_count INTEGER DEFAULT 0,
			prompt_sent TEXT NOT NULL,
			response_received TEXT,
			model_used TEXT NOT NULL,
			requested_model TEXT,
			generated_summary TEXT,
			generated_tags TEXT,
			generated_title TEXT,
			prompt_tokens INTEGER DEFAULT 0,
			completion_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			status TEXT NOT NULL,
			error_message TEXT,
			summarization_type TEXT,
			created_at INTEGER NOT NULL,
			completed_at INTEGER,
			PRIMARY KEY (user_id, session_id, log_id)
		)`,
		"log_id, session_id, user_id, session_title, previous_summary, previous_tags, messages_before_count, messages_after_count, archived_messages_count, prompt_sent, response_received, model_used, requested_model, generated_summary, generated_tags, generated_title, prompt_tokens, completion_tokens, total_tokens, duration_ms, status, error_message, summarization_type, created_at, completed_at",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_summarization_logs_session_id ON summarization_logs(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_summarization_logs_user_id ON summarization_logs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_summarization_logs_created_at ON summarization_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_summarization_logs_status ON summarization_logs(status)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "route_traces",
		`CREATE TABLE route_traces_scoped (
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			status TEXT DEFAULT '',
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, session_id, trace_id)
		)`,
		"trace_id, session_id, user_id, status, data, created_at",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_route_traces_session_id ON route_traces(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_route_traces_user_id ON route_traces(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_route_traces_created_at ON route_traces(created_at)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "workflow_runs",
		`CREATE TABLE workflow_runs_scoped (
			user_id TEXT NOT NULL,
			workflow_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			status TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, workflow_id)
		)`,
		"workflow_id, user_id, session_id, status, data, created_at, updated_at",
	); err != nil {
		return err
	}
	if err := execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_workflow_runs_user_created ON workflow_runs(user_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_runs_session_created ON workflow_runs(session_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_runs_status_updated ON workflow_runs(status, updated_at DESC)`,
	); err != nil {
		return err
	}

	if err := rebuildSQLiteTable(tx, "task_schedules",
		`CREATE TABLE task_schedules_scoped (
			user_id TEXT NOT NULL,
			schedule_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			status TEXT NOT NULL,
			next_run_at INTEGER NOT NULL,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, schedule_id)
		)`,
		"schedule_id, user_id, session_id, status, next_run_at, data, created_at, updated_at",
	); err != nil {
		return err
	}
	return execAll(tx,
		`CREATE INDEX IF NOT EXISTS idx_task_schedules_user_id ON task_schedules(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_task_schedules_status_next ON task_schedules(status, next_run_at)`,
		`CREATE INDEX IF NOT EXISTS idx_task_schedules_created_at ON task_schedules(created_at)`,
	)
}

func rebuildSQLiteTable(tx *sql.Tx, table, createSQL, columns string) error {
	tmp := table + "_scoped"
	if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + tmp); err != nil {
		return fmt.Errorf("drop %s: %w", tmp, err)
	}
	if _, err := tx.Exec(createSQL); err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	var copySQL string
	if table == "tool_calls" {
		copySQL = `INSERT INTO tool_calls_scoped (
			user_id, session_id, message_id, tool_id, tool_call_id, user_message_id,
			agent_type, function_name, display_label, arguments, response, response_length,
			duration_ms, status, error, created_at, updated_at
		) SELECT
			user_id, session_id, message_id,
			CASE WHEN tool_id IS NULL OR tool_id = '' THEN tool_call_id ELSE tool_id END,
			COALESCE(tool_call_id, ''), COALESCE(user_message_id, ''),
			COALESCE(agent_type, ''), function_name, COALESCE(display_label, ''),
			arguments, COALESCE(response, ''), COALESCE(response_length, 0),
			COALESCE(duration_ms, 0), COALESCE(status, 'pending'), COALESCE(error, ''),
			created_at, updated_at
		FROM tool_calls`
	} else {
		copySQL = `INSERT INTO ` + tmp + ` (` + columns + `) SELECT ` + columns + ` FROM ` + table
	}
	if _, err := tx.Exec(copySQL); err != nil {
		return fmt.Errorf("copy %s: %w", table, err)
	}
	if _, err := tx.Exec(`DROP TABLE ` + table); err != nil {
		return fmt.Errorf("drop %s: %w", table, err)
	}
	if _, err := tx.Exec(`ALTER TABLE ` + tmp + ` RENAME TO ` + table); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}
