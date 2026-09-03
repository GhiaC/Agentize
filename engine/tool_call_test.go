package engine

import (
	"errors"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

// TestRealStoresImplementToolCallStore verifies that SQLiteStore and MongoDBStore
// implement the ToolCallStore interface required for tool call persistence.
func TestRealStoresImplementToolCallStore(t *testing.T) {
	// Test SQLiteStore
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create SQLite store: %v", err)
	}
	defer sqliteStore.Close()

	// Verify it can be used as model.SessionStore
	var sessionStore model.SessionStore = sqliteStore
	_ = sessionStore

	// Verify it implements ToolCallStore
	persister := NewToolCallPersister(sqliteStore, "Test")
	if persister == nil {
		t.Error("SQLiteStore should implement ToolCallStore but NewToolCallPersister returned nil")
	}
	if !persister.IsAvailable() {
		t.Error("SQLiteStore persister should be available")
	}
}

// TestToolCallPersister_IntegrationWithSQLite tests the full save/retrieve cycle.
func TestToolCallPersister_IntegrationWithSQLite(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create SQLite store: %v", err)
	}
	defer sqliteStore.Close()

	persister := NewToolCallPersister(sqliteStore, "IntegrationTest")
	if persister == nil {
		t.Fatal("NewToolCallPersister returned nil for SQLiteStore")
	}

	// Create a session first (required for GenerateToolID)
	session := &model.Session{
		SessionID: "test-integration-session",
		UserID:    "user-integration",
		AgentType: model.AgentTypeHigh,
		ToolSeq:   0,
	}

	toolCall := openai.ToolCall{
		ID: "call_integration_test_123",
		Function: openai.FunctionCall{
			Name:      "get_crypto_price_cmc",
			Arguments: `{"symbol":"BTC"}`,
		},
	}

	// Save tool call
	toolID := persister.Save(session, "msg-integration", toolCall)
	if toolID == "" {
		t.Fatal("Save returned empty toolID")
	}
	t.Logf("Saved tool call with ID: %s", toolID)

	// Update the response
	persister.Update(session, "msg-integration", toolID, `{"price": 50000, "currency": "USD"}`, nil)

	// Retrieve and verify
	savedToolCalls, err := sqliteStore.GetAllToolCalls()
	if err != nil {
		t.Fatalf("GetAllToolCalls failed: %v", err)
	}

	if len(savedToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(savedToolCalls))
	}

	saved := savedToolCalls[0]
	if saved.ToolID != toolID {
		t.Errorf("ToolID mismatch: got %s, want %s", saved.ToolID, toolID)
	}
	if saved.FunctionName != "get_crypto_price_cmc" {
		t.Errorf("FunctionName mismatch: got %s", saved.FunctionName)
	}
	if saved.Response != `{"price": 50000, "currency": "USD"}` {
		t.Errorf("Response mismatch: got %s", saved.Response)
	}
	t.Logf("Successfully retrieved tool call: %+v", saved)
}

// mockToolCallStore implements ToolCallStore for testing.
type mockToolCallStore struct {
	putCalls        []*model.ToolCall
	updateCalls     []updateCall
	putErr          error
	updateErr       error
	putCallCount    int
	updateCallCount int
}

type updateCall struct {
	toolID   string
	response string
	execErr  error
}

func (m *mockToolCallStore) PutToolCall(tc *model.ToolCall) error {
	m.putCallCount++
	if m.putErr != nil {
		return m.putErr
	}
	m.putCalls = append(m.putCalls, tc)
	return nil
}

func (m *mockToolCallStore) UpdateToolCallResponse(toolID, response string, execErr error) error {
	m.updateCallCount++
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updateCalls = append(m.updateCalls, updateCall{toolID: toolID, response: response, execErr: execErr})
	return nil
}

func (m *mockToolCallStore) UpdateMessageToolCallResponse(userID, sessionID, messageID, toolID, response string, execErr error) error {
	return m.UpdateToolCallResponse(toolID, response, execErr)
}

// mockSessionStore wraps mockToolCallStore to satisfy model.SessionStore interface.
type mockSessionStore struct {
	mockToolCallStore
}

// SessionStore interface methods (stubs)
func (m *mockSessionStore) Get(sessionID string) (*model.Session, error) { return nil, nil }
func (m *mockSessionStore) Put(session *model.Session) error             { return nil }
func (m *mockSessionStore) Delete(sessionID string) error                { return nil }
func (m *mockSessionStore) DeleteUserSession(userID, sessionID string) error {
	return nil
}
func (m *mockSessionStore) List(userID string) ([]*model.Session, error) { return nil, nil }
func (m *mockSessionStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	return 1, nil
}
func (m *mockSessionStore) GetUserSession(userID, sessionID string) (*model.Session, error) {
	return nil, nil
}

// nonToolCallSessionStore is a SessionStore that does NOT implement ToolCallStore.
type nonToolCallSessionStore struct{}

func (n *nonToolCallSessionStore) Get(sessionID string) (*model.Session, error) { return nil, nil }
func (n *nonToolCallSessionStore) Put(session *model.Session) error             { return nil }
func (n *nonToolCallSessionStore) Delete(sessionID string) error                { return nil }
func (n *nonToolCallSessionStore) DeleteUserSession(userID, sessionID string) error {
	return nil
}
func (n *nonToolCallSessionStore) List(userID string) ([]*model.Session, error) { return nil, nil }
func (n *nonToolCallSessionStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	return 1, nil
}
func (n *nonToolCallSessionStore) GetUserSession(userID, sessionID string) (*model.Session, error) {
	return nil, nil
}

func TestNewToolCallPersister_WithToolCallStore(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")
	if p == nil {
		t.Fatal("expected non-nil persister when store implements ToolCallStore")
	}
	if !p.IsAvailable() {
		t.Error("expected IsAvailable() to return true")
	}
}

func TestNewToolCallPersister_WithoutToolCallStore(t *testing.T) {
	store := &nonToolCallSessionStore{}
	p := NewToolCallPersister(store, "Test")
	if p != nil {
		t.Error("expected nil persister when store does not implement ToolCallStore")
	}
}

func TestToolCallPersister_Save(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")

	session := &model.Session{
		SessionID: "test-session-001",
		UserID:    "user123",
		AgentType: model.AgentTypeHigh,
		ToolSeq:   0,
	}

	toolCall := openai.ToolCall{
		ID: "call_abc123",
		Function: openai.FunctionCall{
			Name:      "get_weather",
			Arguments: `{"city":"Paris"}`,
		},
	}

	toolID := p.Save(session, "msg-001", toolCall)

	if toolID == "" {
		t.Fatal("expected non-empty toolID")
	}

	if len(store.putCalls) != 1 {
		t.Fatalf("expected 1 PutToolCall call, got %d", len(store.putCalls))
	}

	saved := store.putCalls[0]
	if saved.ToolID != toolID {
		t.Errorf("ToolID mismatch: got %s, want %s", saved.ToolID, toolID)
	}
	if saved.ToolCallID != "call_abc123" {
		t.Errorf("ToolCallID mismatch: got %s", saved.ToolCallID)
	}
	if saved.FunctionName != "get_weather" {
		t.Errorf("FunctionName mismatch: got %s", saved.FunctionName)
	}
	if saved.Arguments != `{"city":"Paris"}` {
		t.Errorf("Arguments mismatch: got %s", saved.Arguments)
	}
	if saved.SessionID != "test-session-001" {
		t.Errorf("SessionID mismatch: got %s", saved.SessionID)
	}
	if saved.UserID != "user123" {
		t.Errorf("UserID mismatch: got %s", saved.UserID)
	}
	if saved.AgentType != model.AgentTypeHigh {
		t.Errorf("AgentType mismatch: got %s", saved.AgentType)
	}
	if saved.Response != "" {
		t.Errorf("Response should be empty initially, got %s", saved.Response)
	}
}

func TestToolCallPersister_SaveWithAgentType(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")

	session := &model.Session{
		SessionID: "core-session-001",
		UserID:    "user456",
		AgentType: model.AgentTypeUser, // session's agent type
		ToolSeq:   5,
	}

	toolCall := openai.ToolCall{
		ID: "call_xyz789",
		Function: openai.FunctionCall{
			Name:      "delegate_task",
			Arguments: `{"agent":"high"}`,
		},
	}

	// Save with explicit AgentTypeCore (overriding session's AgentType)
	toolID := p.SaveWithAgentType(session, "msg-002", toolCall, model.AgentTypeCore, "Get weather")

	if toolID == "" {
		t.Fatal("expected non-empty toolID")
	}

	if len(store.putCalls) != 1 {
		t.Fatalf("expected 1 PutToolCall call, got %d", len(store.putCalls))
	}

	saved := store.putCalls[0]
	if saved.AgentType != model.AgentTypeCore {
		t.Errorf("AgentType should be Core, got %s", saved.AgentType)
	}
}

func TestToolCallPersister_Save_Error(t *testing.T) {
	store := &mockSessionStore{}
	store.putErr = errors.New("database error")
	p := NewToolCallPersister(store, "Test")

	session := &model.Session{SessionID: "test-session", UserID: "user1"}
	toolCall := openai.ToolCall{
		ID:       "call_1",
		Function: openai.FunctionCall{Name: "foo"},
	}

	toolID := p.Save(session, "msg-1", toolCall)

	if toolID != "" {
		t.Errorf("expected empty toolID on error, got %s", toolID)
	}
}

func TestToolCallPersister_Update(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")

	p.Update(nil, "", "tool-001", "result data", nil)

	if len(store.updateCalls) != 1 {
		t.Fatalf("expected 1 UpdateToolCallResponse call, got %d", len(store.updateCalls))
	}

	if store.updateCalls[0].toolID != "tool-001" {
		t.Errorf("toolID mismatch: got %s", store.updateCalls[0].toolID)
	}
	if store.updateCalls[0].response != "result data" {
		t.Errorf("response mismatch: got %s", store.updateCalls[0].response)
	}
}

func TestToolCallPersister_Update_EmptyToolID(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")

	// Should not call store when toolID is empty
	p.Update(nil, "", "", "some response", nil)

	if len(store.updateCalls) != 0 {
		t.Errorf("expected no UpdateToolCallResponse calls for empty toolID, got %d", len(store.updateCalls))
	}
}

func TestToolCallPersister_Update_Error(t *testing.T) {
	store := &mockSessionStore{}
	store.updateErr = errors.New("update failed")
	p := NewToolCallPersister(store, "Test")

	// Should not panic, just log the error
	p.Update(nil, "", "tool-001", "result", nil)

	if store.updateCallCount != 1 {
		t.Errorf("expected Update to be called, count=%d", store.updateCallCount)
	}
}

func TestToolCallPersister_NilPersister(t *testing.T) {
	var p *ToolCallPersister = nil

	// All methods should be safe to call on nil
	if p.IsAvailable() {
		t.Error("nil persister should not be available")
	}

	session := &model.Session{SessionID: "s1", UserID: "u1"}
	toolCall := openai.ToolCall{ID: "c1", Function: openai.FunctionCall{Name: "f1"}}

	toolID := p.Save(session, "m1", toolCall)
	if toolID != "" {
		t.Errorf("expected empty toolID from nil persister, got %s", toolID)
	}

	toolID = p.SaveWithAgentType(session, "m1", toolCall, model.AgentTypeCore, "")
	if toolID != "" {
		t.Errorf("expected empty toolID from nil persister, got %s", toolID)
	}

	// Should not panic
	p.Update(nil, "", "tool-1", "response", nil)
}

func TestToolCallPersister_SaveGeneratesSequentialToolIDs(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")

	session := &model.Session{
		SessionID: "seq-test-session",
		UserID:    "user1",
		ToolSeq:   0,
	}

	toolCall := openai.ToolCall{
		ID:       "call_1",
		Function: openai.FunctionCall{Name: "tool1"},
	}

	toolID1 := p.Save(session, "msg-1", toolCall)
	toolID2 := p.Save(session, "msg-1", toolCall)
	toolID3 := p.Save(session, "msg-2", toolCall)

	if toolID1 != "1" || toolID2 != "2" {
		t.Errorf("same-message tool ids = %s, %s, want 1, 2", toolID1, toolID2)
	}
	if toolID3 != "1" {
		t.Errorf("per-message tool id = %s, want 1", toolID3)
	}

	// Verify sequence incremented
	if session.ToolSeq != 3 {
		t.Errorf("expected ToolSeq=3, got %d", session.ToolSeq)
	}
}

func TestToolCallPersister_UpdateCompletesPerMessageDuplicateIDs(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create SQLite store: %v", err)
	}
	defer sqliteStore.Close()

	persister := NewToolCallPersister(sqliteStore, "PerMessage")
	session := &model.Session{SessionID: "sess", UserID: "user-1", AgentType: model.AgentTypeLow}
	if err := sqliteStore.Put(session); err != nil {
		t.Fatalf("Put session: %v", err)
	}

	first := persister.Save(session, "m1", openai.ToolCall{ID: "call_a", Function: openai.FunctionCall{Name: "search"}})
	second := persister.Save(session, "m2", openai.ToolCall{ID: "call_b", Function: openai.FunctionCall{Name: "search"}})
	if first != "1" || second != "1" {
		t.Fatalf("tool ids = %s, %s, want 1, 1", first, second)
	}

	persister.Update(session, "m1", first, "one", nil)
	persister.Update(session, "m2", second, "two", nil)

	items, err := sqliteStore.GetUserToolCallsBySession(session.UserID, session.SessionID)
	if err != nil {
		t.Fatalf("GetUserToolCallsBySession: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(items))
	}
	got := map[string]string{}
	for _, item := range items {
		if item.Status != model.ToolCallStatusSuccess {
			t.Fatalf("message %s status = %q, want success", item.MessageID, item.Status)
		}
		got[item.MessageID] = item.Response
	}
	if got["m1"] != "one" || got["m2"] != "two" {
		t.Fatalf("responses = %v", got)
	}
}

func TestToolCallPersister_SaveSetsTimestamps(t *testing.T) {
	store := &mockSessionStore{}
	p := NewToolCallPersister(store, "Test")

	session := &model.Session{SessionID: "ts-session", UserID: "u1"}
	toolCall := openai.ToolCall{
		ID:       "call_ts",
		Function: openai.FunctionCall{Name: "tool_ts"},
	}

	before := time.Now()
	p.Save(session, "msg-ts", toolCall)
	after := time.Now()

	saved := store.putCalls[0]
	if saved.CreatedAt.Before(before) || saved.CreatedAt.After(after) {
		t.Errorf("CreatedAt not in expected range: %v", saved.CreatedAt)
	}
	if saved.UpdatedAt.Before(before) || saved.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt not in expected range: %v", saved.UpdatedAt)
	}
}
