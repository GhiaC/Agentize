package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/filemanager"
	"github.com/ghiac/agentize/filestore"
	"github.com/sashabaranov/go-openai"
)

type fileRecordingTransport struct {
	next       http.RoundTripper
	sawContent atomic.Bool
}

func (t *fileRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(data, []byte("verified text")) {
		t.sawContent.Store(true)
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	return t.next.RoundTrip(req)
}

func TestCoreFileMetadataAndPinnedConversationReachWorker(t *testing.T) {
	ch, st, coreTx, agentTx := newDispatchableCore(t, "researcher")
	defer st.Close()
	worker, _ := ch.agents.Get("researcher")
	recorder := &fileRecordingTransport{next: agentTx["researcher"]}
	cfg := worker.Engine.GetLLMConfig()
	cfg.HTTPClient = &http.Client{Transport: recorder}
	if err := worker.Engine.UseLLMConfig(cfg); err != nil {
		t.Fatal(err)
	}
	fs, err := filestore.NewLocalFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker.Engine.SetFileStore(fs)
	worker.Engine.Executor = func(name string, args map[string]interface{}) (string, error) {
		return worker.Engine.Functions.Execute(name, args)
	}
	eng := &engine.Engine{Sessions: st}
	first, err := eng.CreateConversation(engine.CreateConversationInput{UserID: "user1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng.CreateConversation(engine.CreateConversationInput{UserID: "user1"})
	if err != nil {
		t.Fatal(err)
	}
	service := &filemanager.ChatService{Store: st, Files: fs}
	scope := filemanager.ChatScope{UserID: "user1", ConversationID: first.ConversationID, SessionID: first.SessionID}
	prepared, err := service.Prepare(context.Background(), filemanager.ChatInput{Scope: scope, MessageKey: "incoming", Attachments: []filemanager.IncomingAttachment{{Name: "config.txt", Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("verified text")), nil
	}}}})
	if err != nil || len(prepared.Files) != 1 {
		t.Fatal(err)
	}
	user, err := st.GetOrCreateUser("user1")
	if err != nil {
		t.Fatal(err)
	}
	user.ActiveConversationID = second.ConversationID
	user.SetActiveSessionID(worker.Config.AgentType, second.SessionID)
	if err := st.PutUser(user); err != nil {
		t.Fatal(err)
	}
	runtime := &filemanager.Runtime{Service: service, Scope: scope, Worker: worker.Config.AgentType, TurnID: "incoming", Introduction: filemanager.AttachmentMessage("", prepared)}
	ctx := filemanager.WithRuntime(context.Background(), runtime)
	// The routing model intentionally omits every file ID; the runtime must carry them.
	coreTx.AddResponse(coreDispatchTurn("researcher", "inspect the attachment"))
	agentTx["researcher"].AddResponse(openai.ChatCompletionResponse{Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{{ID: "read", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "manage_files", Arguments: fmt.Sprintf(`{"action":"read","file_id":%q}`, prepared.Files[0].FileID)}}}}, FinishReason: openai.FinishReasonToolCalls}}})
	agentTx["researcher"].AddResponse(agentAnswer("inspected"))
	response, _, err := ch.ProcessMessageWithGeneratedFiles(ctx, "user1", prepared.Text)
	if err != nil || response != "inspected" {
		t.Fatalf("Core attachment turn: %q %v", response, err)
	}
	messages, err := st.GetUserMessagesBySession("user1", first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	seenMeta := false
	for _, message := range messages {
		if strings.Contains(message.Content, "size_bytes") && strings.Contains(message.Content, "action=\"send\"") {
			seenMeta = true
		}
	}
	if !seenMeta || !recorder.sawContent.Load() {
		t.Fatalf("pinned worker metadata=%v content=%v", seenMeta, recorder.sawContent.Load())
	}

	unrelated, err := st.GetUserMessagesBySession("user1", second.SessionID)
	if err != nil || len(unrelated) != 0 {
		t.Fatal("file turn executed in subsequently active conversation")
	}
}
