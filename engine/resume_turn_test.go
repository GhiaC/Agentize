package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

func TestPlanTurnResumeRetriesOnlyFailedLastTool(t *testing.T) {
	now := time.Now()
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "check price"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{
			{ID: "ok-call", Function: openai.FunctionCall{Name: "lookup"}},
			{ID: "bad-call", Function: openai.FunctionCall{Name: "place"}},
		}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "ok-call", Content: "quoted"},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "bad-call", Content: "Error executing tool place: timeout"},
	}
	stored := []*model.Message{
		{MessageID: "u1", SeqID: 1, Role: openai.ChatMessageRoleUser, Content: "check price"},
		{MessageID: "a1", SeqID: 2, Role: openai.ChatMessageRoleAssistant, Content: "[Tool Calls: lookup, place]", HasToolCalls: true},
	}
	tools := []*model.ToolCall{
		{ToolID: "1", ToolCallID: "ok-call", MessageID: "a1", UserMessageID: "u1", FunctionName: "lookup", Status: model.ToolCallStatusSuccess, CreatedAt: now},
		{ToolID: "2", ToolCallID: "bad-call", MessageID: "a1", UserMessageID: "u1", FunctionName: "place", Arguments: `{"x":1}`, Status: model.ToolCallStatusFailed, CreatedAt: now.Add(time.Second)},
	}
	plan := planTurnResume(msgs, stored, tools)
	if !plan.Resumable || plan.UserMessageID != "u1" {
		t.Fatalf("plan = %#v", plan)
	}
	if len(plan.Retry) != 1 || plan.Retry[0].ToolID != "2" || plan.Retry[0].Call.ID != "bad-call" {
		t.Fatalf("retry = %#v", plan.Retry)
	}
	if len(msgs) != 4 || msgs[0].Content != "check price" {
		t.Fatal("planning must not delete messages")
	}
}

func TestPlanTurnResumeContinuesIncompleteDAGWithoutRerunningSuccess(t *testing.T) {
	now := time.Now()
	stored := []*model.Message{
		{MessageID: "u1", SeqID: 1, Role: openai.ChatMessageRoleUser, Content: "go"},
		{MessageID: "a1", SeqID: 2, Role: openai.ChatMessageRoleAssistant, Content: "[Tool Calls: lookup]", HasToolCalls: true},
	}
	tools := []*model.ToolCall{{
		ToolID: "1", ToolCallID: "ok-call", MessageID: "a1", UserMessageID: "u1",
		FunctionName: "lookup", Status: model.ToolCallStatusSuccess, Response: "quoted", CreatedAt: now,
	}}
	plan := planTurnResume(nil, stored, tools)
	if !plan.Resumable || len(plan.Retry) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
	restored := ensureTurnToolMessages(nil, tools)
	if len(restored) != 2 || restored[1].ToolCallID != "ok-call" || restored[1].Content != "quoted" {
		t.Fatalf("restored = %#v", restored)
	}
}

func TestPlanTurnResumeSkipsFinishedTurn(t *testing.T) {
	stored := []*model.Message{
		{MessageID: "u1", SeqID: 1, Role: openai.ChatMessageRoleUser, Content: "go"},
		{MessageID: "a1", SeqID: 2, Role: openai.ChatMessageRoleAssistant, Content: "done"},
	}
	if TurnCanResume(nil, stored, nil) {
		t.Fatal("finished turn must not resume")
	}
}

func TestRemoveFunctionCallsKeepsLatestTurnTools(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := model.NewSessionWithID("user-1", "1", model.AgentTypeConversation)
	session.UpdatedAt = time.Now().Add(-3 * time.Hour)
	session.Msgs = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "old"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "old-call"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "old-call", Content: "old-result"},
		{Role: openai.ChatMessageRoleUser, Content: "check price"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "bad-call", Function: openai.FunctionCall{Name: "place"}}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "bad-call", Content: "Error executing tool place: timeout"},
	}
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Sessions: st, dbReady: true}
	if err := e.removeFunctionCalls(model.WithUserID(context.Background(), "user-1"), "1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetUserSession("user-1", "1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range got.Msgs {
		if msg.ToolCallID == "bad-call" {
			found = true
		}
		if msg.ToolCallID == "old-call" {
			t.Fatal("older turn tool message should be collapsed")
		}
	}
	if !found {
		t.Fatalf("latest tool message was deleted: %#v", got.Msgs)
	}
}

func TestResumeIncompleteTurnKeepsMessagesAndRetriesLastTool(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(request.Messages)
		if !strings.Contains(string(raw), "check price") || !strings.Contains(string(raw), "filled") {
			t.Fatalf("resume request dropped history: %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resume","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"resumed answer"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	t.Cleanup(server.Close)

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := model.NewSessionWithID("user-1", "9", model.AgentTypeConversation)
	session.Model = "test-model"
	session.Msgs = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "check price"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{
			ID: "bad-call", Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{Name: "place", Arguments: `{"x":1}`},
		}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "bad-call", Name: "place", Content: "Error executing tool place: timeout"},
	}
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.PutMessage(&model.Message{
		MessageID: "u1", SeqID: 1, UserID: "user-1", SessionID: "9",
		Role: openai.ChatMessageRoleUser, Content: "check price", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMessage(&model.Message{
		MessageID: "a1", SeqID: 2, UserID: "user-1", SessionID: "9",
		Role: openai.ChatMessageRoleAssistant, Content: "[Tool Calls: place]", HasToolCalls: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutToolCall(&model.ToolCall{
		ToolID: "7", ToolCallID: "bad-call", MessageID: "a1", UserMessageID: "u1",
		SessionID: "9", UserID: "user-1", FunctionName: "place", Arguments: `{"x":1}`,
		Status: model.ToolCallStatusFailed, Error: "timeout", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config := openai.DefaultConfig("test")
	config.BaseURL = server.URL
	e := &Engine{
		Sessions:  st,
		dbReady:   true,
		llmClient: openai.NewClientWithConfig(config),
		llmConfig: LLMConfig{BackupDisabled: true, Model: "test-model"},
		Executor: func(name string, args map[string]interface{}) (string, error) {
			calls++
			if name != "place" {
				t.Fatalf("retried unexpected tool %s", name)
			}
			return "filled", nil
		},
	}
	reply, _, err := e.ResumeIncompleteTurn(model.WithUserID(context.Background(), "user-1"), "9")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "resumed answer" {
		t.Fatalf("reply = %q", reply)
	}
	if calls != 1 {
		t.Fatalf("tool executions = %d, want 1", calls)
	}
	got, err := st.GetUserSession("user-1", "9")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msgs) < 3 || got.Msgs[0].Content != "check price" || got.Msgs[2].ToolCallID != "bad-call" || got.Msgs[2].Content != "filled" {
		t.Fatalf("transcript changed incorrectly: %#v", got.Msgs)
	}
	tools, err := st.GetUserToolCallsBySession("user-1", "9")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].ToolID != "7" || tools[0].Status != model.ToolCallStatusSuccess || tools[0].Response != "filled" {
		t.Fatalf("tool rows = %#v", tools)
	}
}
