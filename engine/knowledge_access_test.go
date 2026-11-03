package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func accessTestEngine(t *testing.T, nodes map[string]string) (*Engine, string) {
	t.Helper()
	base := t.TempDir()
	for nodePath, policy := range nodes {
		dir := filepath.Join(base, nodePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "node.yaml"), []byte(policy), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "node.md"), []byte("content "+nodePath), 0644); err != nil {
			t.Fatal(err)
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
	t.Cleanup(func() { _ = st.Close() })
	eng := &Engine{Repo: repo, Sessions: st}
	eng.UseFunctionRegistry(model.NewFunctionRegistry())
	return eng, base
}

func TestKnowledgeAuthorizationInheritanceMembershipAndExplicitDenial(t *testing.T) {
	eng, _ := accessTestEngine(t, map[string]string{
		"root":                  "id: root\nauth:\n  default: {perms: rsx}\n",
		"root/parent":           "id: parent\n",
		"root/parent/inherited": "id: inherited\n",
		"root/parent/private":   "id: private\nauth:\n  inherit: false\n  default: {}\n  users:\n    owner: {perms: rsx}\n  groups:\n    team: {perms: rsx}\n  roles:\n    operator: {perms: rsx}\n",
	})
	eng.KnowledgeMemberships = func(user string) ([]string, []string) {
		if user == "role-user" {
			return []string{"operator"}, nil
		}
		if user == "group-user" {
			return nil, []string{"team"}
		}
		return nil, nil
	}
	for _, user := range []string{"owner", "role-user", "group-user", "stranger"} {
		if _, err := eng.readableKnowledgeNode(user, "root/parent/inherited"); err != nil {
			t.Fatalf("grandparent inheritance failed for %s: %v", user, err)
		}
		_, err := eng.executableKnowledgeNode(user, "root/parent/private")
		if (err == nil) != (user != "stranger") {
			t.Fatalf("membership decision for %s: %v", user, err)
		}
	}
	if _, err := eng.readableKnowledgeNode("", "root"); err == nil {
		t.Fatal("identity-free knowledge read accepted")
	}
}

func TestKnowledgeAuthorizationRechecksCachedRevocationAndRegistryGet(t *testing.T) {
	eng, base := accessTestEngine(t, map[string]string{"root": "id: root\n", "root/private": "id: private\nauth:\n  inherit: false\n  default: {}\n  users:\n    owner: {perms: rsx}\n"})
	toolsPath := filepath.Join(base, "root/private/tools.json")
	if err := os.WriteFile(toolsPath, []byte(`{"tools":[{"name":"private_tool","input_schema":{"type":"object"}}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	session := model.NewSessionWithID("owner", "session", model.AgentTypeConversation)
	session.NodeDigests = []model.NodeDigest{{Path: "root/private"}}
	if err := eng.Sessions.Put(session); err != nil {
		t.Fatal(err)
	}
	calls := 0
	eng.Functions.MustRegister("private_tool", "Private", func(map[string]interface{}) (string, error) { calls++; return "executed", nil })
	captured, _ := eng.Functions.Get("private_tool")
	args := map[string]interface{}{"__user_id__": "owner", "__session_id__": "session"}
	if _, err := captured(args); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Repo.LoadNode("root/private"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "root/private/node.yaml"), []byte("id: private\nauth:\n  inherit: false\n  default: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := captured(args); err == nil {
		t.Fatal("captured function bypassed a revocation after cache population")
	}
	if _, err := eng.readableKnowledgeNode("owner", "root/private"); err == nil {
		t.Fatal("cached policy survived revocation")
	}
	if calls != 1 {
		t.Fatalf("unauthorized invocation reached handler: %d", calls)
	}
	// Restoring permission must not reactivate a node that the user has closed.
	if err := os.WriteFile(filepath.Join(base, "root/private/node.yaml"), []byte("id: private\nauth:\n  default: {perms: rsx}\n  inherit: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	session.NodeDigests = nil
	if err := eng.Sessions.Put(session); err != nil {
		t.Fatal(err)
	}
	if _, err := captured(args); err == nil {
		t.Fatal("closed node executed through a captured handler")
	}
}

func TestKnowledgeResultBoundsKeepJSONUTF8AndPaginationProgress(t *testing.T) {
	content := strings.Repeat("متن فارسی 🦉 ", 3000)
	result := boundedKnowledgeJSON(map[string]interface{}{"path": "root", "content": content})
	if len(result) > maxKnowledgeResultChars || !utf8.ValidString(result) || !json.Valid([]byte(result)) {
		t.Fatal("content truncation broke JSON, UTF-8 or size limit")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["truncated"] != true {
		t.Fatal("truncated content was not marked")
	}
	nodes := []map[string]interface{}{{"path": "root/one", "description": content}, {"path": "root/two", "description": "next"}}
	page := knowledgePage(nodes, 0, 2, 2)
	var p struct {
		Next  int                      `json:"next_offset"`
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal([]byte(page), &p); err != nil {
		t.Fatal(err)
	}
	if len(page) > maxKnowledgeResultChars || p.Next != 1 || len(p.Items) != 1 {
		t.Fatalf("page failed to make bounded progress: %s", page)
	}
}

func TestKnowledgeRemovedToolAndDeletedNodeRemainClosed(t *testing.T) {
	eng, base := accessTestEngine(t, map[string]string{"root": "id: root\n", "root/private": "id: private\n"})
	toolPath := filepath.Join(base, "root/private/tools.json")
	if err := os.WriteFile(toolPath, []byte(`{"tools":[{"name":"removed_tool","input_schema":{"type":"object"}}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	eng.Repo.InvalidateCache("")
	eng.UseFunctionRegistry(eng.Functions)
	eng.Functions.MustRegister("removed_tool", "Removed", func(map[string]interface{}) (string, error) { return "must not run", nil })
	session := model.NewSessionWithID("owner", "session", model.AgentTypeConversation)
	session.NodeDigests = []model.NodeDigest{{Path: "root/./private"}}
	if err := eng.Sessions.Put(session); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(toolPath); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Functions.Execute("removed_tool", map[string]interface{}{"__user_id__": "owner", "__session_id__": "session"}); err == nil {
		t.Fatal("removed declaration became an unguarded host tool")
	}
	if err := os.RemoveAll(filepath.Join(base, "root/private")); err != nil {
		t.Fatal(err)
	}
	if err := eng.closeFile("owner", "session", "root/private"); err != nil {
		t.Fatalf("deleted or revoked node could not be closed: %v", err)
	}
	stored, err := eng.Sessions.GetUserSession("owner", "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.NodeDigests) != 0 {
		t.Fatal("historical path alias survived node closure")
	}
}
