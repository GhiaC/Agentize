package core

import (
	"context"
	"strings"
	"testing"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func TestConversationTools_ListCreateSelectRename(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	eng := &engine.Engine{Sessions: sqliteStore, Functions: model.NewFunctionRegistry()}
	ch.SetConversationEngine(eng)

	created, err := ch.createConversationTool(context.Background(), "alice", map[string]interface{}{
		"title": "Market",
		"model": "m1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(created, "alice-c0001") {
		t.Fatalf("create result = %s", created)
	}
	list, err := ch.listConversationsTool("alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, "alice-c0001") || !strings.Contains(list, "Market") {
		t.Fatalf("list = %s", list)
	}
	if _, err := ch.selectConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "alice-c0001",
	}); err != nil {
		t.Fatalf("select: %v", err)
	}
	if ch.getActiveConversationID("alice") != "alice-c0001" {
		t.Fatalf("active = %s", ch.getActiveConversationID("alice"))
	}
	if _, err := ch.renameConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "alice-c0001",
		"title":           "Renamed",
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := eng.GetConversation("alice", "alice-c0001")
	if err != nil || got.Title != "Renamed" || got.Model != "m1" {
		t.Fatalf("after rename: %+v %v", got, err)
	}
	if _, err := ch.selectConversationTool(context.Background(), "bob", map[string]interface{}{
		"conversation_id": "alice-c0001",
	}); err == nil {
		t.Fatal("cross-user select must fail")
	}
}
