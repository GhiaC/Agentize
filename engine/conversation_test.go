package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func newConversationTestEngine(t *testing.T) *Engine {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return &Engine{Sessions: sqliteStore, Functions: model.NewFunctionRegistry()}
}

func TestCreateConversation_IDFormat(t *testing.T) {
	eng := newConversationTestEngine(t)
	conv, err := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "BTC plan", Model: "provider/fast"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.ConversationID != "alice-c0001" {
		t.Fatalf("ConversationID = %q, want alice-c0001", conv.ConversationID)
	}
	if strings.Contains(conv.ConversationID, "btc") || strings.Contains(conv.ConversationID, "plan") {
		t.Fatalf("id must not contain title slug: %s", conv.ConversationID)
	}
	if conv.Title != "BTC plan" || conv.Model != "provider/fast" {
		t.Fatalf("title/model = %q %q", conv.Title, conv.Model)
	}
	session, err := eng.Sessions.Get(conv.SessionID)
	if err != nil {
		t.Fatalf("linked session: %v", err)
	}
	if session.AgentType != model.AgentTypeConversation {
		t.Fatalf("session type = %s", session.AgentType)
	}
	if session.Title != "BTC plan" || session.Model != "provider/fast" {
		t.Fatalf("session title/model = %q %q", session.Title, session.Model)
	}
	if session.IsSubAgent() {
		t.Fatal("main session must not be a sub-agent")
	}
}

func TestCreateConversation_Sequence(t *testing.T) {
	eng := newConversationTestEngine(t)
	a, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "one"})
	b, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "two"})
	if a.ConversationID != "alice-c0001" || b.ConversationID != "alice-c0002" {
		t.Fatalf("ids = %s %s", a.ConversationID, b.ConversationID)
	}
	other, _ := eng.CreateConversation(CreateConversationInput{UserID: "bob", Title: "one"})
	if other.ConversationID != "bob-c0001" {
		t.Fatalf("other user id = %s", other.ConversationID)
	}
}

func TestListConversations_LastUsedFirst(t *testing.T) {
	eng := newConversationTestEngine(t)
	first, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "old"})
	second, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "new"})
	if err := eng.Sessions.TouchConversationBySession(first.SessionID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	list, err := eng.ListConversations("alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].ConversationID != first.ConversationID {
		t.Fatalf("expected last-touched first, got %s then %s (second=%s)", list[0].ConversationID, list[1].ConversationID, second.ConversationID)
	}
}

func TestRenameConversation_DoesNotChangeIDOrModel(t *testing.T) {
	eng := newConversationTestEngine(t)
	conv, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "old", Model: "m1"})
	if err := eng.RenameConversation("alice", conv.ConversationID, "new name"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := eng.GetConversation("alice", conv.ConversationID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ConversationID != conv.ConversationID {
		t.Fatal("id changed")
	}
	if got.Title != "new name" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Model != "m1" {
		t.Fatalf("model changed to %q", got.Model)
	}
	if _, err := eng.GetConversation("bob", conv.ConversationID); err == nil {
		t.Fatal("cross-user get must fail")
	}
}

func TestSetConversationModel_DoesNotChangeTitle(t *testing.T) {
	eng := newConversationTestEngine(t)
	conv, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "keep", Model: "m1"})
	if err := eng.SetConversationModel("alice", conv.ConversationID, "m2"); err != nil {
		t.Fatalf("set model: %v", err)
	}
	got, _ := eng.GetConversation("alice", conv.ConversationID)
	if got.Title != "keep" || got.Model != "m2" {
		t.Fatalf("got %+v", got)
	}
	session, _ := eng.Sessions.Get(got.SessionID)
	if session.Model != "m2" {
		t.Fatalf("session model = %q", session.Model)
	}
}

func TestCreateSubAgent_OnlyMainSession(t *testing.T) {
	eng := newConversationTestEngine(t)
	conv, err := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "work", Model: "m1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	child, err := eng.CreateSubAgent(conv.SessionID, "research", "m-sub")
	if err != nil {
		t.Fatalf("sub-agent: %v", err)
	}
	if child.AgentType != model.AgentTypeSub || child.ParentSessionID != conv.SessionID {
		t.Fatalf("child = %+v", child)
	}
	if child.CanCreateSubAgent() {
		t.Fatal("child must not create sub-agents")
	}
	_, err = eng.CreateSubAgent(child.SessionID, "nested", "")
	if !errors.Is(err, ErrSubAgentNesting) {
		t.Fatalf("nested create error = %v", err)
	}
	userSess, err := eng.createTypedSession("alice", model.AgentTypeUser, "legacy", "", "")
	if err != nil {
		t.Fatalf("user session: %v", err)
	}
	_, err = eng.CreateSubAgent(userSess.SessionID, "nope", "")
	if !errors.Is(err, ErrNotConversationSession) {
		t.Fatalf("low parent error = %v", err)
	}
}

func TestDeleteConversation_RemovesSubAgents(t *testing.T) {
	eng := newConversationTestEngine(t)
	conv, _ := eng.CreateConversation(CreateConversationInput{UserID: "alice", Title: "work"})
	child, _ := eng.CreateSubAgent(conv.SessionID, "research", "")
	if err := eng.DeleteConversation("alice", conv.ConversationID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := eng.Sessions.GetConversation(conv.ConversationID); err == nil {
		t.Fatal("conversation still present")
	}
	if _, err := eng.Sessions.Get(conv.SessionID); err == nil {
		t.Fatal("main session still present")
	}
	if _, err := eng.Sessions.Get(child.SessionID); err == nil {
		t.Fatal("sub-agent session still present")
	}
}
