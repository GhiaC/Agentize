package core

import (
	"context"
	"strings"
	"testing"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

func newConversationCore(t *testing.T) (*CoreHandler, *engine.Engine) {
	t.Helper()
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	eng := &engine.Engine{Sessions: sqliteStore, Functions: model.NewFunctionRegistry()}
	ch.SetConversationEngine(eng)
	return ch, eng
}

func TestConversationTools_ListCreateSelectRename(t *testing.T) {
	ch, eng := newConversationCore(t)

	created, err := ch.createConversationTool(context.Background(), "alice", map[string]interface{}{
		"title": "Market",
		"model": "m1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(created, "1") {
		t.Fatalf("create result = %s", created)
	}
	list, err := ch.listConversationsTool("alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, "1") || !strings.Contains(list, "Market") {
		t.Fatalf("list = %s", list)
	}
	if !strings.Contains(list, "[CURRENT]") || !strings.Contains(list, "Current: `1`") {
		t.Fatalf("list must mark the current conversation: %s", list)
	}
	if ch.getActiveConversationID("alice") != "1" {
		t.Fatalf("active after create = %s", ch.getActiveConversationID("alice"))
	}
	if _, err := ch.selectConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
	}); err != nil {
		t.Fatalf("select: %v", err)
	}
	if _, err := ch.renameConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
		"title":           "Renamed",
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := eng.GetConversation("alice", "1")
	if err != nil || got.Title != "Renamed" || got.Model != "m1" {
		t.Fatalf("after rename: %+v %v", got, err)
	}
	if _, err := ch.selectConversationTool(context.Background(), "bob", map[string]interface{}{
		"conversation_id": "1",
	}); err == nil {
		t.Fatal("cross-user select must fail")
	}
}

func TestConversationTools_InspectModelArchiveDelete(t *testing.T) {
	ch, eng := newConversationCore(t)
	if _, err := ch.createConversationTool(context.Background(), "alice", map[string]interface{}{
		"title": "Market",
		"model": "m1",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	conv, err := eng.GetConversation("alice", "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	session, err := eng.Sessions.Get(conv.SessionID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.Summary = model.SummaryEntries{"Discussed BTC support and a long plan"}
	session.Tags = []string{"btc", "plan"}
	session.Msgs = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "How is BTC looking?"},
		{Role: openai.ChatMessageRoleAssistant, Content: "Support is holding."},
	}
	if err := eng.Sessions.Put(session); err != nil {
		t.Fatalf("put session: %v", err)
	}

	list, err := ch.listConversationsTool("alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, "Discussed BTC") || !strings.Contains(list, "btc") {
		t.Fatalf("list must include session memory: %s", list)
	}

	detail, err := ch.getConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
	})
	if err != nil {
		t.Fatalf("get_conversation: %v", err)
	}
	if !strings.Contains(detail, "model: m1") && !strings.Contains(detail, "Model: m1") {
		t.Fatalf("detail missing model: %s", detail)
	}
	if !strings.Contains(detail, "How is BTC looking?") || !strings.Contains(detail, "Support is holding.") {
		t.Fatalf("detail missing recent messages: %s", detail)
	}
	if _, err := ch.getConversationTool(context.Background(), "bob", map[string]interface{}{
		"conversation_id": "1",
	}); err == nil {
		t.Fatal("cross-user get must fail")
	}

	if _, err := ch.setConversationModelTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
		"model":           "m2",
	}); err != nil {
		t.Fatalf("set model: %v", err)
	}
	got, _ := eng.GetConversation("alice", "1")
	if got.Model != "m2" || got.Title != "Market" {
		t.Fatalf("after set model: %+v", got)
	}

	if _, err := ch.archiveConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
		"archived":        true,
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got, _ = eng.GetConversation("alice", "1")
	if !got.Archived {
		t.Fatal("expected archived")
	}
	list, _ = ch.listConversationsTool("alice")
	if !strings.Contains(list, "archived") {
		t.Fatalf("list must mark archived: %s", list)
	}
	if _, err := ch.archiveConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
		"archived":        false,
	}); err != nil {
		t.Fatalf("unarchive: %v", err)
	}

	if _, err := ch.deleteConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ch.getActiveConversationID("alice") != "" {
		t.Fatalf("current must clear after deleting current chat, got %q", ch.getActiveConversationID("alice"))
	}
	if _, err := eng.GetConversation("alice", "1"); err == nil {
		t.Fatal("deleted conversation still present")
	}
}

func TestSendConversation_SwitchesCurrent(t *testing.T) {
	ch, _ := newConversationCore(t)
	if _, err := ch.createConversationTool(context.Background(), "alice", map[string]interface{}{
		"title": "First",
	}); err != nil {
		t.Fatalf("create first: %v", err)
	}
	if _, err := ch.createConversationTool(context.Background(), "alice", map[string]interface{}{
		"title": "Second",
	}); err != nil {
		t.Fatalf("create second: %v", err)
	}
	if ch.getActiveConversationID("alice") != "2" {
		t.Fatalf("create should select the new chat, got %s", ch.getActiveConversationID("alice"))
	}

	_, err := ch.sendConversationTool(context.Background(), "alice", map[string]interface{}{
		"conversation_id": "1",
		"message":         "continue the first topic",
	})
	if err == nil {
		t.Fatal("send without an LLM should fail after switching current")
	}
	if ch.getActiveConversationID("alice") != "1" {
		t.Fatalf("send to another chat must change current, got %s", ch.getActiveConversationID("alice"))
	}

	if _, err := ch.sendConversationTool(context.Background(), "bob", map[string]interface{}{
		"conversation_id": "1",
		"message":         "hijack",
	}); err == nil {
		t.Fatal("cross-user send must fail")
	}
	if ch.getActiveConversationID("bob") == "1" {
		t.Fatal("cross-user send must not become current")
	}
}

func TestConversationsStayBehindTools(t *testing.T) {
	ch, eng := newConversationCore(t)
	if _, err := ch.createConversationTool(context.Background(), "alice", map[string]interface{}{
		"title": "Market",
		"model": "m1",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	conv, _ := eng.GetConversation("alice", "1")
	session, _ := eng.Sessions.Get(conv.SessionID)
	session.Summary = model.SummaryEntries{"the user likes go for market bots"}
	session.Tags = []string{"golang"}
	if err := eng.Sessions.Put(session); err != nil {
		t.Fatalf("put: %v", err)
	}

	sections, err := ch.SystemPromptSectionsFor("alice")
	if err != nil {
		t.Fatalf("sections: %v", err)
	}
	for _, s := range sections {
		if s.Key == "conversations" {
			t.Fatal("conversation list must not be injected into system prompts")
		}
	}
}

func TestConversationTools_RegisteredOnCore(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	names := map[string]bool{}
	for _, tool := range ch.getCoreToolsForLLM() {
		if tool.Function != nil {
			names[tool.Function.Name] = true
		}
	}
	for _, want := range []string{
		"list_conversations", "get_conversation", "create_conversation",
		"select_conversation", "send_conversation", "rename_conversation",
		"set_conversation_model", "archive_conversation", "delete_conversation",
	} {
		if !names[want] {
			t.Errorf("missing conversation tool %s", want)
		}
	}
}
