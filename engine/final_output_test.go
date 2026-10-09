package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

func TestClassifyAssistantChoice(t *testing.T) {
	placeholder := "[Tool Calls: get_account_info, get_research_report, get_forward_overview]"
	tool := []openai.ToolCall{{
		ID: "c1", Type: openai.ToolTypeFunction,
		Function: openai.FunctionCall{Name: "place", Arguments: `{"x":1}`},
	}}
	tests := []struct {
		name   string
		choice openai.ChatCompletionChoice
		want   assistantChoiceKind
	}{
		{name: "tool prose with stop", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: placeholder}}, want: assistantChoiceInvalid},
		{name: "padded placeholder", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: "  " + placeholder + "  "}}, want: assistantChoiceInvalid},
		{name: "whitespace", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: " \n\t "}}, want: assistantChoiceInvalid},
		{name: "empty choice", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop}, want: assistantChoiceInvalid},
		{name: "truncated prose", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonLength, Message: openai.ChatCompletionMessage{Content: "The price is"}}, want: assistantChoiceInvalid},
		{name: "content filter", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonContentFilter, Message: openai.ChatCompletionMessage{Content: "blocked"}}, want: assistantChoiceInvalid},
		{name: "finish tool_calls without calls", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonToolCalls, Message: openai.ChatCompletionMessage{Content: "I should call place"}}, want: assistantChoiceInvalid},
		{name: "protocol leak", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: "to=functions.place {\"x\":1}"}}, want: assistantChoiceInvalid},
		{name: "structured calls ignore stop and prose", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: placeholder, ToolCalls: tool}}, want: assistantChoiceTools},
		{name: "structured calls with tool finish", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonToolCalls, Message: openai.ChatCompletionMessage{ToolCalls: tool}}, want: assistantChoiceTools},
		{name: "short answer", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: "ok"}}, want: assistantChoiceFinal},
		{name: "asks for input", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: "Which symbol should I use?"}}, want: assistantChoiceFinal},
		{name: "mentions tools in a sentence", choice: openai.ChatCompletionChoice{FinishReason: openai.FinishReasonStop, Message: openai.ChatCompletionMessage{Content: "I will not call [Tool Calls: place] from this sentence."}}, want: assistantChoiceFinal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyAssistantChoice(tt.choice); got != tt.want {
				t.Fatalf("classify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlaceholderThenValidAnswerFinishesOnceWithoutTools(t *testing.T) {
	var calls int
	var requests int
	placeholder := "[Tool Calls: get_account_info, get_research_report, get_forward_overview]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		requests++
		raw, _ := json.Marshal(request.Messages)
		body := string(raw)
		if !strings.Contains(body, "check the account") {
			t.Errorf("request dropped the user message: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			fmt.Fprintf(w, `{"id":"bad","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`, placeholder)
			return
		}
		if !strings.Contains(body, "not a valid final answer") {
			t.Errorf("continuation missing protocol correction: %s", body)
		}
		fmt.Fprint(w, `{"id":"good","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Account is flat."}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	t.Cleanup(server.Close)

	e, st := testFinalOutputEngine(t, server.URL, func(name string, _ map[string]interface{}) (string, error) {
		calls++
		t.Errorf("prose placeholder executed tool %s", name)
		return "nope", nil
	})
	ctx := model.WithUserID(context.Background(), "user-1")
	reply, _, err := e.ProcessMessage(ctx, "51", "check the account")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Account is flat." {
		t.Fatalf("reply = %q", reply)
	}
	if calls != 0 {
		t.Fatalf("tool executions = %d", calls)
	}
	if requests != 2 {
		t.Fatalf("provider calls = %d, want 2", requests)
	}
	run, err := st.GetSessionRun("user-1", "51", "1")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != model.SessionRunSucceeded {
		t.Fatalf("run = %#v", run)
	}
	msgs, err := st.GetUserMessagesBySession("user-1", "51")
	if err != nil {
		t.Fatal(err)
	}
	var invalid, final int
	for _, msg := range msgs {
		if msg == nil || msg.Role != openai.ChatMessageRoleAssistant {
			continue
		}
		if model.MessageInvalidOutput(msg) {
			invalid++
			if msg.Content != placeholder || isFinalAssistantStored(msg) {
				t.Fatalf("invalid audit row = %#v", msg)
			}
			continue
		}
		final++
		if msg.Content != "Account is flat." || !isFinalAssistantStored(msg) {
			t.Fatalf("final row = %#v", msg)
		}
	}
	if invalid != 1 || final != 1 {
		t.Fatalf("assistant rows invalid=%d final=%d messages=%d", invalid, final, len(msgs))
	}
	session, err := st.GetUserSession("user-1", "51")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range session.Msgs {
		if msg.Role == openai.ChatMessageRoleAssistant && strings.Contains(msg.Content, "[Tool Calls:") {
			t.Fatalf("placeholder leaked into session history: %#v", session.Msgs)
		}
	}
	traces, err := st.GetRouteTracesBySession("51")
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || traces[0].Status != "ok" {
		t.Fatalf("traces = %#v", traces)
	}
	sawInvalid := false
	for _, node := range traces[0].Nodes {
		if node.Type == model.RouteNodeDecision && strings.Contains(node.Detail, "invalid_output") {
			if node.Status != model.RouteStatusError {
				t.Fatalf("invalid decision status = %s", node.Status)
			}
			sawInvalid = true
		}
		if node.Type == model.RouteNodeToolCall {
			t.Fatalf("trace recorded a tool call: %#v", node)
		}
	}
	if !sawInvalid {
		t.Fatalf("trace missing invalid_output decision: %#v", traces[0].Nodes)
	}
}

func TestInvalidOutputExhaustionDoesNotSucceed(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"bad","model":"test-model","choices":[{"finish_reason":"length","message":{"role":"assistant","content":"   "}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	e, st := testFinalOutputEngine(t, server.URL, func(name string, _ map[string]interface{}) (string, error) {
		t.Fatalf("tool %s executed", name)
		return "", nil
	})
	_, _, err := e.ProcessMessage(model.WithUserID(context.Background(), "user-1"), "51", "scan")
	if !errors.Is(err, ErrInvalidFinalOutput) {
		t.Fatalf("err = %v", err)
	}
	if requests != maxInvalidOutputContinuations+1 {
		t.Fatalf("provider calls = %d, want %d", requests, maxInvalidOutputContinuations+1)
	}
	run, err := st.GetSessionRun("user-1", "51", "1")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != model.SessionRunFailed {
		t.Fatalf("run = %#v, want failed", run)
	}
	session, err := st.GetUserSession("user-1", "51")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range session.Msgs {
		if msg.Role == openai.ChatMessageRoleAssistant {
			t.Fatalf("invalid output committed to session: %#v", session.Msgs)
		}
	}
	traces, err := st.GetRouteTracesBySession("51")
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || traces[0].Status == "ok" {
		t.Fatalf("trace status = %#v", traces)
	}
}

func TestStopFinishWithStructuredToolCallsStillExecutes(t *testing.T) {
	var requests int
	var gotArgs string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			fmt.Fprint(w, `{"id":"tools","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"[Tool Calls: other_tool]","tool_calls":[{"id":"c1","type":"function","function":{"name":"place","arguments":"{\"x\":1}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		fmt.Fprint(w, `{"id":"done","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Order is open."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(server.Close)
	e, _ := testFinalOutputEngine(t, server.URL, func(name string, args map[string]interface{}) (string, error) {
		if name != "place" {
			return "", fmt.Errorf("unexpected tool %s", name)
		}
		raw, _ := json.Marshal(args["x"])
		gotArgs = string(raw)
		return "accepted", nil
	})
	reply, _, err := e.ProcessMessage(model.WithUserID(context.Background(), "user-1"), "51", "place it")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Order is open." || gotArgs != "1" || requests != 2 {
		t.Fatalf("reply=%q args=%s requests=%d", reply, gotArgs, requests)
	}
}

func testFinalOutputEngine(t *testing.T, baseURL string, exec ToolExecutor) (*Engine, *store.SQLiteStore) {
	t.Helper()
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := model.NewSessionWithID("user-1", "51", model.AgentTypeConversation)
	session.Model = "test-model"
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	config := openai.DefaultConfig("test")
	config.BaseURL = baseURL
	return &Engine{
		Sessions:  st,
		dbReady:   true,
		llmClient: openai.NewClientWithConfig(config),
		llmConfig: LLMConfig{BackupDisabled: true, Model: "test-model", MaxLLMIterations: 6},
		Executor:  exec,
	}, st
}
