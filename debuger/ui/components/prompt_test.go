package components

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestToolRetrievablePromptClassifiesDumps(t *testing.T) {
	cases := []struct {
		key, title string
		want       bool
	}{
		{"knowledge_tree", "Knowledge Tree", true},
		{"opened_node_1", "Opened node", true},
		{"web_results", "Web Results", true},
		{"positions", "Open Positions", true},
		{"account_positions", "Positions", true},
		{"user_files", "User Files", true},
		{"account_status", "Account Status", false},
		{"agent_instructions", "Agent Instructions", false},
		{"user_context", "User Context", false},
		{"session_context", "Session Context", false},
	}
	for _, tc := range cases {
		got, _ := ToolRetrievablePrompt(tc.key, tc.title)
		if got != tc.want {
			t.Errorf("%s: tool-retrievable=%v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestRenderPromptArraySplitsCurrentFromExcluded(t *testing.T) {
	html := RenderPromptArray(PromptEntriesFromSnapshot([]model.SystemPromptEntry{
		{Key: "agent_instructions", Title: "Agent Instructions", Content: "rules <b>x</b>", Source: "engine/user_agent.md"},
		{Key: "knowledge_tree", Title: "Knowledge Tree", Content: "all nodes", Source: "legacy"},
		{Key: "positions", Title: "Open Positions", Content: "BTC long", Source: "legacy"},
		{Key: "user_context", Title: "User Context", Content: "", Source: "user"},
	}))
	if strings.Contains(html, "rules <b>x</b>") {
		t.Fatal("prompt content must be HTML-escaped")
	}
	if !strings.Contains(html, "rules &lt;b&gt;x&lt;/b&gt;") {
		t.Fatal("expected escaped prompt content")
	}
	if !strings.Contains(html, "Agent Instructions") || !strings.Contains(html, "user memory") {
		t.Fatal("current prompts must appear in the index")
	}
	if !strings.Contains(html, "Excluded from current prompt") {
		t.Fatal("knowledge/positions must be bucketed out of the current list")
	}
	if !strings.Contains(html, `id="prompt-agent_instructions-0"`) {
		t.Fatal("each current prompt must be an independently addressable document")
	}
}
