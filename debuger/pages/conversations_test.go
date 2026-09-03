package pages

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func TestRenderConversations(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	handler, err := debuger.NewDebugHandler(st)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	html, err := RenderConversations(handler, 1)
	if err != nil {
		t.Fatalf("empty render: %v", err)
	}
	if !strings.Contains(html, "No conversations found") {
		t.Fatalf("expected empty state, got %s", html)
	}

	session := model.NewSessionWithID("alice", "alice-conv-s0001", model.AgentTypeConversation)
	if err := st.Put(session); err != nil {
		t.Fatalf("put session: %v", err)
	}
	conv := model.NewConversation("alice", "alice-c0001", session.SessionID, "BTC plan", "m1", 1)
	if err := st.PutConversation(conv); err != nil {
		t.Fatalf("put conv: %v", err)
	}

	html, err = RenderConversations(handler, 1)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(html, "BTC plan") {
		t.Fatalf("missing conversation title: %s", html)
	}
	if !strings.Contains(html, ">1</code>") {
		t.Fatalf("visible ids should be numeric, got %s", html)
	}
	if strings.Contains(html, ">alice-c0001</code>") || strings.Contains(html, ">alice-conv-s0001</code>") {
		t.Fatal("legacy concat must not be the visible id")
	}
	if !strings.Contains(html, "/sessions/alice-conv-s0001") {
		t.Fatal("href must keep the stored session id")
	}
	if strings.Contains(html, "alice-c-btc") {
		t.Fatal("page must not invent slug ids")
	}
}
