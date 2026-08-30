package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/lib/pq"
)

const defaultPostgreSQLSchema = "agentize"

var postgreSQLIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// PostgreSQLStoreConfig configures the production PostgreSQL backend. The
// schema is intentionally separate from the host application's public tables.
type PostgreSQLStoreConfig struct {
	Addr            string
	Database        string
	User            string
	Password        string
	SSLMode         string
	Schema          string
	ConnectTimeout  time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	// EphemeralSchema is for isolated integration tests only. Construction fails
	// if Schema exists; Close drops only the schema this instance created.
	EphemeralSchema bool
}

func DefaultPostgreSQLStoreConfig() PostgreSQLStoreConfig {
	return PostgreSQLStoreConfig{
		Addr:            "localhost:5432",
		Database:        "agentize",
		SSLMode:         "disable",
		Schema:          defaultPostgreSQLSchema,
		ConnectTimeout:  10 * time.Second,
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

// PostgreSQLStore reuses the relational Store implementation while its driver
// translates the deliberately small SQLite-compatible query surface to native
// PostgreSQL placeholders, JSONB casts, and ON CONFLICT upserts. PostgreSQL
// owns a separate schema with JSONB columns and production indexes.
type PostgreSQLStore struct {
	*SQLiteStore
	addr                 string
	database             string
	schema               string
	ownedEphemeralSchema bool
}

func (s *PostgreSQLStore) Close() error {
	if s.ownedEphemeralSchema {
		if _, err := s.db.Exec(`DROP SCHEMA IF EXISTS ` + pq.QuoteIdentifier(s.schema) + ` CASCADE`); err != nil {
			_ = s.db.Close()
			return fmt.Errorf("store: PostgreSQL drop test schema: %w", err)
		}
	}
	return s.db.Close()
}

func NewPostgreSQLStore(cfg PostgreSQLStoreConfig) (*PostgreSQLStore, error) {
	defaults := DefaultPostgreSQLStoreConfig()
	if strings.TrimSpace(cfg.Addr) == "" {
		cfg.Addr = defaults.Addr
	}
	if strings.TrimSpace(cfg.Database) == "" {
		cfg.Database = defaults.Database
	}
	if strings.TrimSpace(cfg.SSLMode) == "" {
		cfg.SSLMode = defaults.SSLMode
	}
	if strings.TrimSpace(cfg.Schema) == "" {
		cfg.Schema = defaults.Schema
	}
	if !postgreSQLIdentifier.MatchString(cfg.Schema) {
		return nil, fmt.Errorf("store: invalid PostgreSQL schema %q", cfg.Schema)
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaults.ConnectTimeout
	}
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = defaults.MaxOpenConns
	}
	if cfg.MaxIdleConns < 0 {
		return nil, fmt.Errorf("store: PostgreSQL MaxIdleConns cannot be negative")
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = defaults.MaxIdleConns
	}
	if cfg.MaxIdleConns > cfg.MaxOpenConns {
		cfg.MaxIdleConns = cfg.MaxOpenConns
	}
	if cfg.ConnMaxLifetime <= 0 {
		cfg.ConnMaxLifetime = defaults.ConnMaxLifetime
	}

	dsn := postgreSQLDSN(cfg)
	base, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: PostgreSQL connector: %w", err)
	}
	db := sql.OpenDB(&postgreSQLRewriteConnector{base: base})
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: PostgreSQL ping %s/%s: %w", cfg.Addr, cfg.Database, err)
	}
	if cfg.EphemeralSchema {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, cfg.Schema).Scan(&exists); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: PostgreSQL inspect ephemeral schema: %w", err)
		}
		if exists {
			_ = db.Close()
			return nil, fmt.Errorf("store: PostgreSQL ephemeral schema %q already exists", cfg.Schema)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+pq.QuoteIdentifier(cfg.Schema)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: PostgreSQL create schema: %w", err)
	}
	// search_path in the DSN is ignored until the schema exists; pin it on this
	// connection before CREATE TABLE so objects never land in public.
	if _, err := db.ExecContext(ctx, `SET search_path TO `+pq.QuoteIdentifier(cfg.Schema)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: PostgreSQL search_path: %w", err)
	}
	if err := initPostgreSQLSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: PostgreSQL initialize schema: %w", err)
	}

	baseStore := &SQLiteStore{db: db, path: "postgresql"}
	return &PostgreSQLStore{
		SQLiteStore: baseStore, addr: cfg.Addr, database: cfg.Database, schema: cfg.Schema,
		ownedEphemeralSchema: cfg.EphemeralSchema,
	}, nil
}

func (s *PostgreSQLStore) BackendInfo() debuger.BackendInfo {
	location := s.addr
	if s.database != "" {
		location += "/" + s.database
	}
	if s.schema != "" {
		location += " (schema: " + s.schema + ")"
	}
	return debuger.BackendInfo{Type: "PostgreSQL", Location: location}
}

// Backup is intentionally delegated to pg_dump at the deployment layer.
func (s *PostgreSQLStore) Backup(io.Writer) error { return ErrBackupUnsupported }

func postgreSQLDSN(cfg PostgreSQLStoreConfig) string {
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		host = cfg.Addr
		port = "5432"
	}
	parts := []string{
		"host=" + pqEscapeDSN(host),
		"port=" + pqEscapeDSN(port),
		"dbname=" + pqEscapeDSN(cfg.Database),
		"sslmode=" + pqEscapeDSN(cfg.SSLMode),
		fmt.Sprintf("connect_timeout=%d", max(1, int(cfg.ConnectTimeout.Seconds()))),
		"options=" + pqEscapeDSN("-csearch_path="+cfg.Schema),
	}
	if cfg.User != "" {
		parts = append(parts, "user="+pqEscapeDSN(cfg.User))
	}
	if cfg.Password != "" {
		parts = append(parts, "password="+pqEscapeDSN(cfg.Password))
	}
	return strings.Join(parts, " ")
}

func pqEscapeDSN(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, " '\\") {
		return v
	}
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "'", "\\'")
	return "'" + v + "'"
}

func initPostgreSQLSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		applied_at BIGINT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	var current sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for _, m := range postgreSQLMigrations {
		if current.Valid && int64(m.version) <= current.Int64 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migration %d (%s): begin: %w", m.version, m.desc, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", m.version, m.desc, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_version (version, description, applied_at) VALUES (?, ?, ?)`,
			m.version, m.desc, time.Now().Unix(),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s): record: %w", m.version, m.desc, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d (%s): commit: %w", m.version, m.desc, err)
		}
	}
	return nil
}

// PostgreSQL's database/sql driver uses $N placeholders. Keeping translation
// at the connector boundary lets the relational implementation and its full
// conformance contract remain literally identical for SQLite and PostgreSQL.
type postgreSQLRewriteConnector struct{ base driver.Connector }

func (c *postgreSQLRewriteConnector) Driver() driver.Driver { return c.base.Driver() }
func (c *postgreSQLRewriteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &postgreSQLRewriteConn{Conn: conn}, nil
}

type postgreSQLRewriteConn struct{ driver.Conn }

func (c *postgreSQLRewriteConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(rewritePostgreSQLQuery(query))
}
func (c *postgreSQLRewriteConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, rewritePostgreSQLQuery(query))
	}
	return c.Prepare(query)
}
func (c *postgreSQLRewriteConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, rewritePostgreSQLQuery(query), args)
	}
	return nil, driver.ErrSkip
}
func (c *postgreSQLRewriteConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, rewritePostgreSQLQuery(query), args)
	}
	return nil, driver.ErrSkip
}
func (c *postgreSQLRewriteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bc, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bc.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}
func (c *postgreSQLRewriteConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}
func (c *postgreSQLRewriteConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}
func (c *postgreSQLRewriteConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

var insertOrReplaceRE = regexp.MustCompile(`(?is)^\s*INSERT\s+OR\s+REPLACE\s+INTO\s+([a-z_][a-z0-9_]*)\s*\((.*?)\)\s*VALUES\s*\((.*?)\)\s*$`)

var insertValuesRE = regexp.MustCompile(`(?is)INSERT\s+INTO\s+([a-z_][a-z0-9_]*)\s*\(([^)]+)\)\s*VALUES\s*\(([^)]*)\)`)

var postgreSQLPrimaryKeys = map[string]string{
	"sessions": "session_id", "users": "user_id", "messages": "message_id",
	"opened_files": "file_id", "user_files": "file_id", "tool_calls": "tool_call_id",
	"summarization_logs": "log_id", "route_traces": "trace_id", "reviews": "request_id",
	"conversations": "conversation_id", "task_schedules": "schedule_id",
	"task_schedule_runs": "run_id", "workflow_runs": "workflow_id",
}

var postgreSQLJSONBColumns = map[string]map[string]struct{}{
	"sessions":           {"data": {}},
	"users":              {"data": {}},
	"route_traces":       {"data": {}},
	"reviews":            {"data": {}},
	"task_schedules":     {"data": {}},
	"task_schedule_runs": {"data": {}},
	"workflow_runs":      {"data": {}},
	"conversations":      {"data": {}},
}

func rewritePostgreSQLQuery(query string) string {
	query = rewritePostgreSQLInsertOrReplace(query)
	query = rewritePostgreSQLExists(query)
	query = rewritePostgreSQLPlaceholders(query)
	return rewritePostgreSQLJSONBCasts(query)
}

func rewritePostgreSQLInsertOrReplace(query string) string {
	m := insertOrReplaceRE.FindStringSubmatch(query)
	if m == nil {
		return query
	}
	table := strings.ToLower(m[1])
	pk := postgreSQLPrimaryKeys[table]
	if pk == "" {
		return query
	}
	columns := strings.Split(m[2], ",")
	updates := make([]string, 0, len(columns)-1)
	for _, raw := range columns {
		col := strings.TrimSpace(raw)
		if col != "" && col != pk {
			updates = append(updates, col+"=EXCLUDED."+col)
		}
	}
	return "INSERT INTO " + table + " (" + m[2] + ") VALUES (" + m[3] + ") ON CONFLICT (" + pk + ") DO UPDATE SET " + strings.Join(updates, ", ")
}

func rewritePostgreSQLExists(query string) string {
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(strings.ToUpper(trimmed), "SELECT EXISTS(") {
		return "SELECT CASE WHEN " + strings.TrimPrefix(trimmed, "SELECT ") + " THEN 1 ELSE 0 END"
	}
	return query
}

func rewritePostgreSQLPlaceholders(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 16)
	arg := 1
	inSingle := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			inSingle = !inSingle
			b.WriteByte(ch)
			continue
		}
		if ch == '?' && !inSingle {
			fmt.Fprintf(&b, "$%d", arg)
			arg++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func rewritePostgreSQLJSONBCasts(query string) string {
	return insertValuesRE.ReplaceAllStringFunc(query, func(match string) string {
		m := insertValuesRE.FindStringSubmatch(match)
		if m == nil {
			return match
		}
		want := postgreSQLJSONBColumns[strings.ToLower(m[1])]
		if len(want) == 0 {
			return match
		}
		cols := strings.Split(m[2], ",")
		vals := strings.Split(m[3], ",")
		if len(cols) != len(vals) {
			return match
		}
		changed := false
		for i := range vals {
			vals[i] = strings.TrimSpace(vals[i])
			col := strings.ToLower(strings.TrimSpace(cols[i]))
			if _, ok := want[col]; !ok {
				continue
			}
			if strings.Contains(strings.ToLower(vals[i]), "::jsonb") {
				continue
			}
			vals[i] += "::jsonb"
			changed = true
		}
		if !changed {
			return match
		}
		return "INSERT INTO " + strings.ToLower(m[1]) + " (" + m[2] + ") VALUES (" + strings.Join(vals, ", ") + ")"
	})
}

type postgreSQLMigration struct {
	version int
	desc    string
	sql     string
}

var postgreSQLMigrations = []postgreSQLMigration{{
	1, "initial agentize schema", postgreSQLSchema,
}}

// Foreign keys are intentionally omitted on session/user children: Delete of a
// session does not cascade, and Verify must report the same orphans as SQLite
// and MongoDB. Core-session uniqueness is enforced by a partial unique index.
const postgreSQLSchema = `
CREATE TABLE IF NOT EXISTS sessions (
 session_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, agent_type TEXT NOT NULL,
 session_seq BIGINT NOT NULL DEFAULT 0, data JSONB NOT NULL,
 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_sessions_user_updated ON sessions(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_user_agent_seq ON sessions(user_id, agent_type, session_seq DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_user_core ON sessions(user_id) WHERE agent_type='core';

CREATE TABLE IF NOT EXISTS users (
 user_id TEXT PRIMARY KEY, data JSONB NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);

CREATE TABLE IF NOT EXISTS messages (
 message_id TEXT PRIMARY KEY, seq_id BIGINT DEFAULT 0, user_id TEXT NOT NULL, session_id TEXT NOT NULL,
 role TEXT NOT NULL, content TEXT NOT NULL, model TEXT, agent_type TEXT DEFAULT '', content_type TEXT DEFAULT '',
 prompt_tokens BIGINT DEFAULT 0, completion_tokens BIGINT DEFAULT 0, total_tokens BIGINT DEFAULT 0,
 request_model TEXT, max_tokens BIGINT, temperature DOUBLE PRECISION, has_tool_calls BIGINT DEFAULT 0,
 finish_reason TEXT, is_nonsense BIGINT DEFAULT 0, created_at BIGINT NOT NULL, metadata TEXT DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_messages_session_page ON messages(session_id, created_at DESC, seq_id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_user_created ON messages(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS opened_files (
 file_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, user_id TEXT NOT NULL, file_path TEXT NOT NULL,
 file_name TEXT, opened_at BIGINT NOT NULL, closed_at BIGINT, is_open BIGINT DEFAULT 1);
CREATE INDEX IF NOT EXISTS idx_opened_files_session_opened ON opened_files(session_id, opened_at ASC);
CREATE INDEX IF NOT EXISTS idx_opened_files_session_open ON opened_files(session_id, is_open, opened_at ASC);
CREATE INDEX IF NOT EXISTS idx_opened_files_user_opened ON opened_files(user_id, opened_at DESC);

CREATE TABLE IF NOT EXISTS user_files (
 file_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, session_id TEXT NOT NULL, name TEXT, mime_type TEXT,
 size BIGINT DEFAULT 0, storage_key TEXT NOT NULL, source TEXT DEFAULT 'uploaded', parent_file_id TEXT DEFAULT '',
 summary TEXT, created_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_user_files_user_created ON user_files(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_files_session_created ON user_files(session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS tool_calls (
 tool_call_id TEXT PRIMARY KEY, tool_id TEXT DEFAULT '', message_id TEXT NOT NULL, user_message_id TEXT DEFAULT '',
 session_id TEXT NOT NULL, user_id TEXT NOT NULL, agent_type TEXT DEFAULT '', function_name TEXT NOT NULL,
 display_label TEXT DEFAULT '', arguments TEXT NOT NULL, response TEXT DEFAULT '', response_length BIGINT DEFAULT 0,
 duration_ms BIGINT DEFAULT 0, status TEXT DEFAULT 'pending', error TEXT DEFAULT '',
 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_tool_calls_session_created ON tool_calls(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_calls_user_created ON tool_calls(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tool_calls_tool_id ON tool_calls(tool_id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_user_message_id ON tool_calls(user_message_id);

CREATE TABLE IF NOT EXISTS summarization_logs (
 log_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, user_id TEXT NOT NULL, session_title TEXT,
 previous_summary TEXT, previous_tags TEXT, messages_before_count BIGINT DEFAULT 0, messages_after_count BIGINT DEFAULT 0,
 archived_messages_count BIGINT DEFAULT 0, prompt_sent TEXT NOT NULL, response_received TEXT, model_used TEXT NOT NULL,
 requested_model TEXT, generated_summary TEXT, generated_tags TEXT, generated_title TEXT,
 prompt_tokens BIGINT DEFAULT 0, completion_tokens BIGINT DEFAULT 0, total_tokens BIGINT DEFAULT 0,
 duration_ms BIGINT DEFAULT 0, status TEXT NOT NULL, error_message TEXT, summarization_type TEXT,
 created_at BIGINT NOT NULL, completed_at BIGINT);
CREATE INDEX IF NOT EXISTS idx_summaries_session_created ON summarization_logs(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_summaries_user_created ON summarization_logs(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS route_traces (
 trace_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, user_id TEXT NOT NULL, status TEXT DEFAULT '',
 data JSONB NOT NULL, created_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_routes_session_created ON route_traces(session_id, created_at DESC, trace_id DESC);
CREATE INDEX IF NOT EXISTS idx_routes_user_created ON route_traces(user_id, created_at DESC, trace_id DESC);

CREATE TABLE IF NOT EXISTS reviews (
 request_id TEXT PRIMARY KEY, session_id TEXT DEFAULT '', user_id TEXT DEFAULT '', status TEXT NOT NULL DEFAULT 'pending',
 data JSONB NOT NULL, created_at BIGINT NOT NULL, decided_at BIGINT DEFAULT 0);
CREATE INDEX IF NOT EXISTS idx_reviews_user_status_created ON reviews(user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reviews_status_created ON reviews(status, created_at DESC);

CREATE TABLE IF NOT EXISTS task_schedules (
 schedule_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, session_id TEXT NOT NULL, status TEXT NOT NULL,
 next_run_at BIGINT NOT NULL, data JSONB NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_schedules_user_created ON task_schedules(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_schedules_status_next ON task_schedules(status, next_run_at);

CREATE TABLE IF NOT EXISTS task_schedule_runs (
 run_id TEXT PRIMARY KEY, schedule_id TEXT NOT NULL, status TEXT NOT NULL, data JSONB NOT NULL,
 started_at BIGINT NOT NULL, completed_at BIGINT DEFAULT 0);
CREATE INDEX IF NOT EXISTS idx_schedule_runs_schedule_started ON task_schedule_runs(schedule_id, started_at DESC, run_id DESC);

CREATE TABLE IF NOT EXISTS workflow_runs (
 workflow_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, session_id TEXT NOT NULL, status TEXT NOT NULL,
 data JSONB NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_workflows_user_created ON workflow_runs(user_id, created_at DESC, workflow_id DESC);
CREATE INDEX IF NOT EXISTS idx_workflows_session_created ON workflow_runs(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflows_status_updated ON workflow_runs(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS conversations (
 conversation_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, session_id TEXT NOT NULL UNIQUE,
 conversation_seq BIGINT NOT NULL DEFAULT 0, title TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
 archived BIGINT NOT NULL DEFAULT 0, data JSONB NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_conversations_user_updated ON conversations(user_id, updated_at DESC);
`
