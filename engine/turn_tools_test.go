package engine

import (
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
)

func TestUniquifyToolCallIDsRewritesDuplicates(t *testing.T) {
	calls := []openai.ToolCall{
		{ID: "1", Function: openai.FunctionCall{Name: "a"}},
		{ID: "1", Function: openai.FunctionCall{Name: "b"}},
		{ID: "", Function: openai.FunctionCall{Name: "c"}},
	}
	uniquifyToolCallIDs(calls)
	if calls[0].ID != "1" {
		t.Fatalf("first id = %q, want 1", calls[0].ID)
	}
	if calls[1].ID != "1-2" {
		t.Fatalf("duplicate id = %q, want 1-2", calls[1].ID)
	}
	if calls[2].ID != "call-3" {
		t.Fatalf("empty id = %q, want call-3", calls[2].ID)
	}
}

func TestCollapseCompletedTurnToolsDropsPriorToolPairs(t *testing.T) {
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "first"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "create_alert"}}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "1", Content: "ok"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
		{Role: openai.ChatMessageRoleUser, Content: "second"},
	}
	got := collapseCompletedTurnTools(msgs)
	if len(got) != 4 {
		t.Fatalf("len=%d want 4: %#v", len(got), got)
	}
	if got[1].Role != openai.ChatMessageRoleAssistant || len(got[1].ToolCalls) != 0 {
		t.Fatalf("prior assistant still has tool calls: %#v", got[1])
	}
	if !strings.Contains(got[1].Content, "create_alert") {
		t.Fatalf("collapsed assistant content = %q", got[1].Content)
	}
	if got[2].Content != "done" {
		t.Fatalf("prior final assistant = %q", got[2].Content)
	}
	if got[3].Content != "second" {
		t.Fatalf("current user = %q", got[3].Content)
	}
}

func TestEstimateChatUsageWhenProviderOmitsTokens(t *testing.T) {
	prompt, completion := estimateChatUsage(
		[]openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: strings.Repeat("x", 40)}},
		openai.ChatCompletionChoice{Message: openai.ChatCompletionMessage{Content: strings.Repeat("y", 8)}},
	)
	if prompt < 1 || completion < 1 {
		t.Fatalf("prompt=%d completion=%d", prompt, completion)
	}
}
