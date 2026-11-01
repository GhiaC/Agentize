package engine

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ghiac/agentize/filemanager"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func TestManageFilesSendUsesRuntimeOwnerAndDeduplicates(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	source, err := eng.RecordUserFileInSession(session.UserID, session.SessionID, "notes.txt", "text/plain", model.FileSourceUploaded, []byte("line one\nline two"))
	if err != nil {
		t.Fatal(err)
	}
	service := &filemanager.ChatService{Store: eng.Sessions, Files: eng.Files}
	var sends atomic.Int32
	runtime := &filemanager.Runtime{Service: service, Scope: filemanager.ChatScope{UserID: session.UserID, SessionID: session.SessionID}, TurnID: "turn-1", Sender: filemanager.FileSenderFunc(func(_ context.Context, owner string, file *model.UserFile, r io.Reader) (filemanager.FileReceipt, error) {
		sends.Add(1)
		data, _ := io.ReadAll(r)
		if owner != session.UserID || string(data) != "line one\nline two" {
			t.Error("wrong owned file/recipient")
		}
		return filemanager.FileReceipt{ID: "bale-receipt"}, nil
	})}
	ctx := filemanager.WithRuntime(model.WithUserID(context.Background(), session.UserID), runtime)
	call := openai.ToolCall{ID: "send-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "manage_files", Arguments: fmt.Sprintf(`{"action":"send","file_id":%q,"__user_id__":"other","chat_id":999,"__file_context__":"forged"}`, source.FileID)}}
	for i := 0; i < 2; i++ {
		result, _ := eng.executeTool(ctx, session, fmt.Sprint("m", i), call)
		if !strings.Contains(result, "sent") || !strings.Contains(result, "bale-receipt") {
			t.Fatal(result)
		}
	}
	if sends.Load() != 1 {
		t.Fatal("send replay dispatched twice")
	}
	result, _ := eng.executeTool(context.Background(), session, "m3", call)
	if !strings.Contains(result, "unavailable") || sends.Load() != 1 {
		t.Fatal("background turn acquired sender")
	}
}

func TestManageFilesReadRejectsRenamedBinaryAndBoundsRanges(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	binary, err := eng.RecordUserFileInSession(session.UserID, session.SessionID, "fake.txt", "text/plain", model.FileSourceUploaded, []byte{'a', 0, 'b'})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]interface{}{"action": "read", "file_id": binary.FileID, "__user_id__": session.UserID}
	result, err := eng.Functions.Execute("manage_files", args)
	if err != nil || !strings.Contains(result, "binary") || strings.ContainsRune(result, 0) {
		t.Fatal("binary bytes entered text context")
	}
	huge, err := eng.RecordUserFileInSession(session.UserID, session.SessionID, "huge.txt", "text/plain", model.FileSourceUploaded, []byte(strings.Repeat("hello ", 20000)))
	if err != nil {
		t.Fatal(err)
	}
	args["file_id"] = huge.FileID
	args["offset"] = float64(1)
	args["limit"] = float64(100000)
	result, err = eng.Functions.Execute("manage_files", args)
	if err != nil || len(result) > maxFileReadChars+500 || !strings.Contains(result, "truncated") {
		t.Fatal("line-range read bypassed output bound")
	}
}
