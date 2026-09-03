package pages

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

func TestRenderSessionDetailPaginatesAndCollapsesCollections(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	session := model.NewSessionWithID("alice", "alice-conv-s0001", model.AgentTypeConversation)
	session.Summary = model.SummaryEntries{"first immutable fact", "second immutable fact"}
	session.SystemPrompts = []model.SystemPromptEntry{
		{Key: "agent_instructions", Title: "Agent Instructions", Content: "current instructions", Source: "engine/user_agent.md"},
		{Key: "positions", Title: "Open Positions", Content: "BTC long dump", Source: "legacy"},
	}
	session.SystemPromptsUpdatedAt = time.Now()
	session.ArchivedMsgs = []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleSystem, Content: "stale context one"}}
	for i := 1; i <= 26; i++ {
		session.ArchivedMsgs = append(session.ArchivedMsgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: fmt.Sprintf("archived-%02d", i)})
	}
	session.ArchivedMsgs = append(session.ArchivedMsgs, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleSystem, Content: "stale context two"})
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	user, _ := st.GetOrCreateUser("alice")
	user.ContextSummary = model.SummaryEntries{"prefers concise answers"}
	user.ContextTags = []string{"concise"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 26; i++ {
		tc := &model.ToolCall{
			ToolID: fmt.Sprintf("%s-t%04d", session.SessionID, i), ToolCallID: fmt.Sprintf("call-%d", i),
			MessageID: session.SessionID + "-m0001", SessionID: session.SessionID, UserID: session.UserID,
			AgentType: session.AgentType, FunctionName: fmt.Sprintf("tool-%02d", i), Arguments: `{}`, Status: "pending",
			CreatedAt: time.Unix(int64(i), 0),
		}
		if err := st.PutToolCall(tc); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := debuger.NewDebugHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	html, err := RenderUserSessionDetailPage(handler, session.UserID, session.SessionID, SessionDetailPages{
		Prompts: 1, Messages: 1, Archived: 2, Summarization: 1, ToolCalls: 2, Files: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<details class="card mb-4 debug-section"`, "Archived Messages (Debug Only)",
		"Historical snapshots hidden", "26</span>", "archived_page=1", "tools_page=1",
		"first immutable fact", "second immutable fact",
		"Agent Instructions", "agent_instructions", "User Context", "prefers concise answers",
		"Session Context", "Opened Tools",
		"Created At:", "Updated At:", "Summarized At:", "border-top",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q in rendered session page", want)
		}
	}
	if !strings.Contains(html, "Excluded from current prompt") {
		t.Fatal("position dumps must be bucketed out of the current prompt list")
	}
	if strings.Contains(html, "stale context one") || strings.Contains(html, "stale context two") {
		t.Fatal("historical system prompt contents must not be rendered as current prompts")
	}
	if strings.Contains(html, "Session: alice-conv-s0001") || strings.Contains(html, ">alice-conv-s0001</code>") {
		t.Fatal("legacy concat must not be the visible session id")
	}
	if !strings.Contains(html, "Session: 1") || !strings.Contains(html, ">1</code>") {
		t.Fatal("session page should show numeric id")
	}
	if !strings.Contains(html, "archived-01") {
		t.Fatal("second archived page must expose the oldest archived message")
	}
	if !strings.Contains(html, "tool-01") {
		t.Fatal("second tool page must expose the oldest tool call")
	}
}
