package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/sashabaranov/go-openai"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/filestore"
	"github.com/ghiac/agentize/mcpauth"
	"github.com/ghiac/agentize/model"
)

func mcpPrincipal(user string) mcpauth.Principal {
	return mcpauth.Principal{UserID: user, CredentialID: "test-credential", Scopes: []string{"tools:execute", "inference:execute", "conversations:read", "conversations:write", "files:read", "files:write"}}
}
func TestMCPRegisteredToolACLIdentityAndQuota(t *testing.T) {
	eng, base := accessTestEngine(t, map[string]string{"root": "id: root\n", "root/private": "id: private\nauth:\n  inherit: false\n  default: {}\n  users:\n    alice: {perms: rsx}\n"})
	eng.Executor = eng.Functions.Execute
	path := filepath.Join(base, "root/private/tools.json")
	if err := os.WriteFile(path, []byte(`{"tools":[{"name":"private_tool","description":"Private","status":"active","input_schema":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	eng.Functions.MustRegister("private_tool", "Private", func(args map[string]any) (string, error) {
		calls++
		if args["__user_id__"] != "alice" {
			t.Fatal("identity was not injected")
		}
		if _, ok := args["agentize_conversation_id"]; ok {
			t.Fatal("transport field leaked into function")
		}
		return "ok", nil
	})
	conv, err := eng.CreateConversation(CreateConversationInput{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	provider := eng.MCPProvider()
	alice := mcpPrincipal("alice")
	bob := mcpPrincipal("bob")
	tools, err := provider.List(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "agentize_tool_private_tool" {
			found = true
		}
	}
	if !found {
		t.Fatal("registered authorized tool missing")
	}
	tools, err = provider.List(t.Context(), bob)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == "agentize_tool_private_tool" {
			t.Fatal("private schema leaked")
		}
	}
	args := json.RawMessage(`{"agentize_conversation_id":"` + conv.ConversationID + `","value":1}`)
	if _, err := provider.Call(t.Context(), alice, "agentize_tool_private_tool", args); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"agentize_conversation_id":"` + conv.ConversationID + `","value":"bad"}`, `{"agentize_conversation_id":"` + conv.ConversationID + `","value":1,"__user_id__":"bob"}`, `{"agentize_conversation_id":"` + conv.ConversationID + `","value":1,"value":2}`} {
		if _, err := provider.Call(t.Context(), alice, "agentize_tool_private_tool", json.RawMessage(raw)); err == nil {
			t.Fatal("invalid input executed")
		}
	}
	callback := &recordingCallback{blockErr: errors.New("quota")}
	eng.Callback = callback
	if _, err := provider.Call(t.Context(), alice, "agentize_tool_private_tool", args); err == nil {
		t.Fatal("quota was bypassed")
	}
	eng.Callback = nil
	manager := &immediateToolApprovalManager{status: model.ReviewRejected, decision: "reject"}
	eng.ToolApprovalManager = manager
	if _, err := provider.Call(t.Context(), alice, "agentize_tool_private_tool", args); err == nil || manager.requested == nil {
		t.Fatal("approval gate bypassed")
	}
	eng.ToolApprovalManager = nil
	// A stale catalog cannot authorize a newly revoked node.
	if err := os.WriteFile(filepath.Join(base, "root/private/node.yaml"), []byte("id: private\nauth:\n  inherit: false\n  default: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Call(t.Context(), alice, "agentize_tool_private_tool", args); err == nil {
		t.Fatal("revocation bypassed")
	}
	if calls != 1 {
		t.Fatalf("unexpected executions %d", calls)
	}
}
func TestMCPConversationsFilesAndScopeIsolation(t *testing.T) {
	eng, _ := accessTestEngine(t, map[string]string{"root": "id: root\n"})
	files, err := filestore.NewLocalFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng.SetFileStore(files)
	provider := eng.MCPProvider()
	alice := mcpPrincipal("alice")
	bob := mcpPrincipal("bob")
	value, err := provider.Call(t.Context(), alice, "agentize_create_conversation", json.RawMessage(`{"title":"MCP"}`))
	if err != nil {
		t.Fatal(err)
	}
	conv := value.(*model.Conversation)
	raw, _ := json.Marshal(map[string]any{"conversation_id": conv.ConversationID, "name": "test.txt", "data_base64": base64.StdEncoding.EncodeToString([]byte("hello"))})
	value, err = provider.Call(t.Context(), alice, "agentize_upload_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	file := value.(map[string]any)
	id := file["file_id"].(string)
	readArgs, _ := json.Marshal(map[string]any{"file_id": id})
	value, err = provider.Call(t.Context(), alice, "agentize_read_file", readArgs)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["data_base64"] != "aGVsbG8=" {
		t.Fatal("file bytes changed")
	}
	if _, ok := file["StorageKey"]; ok {
		t.Fatal("storage key exposed")
	}
	listed, err := provider.Call(t.Context(), alice, "agentize_list_files", json.RawMessage(`{}`))
	if err != nil || listed.(map[string]any)["total"] != 1 {
		t.Fatal("owned files not discoverable")
	}
	otherFiles, err := provider.Call(t.Context(), bob, "agentize_list_files", json.RawMessage(`{}`))
	if err != nil || otherFiles.(map[string]any)["total"] != 0 {
		t.Fatal("file list crossed users")
	}
	if _, err := provider.Call(t.Context(), bob, "agentize_read_file", readArgs); err == nil {
		t.Fatal("cross-owner file read")
	}
	if _, err := provider.Call(t.Context(), bob, "agentize_upload_file", raw); err == nil {
		t.Fatal("cross-owner upload")
	}
	if _, err := provider.Call(t.Context(), bob, "agentize_get_conversation", json.RawMessage(`{"conversation_id":"`+conv.ConversationID+`"}`)); err == nil {
		t.Fatal("cross-owner conversation read")
	}
	restricted := alice
	restricted.Scopes = []string{"catalog:read"}
	tools, err := provider.List(context.Background(), restricted)
	if err != nil || len(tools) != 0 {
		t.Fatal("scopes ignored")
	}
}

type mcpMockTransport struct{}

func (mcpMockTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"MCP reply"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))}, nil
}
func TestMCPSendPersistsConversationAndUsesBilling(t *testing.T) {
	eng, _ := accessTestEngine(t, map[string]string{"root": "id: root\n"})
	config := openai.DefaultConfig("test")
	config.HTTPClient = &http.Client{Transport: mcpMockTransport{}}
	eng.llmClient = openai.NewClientWithConfig(config)
	eng.llmConfig = LLMConfig{Model: "test-model", BackupDisabled: true, MaxLLMIterations: 2}
	eng.dbReady = true
	callback := &recordingCallback{}
	eng.Callback = callback
	conv, err := eng.CreateConversation(CreateConversationInput{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"conversation_id":"` + conv.ConversationID + `","message":"hello MCP"}`)
	value, err := eng.MCPProvider().Call(t.Context(), mcpPrincipal("alice"), "agentize_send_message", args)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["message"] != "MCP reply" {
		t.Fatal("reply missing")
	}
	session, err := eng.Sessions.GetUserSession("alice", conv.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(session.Msgs)
	if !strings.Contains(string(encoded), "hello MCP") || !strings.Contains(string(encoded), "MCP reply") {
		t.Fatal("conversation history not persisted")
	}
	if callback.before == nil || callback.after == nil {
		t.Fatal("billing callbacks bypassed")
	}
	if _, err := eng.MCPProvider().Call(t.Context(), mcpPrincipal("bob"), "agentize_send_message", args); err == nil {
		t.Fatal("cross-user send accepted")
	}
}
