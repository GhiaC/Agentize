package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func TestAlertInvalidOutputRecoversAnswerWithoutRepeatingTrade(t *testing.T) {
	requests, executions := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests >= 3 {
			if len(req.Tools) != 0 {
				t.Error("answer recovery exposed tools")
			}
			raw, _ := json.Marshal(req.Messages)
			if !strings.Contains(string(raw), "accepted-once") {
				t.Error("lost completed tool receipt")
			}
		}
		switch requests {
		case 1, 3:
			fmt.Fprint(w, `{"id":"tool","model":"test-model","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"place","arguments":"{\"x\":1}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		case 2:
			fmt.Fprint(w, `{"id":"invalid","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"[Tool Calls: place]"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		default:
			fmt.Fprint(w, `{"id":"final","model":"test-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"The existing action was accepted once."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	defer server.Close()
	e, st := testFinalOutputEngine(t, server.URL, func(name string, _ map[string]interface{}) (string, error) {
		executions++
		if name != "place" {
			t.Errorf("unexpected tool %s", name)
		}
		return "accepted-once", nil
	})
	meta := map[string]any{"kind": "alert", "idempotency_key": "alert-event:test"}
	if _, err := st.AdmitSessionRun(model.SessionRunInput{UserID: "user-1", SessionID: "51", OriginKind: "alert", IdempotencyKey: "alert-event:test", Content: "perform the action once", Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	reply, _, err := e.ProcessIncoming(model.WithUserID(context.Background(), "user-1"), "51", IncomingMessage{Content: "perform the action once", Metadata: meta, Queue: QueueDeferred})
	if err != nil {
		t.Fatal(err)
	}
	if executions != 1 || requests != 4 || reply != "The existing action was accepted once." {
		t.Fatalf("executions=%d requests=%d reply=%q", executions, requests, reply)
	}
	run, err := st.FindSessionRun("user-1", "51", "alert-event:test")
	if err != nil || run.Status != model.SessionRunSucceeded {
		t.Fatal("recovered receipt not succeeded")
	}
	messages, err := st.GetUserMessagesBySession("user-1", "51")
	if err != nil {
		t.Fatal(err)
	}
	invalid := 0
	for _, m := range messages {
		if model.MessageInvalidOutput(m) {
			invalid++
		}
	}
	if invalid != 2 {
		t.Fatalf("rejected outputs not marked: %d", invalid)
	}
}
