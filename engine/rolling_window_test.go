package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	openai "github.com/sashabaranov/go-openai"
)

func msgs(n int) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, n)
	for i := 0; i < n; i++ {
		out[i] = openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: string(rune('a' + i))}
	}
	return out
}

func TestSplitRollingWindow(t *testing.T) {
	cases := []struct {
		name             string
		total, retain    int
		wantArch, wantKp int
	}{
		{"more than window", 30, 10, 20, 10},
		{"exactly window", 10, 10, 0, 10},
		{"fewer than window", 4, 10, 0, 4},
		{"empty", 0, 10, 0, 0},
		{"retain zero archives all", 5, 0, 5, 0},
		{"retain negative treated as zero", 5, -3, 5, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arch, keep := splitRollingWindow(msgs(c.total), c.retain)
			if len(arch) != c.wantArch {
				t.Fatalf("archive: got %d want %d", len(arch), c.wantArch)
			}
			if len(keep) != c.wantKp {
				t.Fatalf("keep: got %d want %d", len(keep), c.wantKp)
			}
			// The kept messages must be the most recent ones (a contiguous tail).
			if len(keep) > 0 {
				all := msgs(c.total)
				if keep[len(keep)-1].Content != all[len(all)-1].Content {
					t.Fatalf("kept tail mismatch: last kept=%q want %q", keep[len(keep)-1].Content, all[len(all)-1].Content)
				}
			}
			if len(arch)+len(keep) != c.total {
				t.Fatalf("archive+keep=%d want total %d", len(arch)+len(keep), c.total)
			}
		})
	}
}

func TestSummarizationEligibilityDistinguishesValidEmptyFromLegacyEmpty(t *testing.T) {
	now := time.Now()
	config := DefaultSessionSchedulerConfig()
	config.FirstSummarizationThreshold = 5
	config.SubsequentMessageThreshold = 25
	config.ImmediateSummarizationThreshold = 50
	scheduler := &SessionScheduler{config: config}

	legacy := &model.Session{
		Msgs:         msgs(5),
		SummarizedAt: now.Add(-2 * time.Hour),
		UpdatedAt:    now,
	}
	if !scheduler.isEligibleForSummarization(legacy, now) {
		t.Fatal("legacy empty summary should use the first/recovery threshold")
	}

	validNoop := legacy.Clone()
	validNoop.SummaryInitialized = true
	if scheduler.isEligibleForSummarization(validNoop, now) {
		t.Fatal("a valid empty summary must use the subsequent threshold")
	}
}

// toolConv builds a realistic conversation with two tool-call/result pairs:
//
//	[0] user
//	[1] assistant (ToolCalls=[X])
//	[2] tool     (ToolCallID=X)
//	[3] assistant (text response)
//	[4] user
//	[5] assistant (ToolCalls=[Y])
//	[6] tool     (ToolCallID=Y)
//	[7] assistant (text response)
func toolConv() []openai.ChatCompletionMessage {
	return []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "user1"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "X"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "X", Content: "resultX"},
		{Role: openai.ChatMessageRoleAssistant, Content: "resp1"},
		{Role: openai.ChatMessageRoleUser, Content: "user2"},
		{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "Y"}}},
		{Role: openai.ChatMessageRoleTool, ToolCallID: "Y", Content: "resultY"},
		{Role: openai.ChatMessageRoleAssistant, Content: "resp2"},
	}
}

func TestSplitRollingWindow_ToolCallPairNotSplit(t *testing.T) {
	// Verify that toKeep never starts with a "tool" message regardless of
	// where the naive cut lands.
	conv := toolConv() // 8 messages
	for retain := 0; retain <= len(conv)+2; retain++ {
		arch, keep := splitRollingWindow(conv, retain)
		if len(keep) > 0 && keep[0].Role == openai.ChatMessageRoleTool {
			t.Errorf("retain=%d: toKeep starts with role=tool (orphaned tool result) — arch=%d keep=%d",
				retain, len(arch), len(keep))
		}
		if len(arch)+len(keep) != len(conv) {
			t.Errorf("retain=%d: arch(%d)+keep(%d) != total(%d)", retain, len(arch), len(keep), len(conv))
		}
	}
}

func TestSplitRollingWindow_CutOnToolResult_ShiftsBack(t *testing.T) {
	// With retain=6 the naive cut is index 2 (the tool-result for X).
	// The fix must move it back to index 1 (the assistant with ToolCalls),
	// so toKeep=[1..7] and toArchive=[0].
	conv := toolConv() // 8 messages
	arch, keep := splitRollingWindow(conv, 6)
	if len(keep) == 0 || keep[0].Role == openai.ChatMessageRoleTool {
		t.Fatalf("expected keep to start before tool-result, got role=%q", keep[0].Role)
	}
	if len(arch)+len(keep) != len(conv) {
		t.Fatalf("arch(%d)+keep(%d) != 8", len(arch), len(keep))
	}
}

func TestSummarizeSessionAppendsMetadataAndSyncsConversation(t *testing.T) {
	responses := []string{`["new decision"]`, "bitcoin,new-topic", "Updated Market Plan"}
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if call >= len(responses) {
			t.Fatalf("unexpected LLM call %d", call+1)
		}
		content := strings.ReplaceAll(responses[call], `"`, `\"`)
		call++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"test","model":"summary-test","choices":[{"message":{"role":"assistant","content":"%s"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`, content)
	}))
	defer server.Close()

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session := model.NewSessionWithID("u1", "u1-conv-s0001", model.AgentTypeConversation)
	session.Title = "New market conversation"
	session.Summary = model.SummaryEntries{"old fact"}
	session.Tags = []string{"bitcoin"}
	session.ArchivedMsgs = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: "stale runtime context"},
		{Role: openai.ChatMessageRoleUser, Content: "old user message"},
	}
	session.Msgs = []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: "current runtime context"},
		{Role: openai.ChatMessageRoleUser, Content: "make a new decision"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
	}
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	conversation := model.NewConversation("u1", "u1-c0001", session.SessionID, session.Title, "", 1)
	if err := st.PutConversation(conversation); err != nil {
		t.Fatal(err)
	}

	config := openai.DefaultConfig("test")
	config.BaseURL = server.URL
	client := openai.NewClientWithConfig(config)
	handler := model.NewSessionHandler(st, model.DefaultSessionHandlerConfig())
	schedulerConfig := DefaultSessionSchedulerConfig()
	schedulerConfig.SummaryModel = "summary-test"
	schedulerConfig.RetainRecentMessages = 1
	schedulerConfig.DisableLogs = true
	scheduler := NewSessionScheduler(handler, client, schedulerConfig)
	if err := scheduler.summarizeSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	got, err := st.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Summary, "|") != "old fact|new decision" {
		t.Fatalf("summary = %#v", got.Summary)
	}
	if strings.Join(got.Tags, "|") != "bitcoin|new-topic" {
		t.Fatalf("tags = %#v", got.Tags)
	}
	if got.Title != "Updated Market Plan" {
		t.Fatalf("session title = %q", got.Title)
	}
	for _, archived := range got.ArchivedMsgs {
		if archived.Role == openai.ChatMessageRoleSystem {
			t.Fatal("system prompt leaked into archived history")
		}
	}
	if len(got.Msgs) == 0 || got.Msgs[0].Role != openai.ChatMessageRoleSystem {
		t.Fatalf("current system prompt was not retained: %#v", got.Msgs)
	}
	linked, err := st.GetConversationBySession(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Title != got.Title || linked.UpdatedAt.Before(got.UpdatedAt.Add(-time.Second)) {
		t.Fatalf("conversation metadata not synchronized: %#v", linked)
	}
}

func TestParseSummaryEntriesRejectsEmptyProviderContent(t *testing.T) {
	if _, err := parseSummaryEntries(""); err == nil {
		t.Fatal("empty provider content must fail closed")
	}
}
