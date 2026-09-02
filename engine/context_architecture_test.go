package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

func contextTestEngine(t *testing.T) (*Engine, *store.SQLiteStore) {
	t.Helper()
	base := t.TempDir()
	for path, files := range map[string]map[string]string{
		"root": {
			"node.yaml":  "id: root\ntitle: Root\ndescription: root node\n",
			"node.md":    "root content",
			"tools.json": `{"tools":[{"name":"root_tool","description":"root only","input_schema":{"type":"object"},"status":"active"}]}`,
		},
		"root/child": {
			"node.yaml":  "id: child\ntitle: Child\ndescription: child node\n",
			"node.md":    "child content",
			"tools.json": `{"tools":[{"name":"child_tool","description":"child only","input_schema":{"type":"object"},"status":"active"}]}`,
		},
	} {
		dir := filepath.Join(base, path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	repo, err := fsrepo.NewNodeRepository(base)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	eng := &Engine{Repo: repo, Sessions: st, Functions: model.NewFunctionRegistry()}
	eng.UseFunctionRegistry(eng.Functions)
	return eng, st
}

func TestGetToolsOnlyUsesExplicitlyOpenedNodes(t *testing.T) {
	eng, _ := contextTestEngine(t)
	session := model.NewSessionWithID("u1", "s1", model.AgentTypeConversation)
	names := toolNames(eng.GetTools(session))
	if names["root_tool"] || names["child_tool"] {
		t.Fatalf("unopened knowledge tools leaked: %v", names)
	}
	session.NodeDigests = []model.NodeDigest{{Path: "root/child"}}
	names = toolNames(eng.GetTools(session))
	if !names["child_tool"] {
		t.Fatal("opened child tool missing")
	}
	if names["root_tool"] {
		t.Fatal("opening a child must not grant its parent tool")
	}
}

func TestSystemPromptEntriesExposeTypedContext(t *testing.T) {
	eng, st := contextTestEngine(t)
	user, _ := st.GetOrCreateUser("u1")
	user.ContextSummary = model.SummaryEntries{"prefers concise answers"}
	user.ContextTags = []string{"concise"}
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	session := model.NewSessionWithID("u1", "s1", model.AgentTypeConversation)
	session.Title = "Plan"
	session.Summary = model.SummaryEntries{"built a plan"}
	session.Tags = []string{"planning"}
	session.NodeDigests = []model.NodeDigest{{Path: "root/child"}}
	entries := eng.GetSystemPromptEntries(session)
	byKey := map[string]string{}
	for _, entry := range entries {
		byKey[entry.Key] = entry.Content
	}
	for _, key := range []string{"agent_instructions", "user_context", "session_context"} {
		if byKey[key] == "" {
			t.Errorf("missing prompt entry %s", key)
		}
	}
	for _, forbidden := range []string{"knowledge_tree", "opened_node_1", "opened_tools", "positions", "web_results", "user_files"} {
		if _, ok := byKey[forbidden]; ok {
			t.Fatalf("tool-retrievable data leaked into prompt: %s", forbidden)
		}
	}
	if !strings.Contains(byKey["user_context"], "prefers concise") {
		t.Fatal("user context missing")
	}
	if !strings.Contains(byKey["session_context"], "Plan") {
		t.Fatal("session title missing")
	}
}

func TestManageKnowledgeSearchGetAndOpen(t *testing.T) {
	eng, st := contextTestEngine(t)
	session := model.NewSessionWithID("u1", "s1", model.AgentTypeConversation)
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	base := map[string]interface{}{"__user_id__": "u1", "__session_id__": "s1"}
	base["action"], base["query"] = "search", "child"
	result, err := eng.Functions.Execute("manage_knowledge", base)
	if err != nil || !strings.Contains(result, "root/child") {
		t.Fatalf("search: %q %v", result, err)
	}
	base["action"], base["path"] = "get", "root/child"
	result, err = eng.Functions.Execute("manage_knowledge", base)
	if err != nil || !strings.Contains(result, "child content") {
		t.Fatalf("get: %q %v", result, err)
	}
	base["action"] = "open"
	result, err = eng.Functions.Execute("manage_knowledge", base)
	if err != nil || !strings.Contains(result, "child content") {
		t.Fatalf("open: %q %v", result, err)
	}
	loaded, _ := st.Get("s1")
	if !toolNames(eng.GetTools(loaded))["child_tool"] {
		t.Fatal("opening node did not activate its tool")
	}
}

func TestOpenNodeIsRegisteredAndReturnsContent(t *testing.T) {
	eng, st := contextTestEngine(t)
	session := model.NewSessionWithID("u1", "s1", model.AgentTypeConversation)
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	names := toolNames(eng.GetTools(session))
	for _, want := range []string{"open_node", "close_node", "manage_knowledge"} {
		if !names[want] {
			t.Fatalf("platform knowledge tool %s missing from GetTools: %v", want, names)
		}
	}
	result, err := eng.Functions.Execute("open_node", map[string]interface{}{
		"path": "root/child", "__session_id__": "s1", "__user_id__": "u1",
	})
	if err != nil {
		t.Fatalf("open_node: %v", err)
	}
	if !strings.Contains(result, "child content") || !strings.Contains(result, "child_tool") {
		t.Fatalf("open_node result missing content/tools: %s", result)
	}
	loaded, _ := st.Get("s1")
	if !toolNames(eng.GetTools(loaded))["child_tool"] {
		t.Fatal("open_node did not activate child_tool")
	}
	if _, err := eng.Functions.Execute("close_node", map[string]interface{}{
		"path": "root/child", "__session_id__": "s1",
	}); err != nil {
		t.Fatalf("close_node: %v", err)
	}
}

func TestSearchToolsReturnsNodePathWithoutActivating(t *testing.T) {
	eng, st := contextTestEngine(t)
	session := model.NewSessionWithID("u1", "s1", model.AgentTypeConversation)
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	result, discovered := eng.executeSearchTools(eng.GetTools(session), `{"query":"child"}`, nil)
	if !strings.Contains(result, "child_tool") || !strings.Contains(result, "root/child") {
		t.Fatalf("search_tools: %s", result)
	}
	if len(discovered) != 0 {
		t.Fatalf("search_tools must not load unopened schemas: %v", discovered)
	}
	if toolNames(eng.GetTools(session))["child_tool"] {
		t.Fatal("search_tools activated an unopened node")
	}
}

func TestManageContextIsAppendOnlyAndOwnerScoped(t *testing.T) {
	eng, st := contextTestEngine(t)
	session := model.NewSessionWithID("u1", "s1", model.AgentTypeConversation)
	if err := st.Put(session); err != nil {
		t.Fatal(err)
	}
	base := map[string]interface{}{"__user_id__": "u1", "__session_id__": "s1", "scope": "user"}
	base["action"], base["entries"] = "add_summary", []interface{}{"likes Go", "likes Go"}
	if _, err := eng.Functions.Execute("manage_context", base); err != nil {
		t.Fatal(err)
	}
	user, _ := st.GetUser("u1")
	if len(user.ContextSummary) != 1 {
		t.Fatalf("summary must dedupe: %v", user.ContextSummary)
	}
	foreign := map[string]interface{}{"__user_id__": "u2", "__session_id__": "s1", "scope": "session", "action": "get"}
	if _, err := eng.Functions.Execute("manage_context", foreign); err == nil {
		t.Fatal("foreign session context was exposed")
	}
}

func toolNames(tools []openai.Tool) map[string]bool {
	out := map[string]bool{}
	for _, tool := range tools {
		if tool.Function != nil {
			out[tool.Function.Name] = true
		}
	}
	return out
}
