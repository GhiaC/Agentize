package store

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

func TestRewritePostgreSQLQueryPlaceholders(t *testing.T) {
	got := rewritePostgreSQLQuery(`SELECT data FROM sessions WHERE user_id = ? AND agent_type = ?`)
	want := `SELECT data FROM sessions WHERE user_id = $1 AND agent_type = $2`
	if got != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
}

func TestRewritePostgreSQLQuerySessionInsert(t *testing.T) {
	got := rewritePostgreSQLQuery(`INSERT OR REPLACE INTO sessions (session_id, user_id, agent_type, session_seq, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	for _, fragment := range []string{
		`ON CONFLICT (user_id, session_id)`, `data=EXCLUDED.data`, `$5::jsonb`,
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("session rewrite %q missing %q", got, fragment)
		}
	}
}

func TestRewritePostgreSQLQueryUpsert(t *testing.T) {
	got := rewritePostgreSQLQuery(`INSERT OR REPLACE INTO users (user_id, data, created_at, updated_at) VALUES (?, ?, ?, ?)`)
	for _, fragment := range []string{
		`INSERT INTO users`, `ON CONFLICT (user_id) DO UPDATE SET`, `data=EXCLUDED.data`, `VALUES ($1, $2::jsonb, $3, $4)`,
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("rewrite %q missing %q", got, fragment)
		}
	}
}

func TestRewritePostgreSQLQueryExistsAsInteger(t *testing.T) {
	got := rewritePostgreSQLQuery(`SELECT EXISTS(SELECT 1 FROM messages WHERE message_id = ?)`)
	if !strings.Contains(got, `SELECT CASE WHEN EXISTS(`) || !strings.Contains(got, `$1`) {
		t.Fatalf("unexpected EXISTS rewrite: %q", got)
	}
	if strings.Contains(got, "?") {
		t.Fatalf("placeholder survived EXISTS rewrite: %q", got)
	}
}

func TestRewritePostgreSQLQueryMessageInsert(t *testing.T) {
	got := rewritePostgreSQLQuery(messageInsertSQL)
	for _, fragment := range []string{
		`INSERT INTO messages`, `ON CONFLICT (user_id, session_id, message_id) DO UPDATE SET`, `VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("message insert rewrite %q missing %q", got, fragment)
		}
	}
}

func TestRewritePostgreSQLQueryConversationInsert(t *testing.T) {
	got := rewritePostgreSQLQuery(`INSERT OR REPLACE INTO conversations (
			conversation_id, user_id, session_id, conversation_seq, title, model, archived, data, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	for _, fragment := range []string{
		`ON CONFLICT (user_id, conversation_id)`, `$8::jsonb`, `data=EXCLUDED.data`,
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("conversation rewrite %q missing %q", got, fragment)
		}
	}
}

func TestRewritePostgreSQLQueryTaskScheduleOnConflict(t *testing.T) {
	got := rewritePostgreSQLQuery(`INSERT INTO task_schedules
			(schedule_id, user_id, session_id, status, next_run_at, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(schedule_id) DO UPDATE SET
			user_id=excluded.user_id,
			data=excluded.data,
			updated_at=excluded.updated_at`)
	if !strings.Contains(got, `$6::jsonb`) || !strings.Contains(got, `ON CONFLICT (user_id, schedule_id)`) {
		t.Fatalf("task schedule rewrite = %q", got)
	}
}

func TestRewritePostgreSQLQueryLeavesQuotedQuestionMarks(t *testing.T) {
	got := rewritePostgreSQLQuery(`SELECT '?' FROM users WHERE user_id = ?`)
	if got != `SELECT '?' FROM users WHERE user_id = $1` {
		t.Fatalf("quoted placeholder rewrite = %q", got)
	}
}

func TestPostgreSQLSchemaHasProductionIndexes(t *testing.T) {
	for _, fragment := range []string{
		`data JSONB NOT NULL`, `idx_messages_session_page`, `idx_tool_calls_session_created`,
		`idx_conversations_user_updated`, `idx_sessions_user_core`, `idx_tool_calls_user_message_id ON tool_calls(user_message_id)`,
	} {
		if !strings.Contains(postgreSQLSchema, fragment) {
			t.Fatalf("schema missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		`PRIMARY KEY (user_id, session_id)`, `PRIMARY KEY (user_id, conversation_id)`,
		`PRIMARY KEY (user_id, session_id, message_id)`, `idx_conversations_user_session`,
	} {
		if !strings.Contains(postgreSQLNumericIDKeys, fragment) {
			t.Fatalf("numeric-id migration missing %q", fragment)
		}
	}
	if postgreSQLMigrations[len(postgreSQLMigrations)-1].version != 3 {
		t.Fatalf("unexpected latest PostgreSQL migration version")
	}
}

func TestPostgreSQLRejectsUnsafeSchema(t *testing.T) {
	cfg := DefaultPostgreSQLStoreConfig()
	cfg.Schema = `agentize; DROP SCHEMA public`
	if _, err := NewPostgreSQLStore(cfg); err == nil || !strings.Contains(err.Error(), "invalid PostgreSQL schema") {
		t.Fatalf("expected schema validation error, got %v", err)
	}
}

func TestPostgreSQLRejectsNegativeMaxIdleConns(t *testing.T) {
	cfg := DefaultPostgreSQLStoreConfig()
	cfg.MaxIdleConns = -1
	if _, err := NewPostgreSQLStore(cfg); err == nil || !strings.Contains(err.Error(), "MaxIdleConns") {
		t.Fatalf("expected MaxIdleConns error, got %v", err)
	}
}

func TestOpenPostgreSQLRequiresAddrAndDatabase(t *testing.T) {
	if _, err := Open(Config{Backend: "postgres", PostgresDatabase: "crypto"}); err == nil {
		t.Fatal("expected missing PostgresAddr error")
	}
	if _, err := Open(Config{Backend: "postgres", PostgresAddr: "postgres.test:5432"}); err == nil {
		t.Fatal("expected missing PostgresDatabase error")
	}
}

func TestPostgreSQLDSNOmitsURLShapeAndEscapes(t *testing.T) {
	cfg := DefaultPostgreSQLStoreConfig()
	cfg.Addr = "db.example:5432"
	cfg.Database = "crypto"
	cfg.User = "app"
	cfg.Password = `p/aw s's`
	cfg.Schema = "agentize"
	dsn := postgreSQLDSN(cfg)
	if strings.Contains(dsn, "postgres://") || strings.Contains(dsn, "@db.example") {
		t.Fatal("DSN still looks like a URL")
	}
	if !strings.Contains(dsn, "host=db.example") || !strings.Contains(dsn, "dbname=crypto") {
		t.Fatal("DSN missing host/db")
	}
	if !strings.Contains(dsn, "options=-csearch_path=agentize") {
		t.Fatal("DSN missing search_path option")
	}
}

func TestPostgreSQLBackendInfoOmitsSecrets(t *testing.T) {
	st := &PostgreSQLStore{addr: "postgres.test:5432", database: "crypto", schema: "agentize"}
	info := st.BackendInfo()
	if info.Type != "PostgreSQL" {
		t.Fatalf("Type = %q", info.Type)
	}
	if !strings.Contains(info.Location, "postgres.test:5432/crypto") || !strings.Contains(info.Location, "schema: agentize") {
		t.Fatalf("Location = %q", info.Location)
	}
	if strings.Contains(strings.ToLower(info.Location), "password") || strings.Contains(info.Location, "@") {
		t.Fatalf("BackendInfo leaked credentials: %q", info.Location)
	}
}

func openPostgreSQLTestStore(t *testing.T) *PostgreSQLStore {
	t.Helper()
	cfg, ok := sharedPostgreSQLConfig()
	if !ok {
		t.Skip("PostgreSQL unavailable (set AGENTIZE_POSTGRES_CONFIG_FILE or AGENTIZE_POSTGRES_ADDR)")
	}
	cfg.Schema = fmt.Sprintf("agentize_unit_%s_%d", postgresRunID, atomic.AddInt64(&postgresSchemaN, 1))
	cfg.EphemeralSchema = true
	st, err := NewPostgreSQLStore(cfg)
	if err != nil {
		t.Fatalf("NewPostgreSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPostgreSQLStoreLiveSchemaAndRoundTrip(t *testing.T) {
	st := openPostgreSQLTestStore(t)
	version, err := st.SchemaVersion()
	if err != nil || version != 3 {
		t.Fatalf("SchemaVersion = %d (%v), want 3", version, err)
	}
	if stats := st.PoolStats(); stats.MaxOpenConnections <= 0 {
		t.Fatalf("PoolStats.MaxOpenConnections = %d", stats.MaxOpenConnections)
	}

	s := model.NewSessionWithType("pg-user-1", model.AgentTypeLow)
	s.Title = "postgres-roundtrip"
	s.Summary = model.SummaryEntries{"first fact", "second fact"}
	s.SummaryInitialized = true
	s.SystemPrompts = []model.SystemPromptEntry{{Key: "session_context", Title: "Session Context", Content: "current", Source: "test"}}
	s.SystemPromptsUpdatedAt = time.Now().Truncate(time.Second)
	if err := st.Put(s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := st.Get(s.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Title != "postgres-roundtrip" || got.UserID != "pg-user-1" || strings.Join(got.Summary, "|") != "first fact|second fact" || !got.SummaryInitialized || len(got.SystemPrompts) != 1 || got.SystemPrompts[0].Key != "session_context" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	user, err := st.GetOrCreateUser(s.UserID)
	if err != nil {
		t.Fatal(err)
	}
	user.ContextSummary = model.SummaryEntries{"prefers concise answers"}
	user.ContextTags = []string{"concise"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	gotUser, err := st.GetUser(s.UserID)
	if err != nil || gotUser == nil || len(gotUser.ContextSummary) != 1 || len(gotUser.ContextTags) != 1 {
		t.Fatalf("user-context round-trip: %v %+v", err, gotUser)
	}

	msg := model.NewUserMessage("1", 1, s.UserID, s.SessionID, "hello jsonb", model.ContentTypeWidget)
	msg.Metadata = model.NewScheduleMessageMeta(&model.TaskSchedule{
		ScheduleID: "sch-1", Name: "4h review", Status: model.TaskScheduleActive,
		LastRunStatus: model.TaskRunSucceeded, LastConclusion: "held the range",
	})
	if err := st.PutMessage(msg); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	msgs, err := st.GetMessagesBySession(s.SessionID)
	if err != nil || len(msgs) != 1 || msgs[0].Content != "hello jsonb" {
		t.Fatalf("messages = %#v err=%v", msgs, err)
	}
	if model.MessageKind(msgs[0]) != model.MessageMetaKindSchedule {
		t.Fatalf("message metadata kind = %q", model.MessageKind(msgs[0]))
	}

	conv := model.NewConversation(s.UserID, "1", s.SessionID, "plan", "m1", 1)
	if err := st.PutConversation(conv); err != nil {
		t.Fatalf("PutConversation: %v", err)
	}
	gotConv, err := st.GetUserConversation(s.UserID, "1")
	if err != nil || gotConv.Title != "plan" {
		t.Fatalf("GetUserConversation: %v %+v", err, gotConv)
	}

	info := st.BackendInfo()
	if info.Type != "PostgreSQL" || strings.Contains(info.Location, "password") {
		t.Fatalf("BackendInfo = %+v", info)
	}
}

func TestPostgreSQLStoreLiveNumericScopedIDs(t *testing.T) {
	st := openPostgreSQLTestStore(t)

	alice := model.NewSessionWithType("alice", model.AgentTypeLow)
	bob := model.NewSessionWithType("bob", model.AgentTypeLow)
	if alice.SessionID != "1" || bob.SessionID != "1" {
		t.Fatalf("numeric session ids = %q / %q, want 1 / 1", alice.SessionID, bob.SessionID)
	}
	if err := st.Put(alice); err != nil {
		t.Fatalf("Put alice: %v", err)
	}
	if err := st.Put(bob); err != nil {
		t.Fatalf("Put bob: %v", err)
	}

	gotAlice, err := st.GetUserSession("alice", "1")
	if err != nil || gotAlice.UserID != "alice" || gotAlice.SessionID != "1" {
		t.Fatalf("GetUserSession alice: %v %+v", err, gotAlice)
	}
	gotBob, err := st.GetUserSession("bob", "1")
	if err != nil || gotBob.UserID != "bob" {
		t.Fatalf("GetUserSession bob: %v %+v", err, gotBob)
	}
	if _, err := st.Get("1"); err == nil {
		t.Fatal("Get(1) must fail closed when two users share the numeric id")
	}

	aliceMsg := model.NewUserMessage("1", 1, "alice", "1", "alice-hello", model.ContentTypeText)
	bobMsg := model.NewUserMessage("1", 1, "bob", "1", "bob-hello", model.ContentTypeText)
	if err := st.PutMessage(aliceMsg); err != nil {
		t.Fatalf("PutMessage alice: %v", err)
	}
	if err := st.PutMessage(bobMsg); err != nil {
		t.Fatalf("PutMessage bob: %v", err)
	}
	aliceMsgs, err := st.GetUserMessagesBySessionPage("alice", "1", 10, 0)
	if err != nil || len(aliceMsgs) != 1 || aliceMsgs[0].Content != "alice-hello" {
		t.Fatalf("alice messages = %#v err=%v", aliceMsgs, err)
	}
	bobMsgs, err := st.GetUserMessagesBySessionPage("bob", "1", 10, 0)
	if err != nil || len(bobMsgs) != 1 || bobMsgs[0].Content != "bob-hello" {
		t.Fatalf("bob messages = %#v err=%v", bobMsgs, err)
	}
	if _, err := st.GetMessagesBySession("1"); err == nil {
		t.Fatal("GetMessagesBySession(1) must fail closed when two users share the numeric session id")
	}

	aliceConv := model.NewConversation("alice", "1", alice.SessionID, "alice-plan", "m1", 1)
	bobConv := model.NewConversation("bob", "1", bob.SessionID, "bob-plan", "m1", 1)
	if err := st.PutConversation(aliceConv); err != nil {
		t.Fatalf("PutConversation alice: %v", err)
	}
	if err := st.PutConversation(bobConv); err != nil {
		t.Fatalf("PutConversation bob: %v", err)
	}
	gotConv, err := st.GetUserConversation("alice", "1")
	if err != nil || gotConv.Title != "alice-plan" {
		t.Fatalf("GetUserConversation alice: %v %+v", err, gotConv)
	}
	if _, err := st.GetConversation("1"); err == nil {
		t.Fatal("GetConversation(1) must fail closed when two users share the numeric id")
	}
}

func TestPostgreSQLStoreLiveCoreUniqueness(t *testing.T) {
	st := openPostgreSQLTestStore(t)
	first := model.NewSessionWithType("pg-core-user", model.AgentTypeCore)
	second := model.NewSessionWithType("pg-core-user", model.AgentTypeCore)
	if err := st.PutCoreSession(first); err != nil {
		t.Fatalf("PutCoreSession first: %v", err)
	}
	if err := st.PutCoreSession(second); err != nil {
		t.Fatalf("PutCoreSession second: %v", err)
	}
	got, err := st.GetCoreSession("pg-core-user")
	if err != nil || got == nil || got.SessionID != second.SessionID {
		t.Fatalf("GetCoreSession = %+v err=%v, want %s", got, err, second.SessionID)
	}
	list, err := st.List("pg-core-user")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	cores := 0
	for _, session := range list {
		if session.AgentType == model.AgentTypeCore {
			cores++
		}
	}
	if cores != 1 {
		t.Fatalf("core sessions = %d, want 1", cores)
	}
}

func TestPostgreSQLStoreLiveEphemeralIsolation(t *testing.T) {
	a := openPostgreSQLTestStore(t)
	b := openPostgreSQLTestStore(t)
	if a.schema == b.schema {
		t.Fatal("ephemeral stores shared a schema")
	}
	s := model.NewSessionWithType("pg-iso", model.AgentTypeLow)
	if err := a.Put(s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := b.Get(s.SessionID); err == nil {
		t.Fatal("store B saw store A's session")
	}
}

func TestPostgreSQLStoreLiveReopenIdempotent(t *testing.T) {
	cfg, ok := sharedPostgreSQLConfig()
	if !ok {
		t.Skip("PostgreSQL unavailable (set AGENTIZE_POSTGRES_CONFIG_FILE or AGENTIZE_POSTGRES_ADDR)")
	}
	s := model.NewSessionWithType("pg-reopen", model.AgentTypeLow)
	s.Title = "kept"

	ephemeral := cfg
	ephemeral.Schema = fmt.Sprintf("agentize_reopen_%s_%d", postgresRunID, atomic.AddInt64(&postgresSchemaN, 1))
	ephemeral.EphemeralSchema = true
	st, err := NewPostgreSQLStore(ephemeral)
	if err != nil {
		t.Fatalf("ephemeral open: %v", err)
	}
	if err := st.Put(s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close ephemeral store: %v", err)
	}
	again, err := NewPostgreSQLStore(ephemeral)
	if err != nil {
		t.Fatalf("recreate after drop: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if _, err := again.Get(s.SessionID); err == nil {
		t.Fatal("ephemeral drop did not remove session")
	}

	durable := cfg
	durable.Schema = fmt.Sprintf("agentize_keep_%s_%d", postgresRunID, atomic.AddInt64(&postgresSchemaN, 1))
	durable.EphemeralSchema = false
	kept, err := NewPostgreSQLStore(durable)
	if err != nil {
		t.Fatalf("durable open: %v", err)
	}
	if err := kept.Put(s); err != nil {
		_ = kept.Close()
		t.Fatalf("Put durable: %v", err)
	}
	if err := kept.Close(); err != nil {
		t.Fatalf("close durable: %v", err)
	}
	reopened, err := NewPostgreSQLStore(durable)
	if err != nil {
		t.Fatalf("reopen durable: %v", err)
	}
	t.Cleanup(func() {
		reopened.ownedEphemeralSchema = true
		_ = reopened.Close()
	})
	got, err := reopened.Get(s.SessionID)
	if err != nil || got.Title != "kept" {
		t.Fatalf("durable reopen lost session: %v %+v", err, got)
	}
	version, err := reopened.SchemaVersion()
	if err != nil || version != 3 {
		t.Fatalf("schema version after reopen = %d (%v)", version, err)
	}
}

func TestPostgreSQLStoreLiveOpenFactory(t *testing.T) {
	cfg, ok := sharedPostgreSQLConfig()
	if !ok {
		t.Skip("PostgreSQL unavailable (set AGENTIZE_POSTGRES_CONFIG_FILE or AGENTIZE_POSTGRES_ADDR)")
	}
	schema := fmt.Sprintf("agentize_open_%s_%d", postgresRunID, atomic.AddInt64(&postgresSchemaN, 1))
	st, err := Open(Config{
		Backend:                "postgres",
		PostgresAddr:           cfg.Addr,
		PostgresDatabase:       cfg.Database,
		PostgresUser:           cfg.User,
		PostgresPassword:       cfg.Password,
		PostgresSSLMode:        cfg.SSLMode,
		PostgresSchema:         schema,
		PostgresConnectTimeout: 5 * time.Second,
		PostgresMaxOpenConns:   4,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pg, ok := st.(*PostgreSQLStore)
	if !ok {
		t.Fatalf("Open type %T, want *PostgreSQLStore", st)
	}
	pg.ownedEphemeralSchema = true
	t.Cleanup(func() { _ = st.Close() })
	info := st.BackendInfo()
	if info.Type != "PostgreSQL" {
		t.Fatalf("BackendInfo.Type = %q", info.Type)
	}
}
