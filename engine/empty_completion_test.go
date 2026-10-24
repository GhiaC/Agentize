package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func TestEmptyCompletionRecoveryPreservesToolResults(t *testing.T) {
	var requests, executions int
	var lastRequest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"lookup-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`)
		case 2:
			lastRequest = string(body)
			var req openai.ChatCompletionRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
			}
			found := false
			for _, msg := range req.Messages {
				if msg.Role == "tool" && msg.Content == "verified result" {
					found = true
				}
			}
			if !found {
				t.Error("completed tool result missing")
			}
			fmt.Fprint(w, `{"choices":[]}`)
		default:
			if string(body) != lastRequest {
				t.Error("recovery changed the request")
			}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
		}
	}))
	defer server.Close()
	e, st := testFinalOutputEngine(t, server.URL, func(string, map[string]interface{}) (string, error) {
		executions++
		return "verified result", nil
	})
	reply, _, err := e.ProcessMessage(model.WithUserID(context.Background(), "user-1"), "51", "check")
	if err != nil || reply != "done" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if requests != 3 || executions != 1 {
		t.Fatalf("requests=%d executions=%d", requests, executions)
	}
	run, err := st.GetSessionRun("user-1", "51", "1")
	if err != nil || run == nil || run.Status != model.SessionRunSucceeded {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func TestEmptyCompletionRetryBoundAndCancellation(t *testing.T) {
	for _, body := range []string{`{}`, `{"choices":null}`, `{"choices":[]}`} {
		t.Run(body, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			e, _ := testFinalOutputEngine(t, server.URL, nil)
			req := openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hello"}}}
			_, err := e.callLLMWithNetworkRetry(context.Background(), req)
			if requests != 3 || PublicTurnError(err) != PublicIncompleteResponse {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			requests = 0
			_, err = e.callLLMWithNetworkRetry(ctx, req)
			if !errors.Is(err, context.DeadlineExceeded) || requests != 1 {
				t.Fatalf("cancel requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestCompletionAuthAndCreditErrorsDoNotRetry(t *testing.T) {
	for _, status := range []int{401, 402, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"access or billing failure"}}`)
			}))
			defer server.Close()
			e, _ := testFinalOutputEngine(t, server.URL, nil)
			_, err := e.callLLMWithNetworkRetry(context.Background(), openai.ChatCompletionRequest{Model: "test-model", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hello"}}})
			if err == nil || requests != 1 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
		})
	}
}
