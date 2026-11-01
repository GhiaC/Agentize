package filemanager_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/filemanager"
	"github.com/ghiac/agentize/filestore"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"gopkg.in/yaml.v3"
)

type chatFixture struct {
	service *filemanager.ChatService
	st      store.Store
	files   filestore.FileStore
	scope   filemanager.ChatScope
	reopen  func() store.Store
}

func fixture(t *testing.T, backend string) *chatFixture {
	t.Helper()
	var st store.Store
	var reopen func() store.Store
	if backend == "postgres" {
		configPath := os.Getenv("AGENTIZE_POSTGRES_CONFIG_FILE")
		if configPath == "" {
			t.Skip("real PostgreSQL requires AGENTIZE_POSTGRES_CONFIG_FILE")
		}
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal("read local PostgreSQL test configuration")
		}
		var file struct {
			Postgres struct {
				Addr, Database, User, Password string
				SSLMode                        string `yaml:"ssl_mode"`
			}
		}
		if err := yaml.Unmarshal(raw, &file); err != nil {
			t.Fatal("invalid PostgreSQL test configuration")
		}
		cfg := store.DefaultPostgreSQLStoreConfig()
		cfg.Addr = file.Postgres.Addr
		cfg.Database = file.Postgres.Database
		cfg.User = file.Postgres.User
		cfg.Password = file.Postgres.Password
		cfg.SSLMode = file.Postgres.SSLMode
		cfg.Schema = fmt.Sprintf("chatfiles_%d", time.Now().UnixNano())
		cfg.EphemeralSchema = true
		pg, err := store.NewPostgreSQLStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		st = pg
		reopen = func() store.Store {
			copy := cfg
			copy.EphemeralSchema = false
			other, err := store.NewPostgreSQLStore(copy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			return other
		}
	} else {
		dbPath := filepath.Join(t.TempDir(), "sessions.db")
		sql, err := store.NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		st = sql
		reopen = func() store.Store {
			other, err := store.NewSQLiteStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			return other
		}
	}
	t.Cleanup(func() { st.Close() })
	fs, err := filestore.NewLocalFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := &engine.Engine{Sessions: st}
	conversation, err := eng.CreateConversation(engine.CreateConversationInput{UserID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return &chatFixture{service: &filemanager.ChatService{Store: st, Files: fs}, st: st, files: fs, scope: filemanager.ChatScope{UserID: "alice", ConversationID: conversation.ConversationID, SessionID: conversation.SessionID}, reopen: reopen}
}
func attachment(name string, data []byte, calls *atomic.Int32) filemanager.IncomingAttachment {
	return filemanager.IncomingAttachment{Name: name, DeclaredSize: int64(len(data)), Open: func(context.Context) (io.ReadCloser, error) {
		if calls != nil {
			calls.Add(1)
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}}
}
func prepare(t *testing.T, f *chatFixture, key string, files ...filemanager.IncomingAttachment) *filemanager.PreparedMessage {
	t.Helper()
	prepared, err := f.service.Prepare(context.Background(), filemanager.ChatInput{Scope: f.scope, MessageKey: key, Text: "Please inspect the file", Attachments: files})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestChatFilesPersistenceAndIsolation(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := fixture(t, backend)
			ctx := context.Background()
			var calls atomic.Int32
			input := attachment("../../notes.txt", []byte("سلام\r\nsecond\nlast"), &calls)
			prepared := prepare(t, f, "message-1", input, input)
			if prepared.Replay || len(prepared.Files) != 2 || len(prepared.Failures) != 0 {
				t.Fatalf("bad preparation: %+v", prepared)
			}
			first, second := prepared.Files[0], prepared.Files[1]
			if first.FileID == second.FileID || first.Name == second.Name {
				t.Fatal("same-named files overwritten")
			}
			if !strings.HasPrefix(first.Name, "chats/"+f.scope.ConversationID+"/uploads/") || strings.Contains(first.Name, "..") {
				t.Fatal("unsafe virtual path")
			}
			if first.Details.LineCount == nil || *first.Details.LineCount != 3 || first.Details.SHA256 == "" {
				t.Fatal("missing inspection metadata")
			}
			for _, text := range []string{"Please inspect the file", "The user uploaded", `"path":`, "size_bytes", "mime_type", "line_count", "action=\"read\"", "action=\"send\""} {
				if !strings.Contains(prepared.Text, text) {
					t.Fatalf("missing English introduction %q", text)
				}
			}
			replay := prepare(t, f, "message-1", input, input)
			if !replay.Replay || calls.Load() != 2 || replay.Files[0].FileID != first.FileID {
				t.Fatal("duplicate message downloaded or registered again")
			}
			other := f.reopen()
			loaded, err := other.GetUserFileForUser("alice", first.FileID)
			if err != nil || loaded == nil || loaded.Details.ConversationID != f.scope.ConversationID || loaded.Details.SHA256 != first.Details.SHA256 {
				t.Fatal("metadata did not survive reopen")
			}
			reloaded := &filemanager.ChatService{Store: other, Files: f.files}
			persisted, err := reloaded.Prepare(ctx, filemanager.ChatInput{Scope: f.scope, MessageKey: "message-1", Attachments: []filemanager.IncomingAttachment{input}})
			if err != nil || !persisted.Replay {
				t.Fatal("message receipt did not survive reopen")
			}
			bobConversation, err := (&engine.Engine{Sessions: f.st}).CreateConversation(engine.CreateConversationInput{UserID: "bob"})
			if err != nil {
				t.Fatal(err)
			}
			bobScope := filemanager.ChatScope{UserID: "bob", ConversationID: bobConversation.ConversationID, SessionID: bobConversation.SessionID}
			bob, err := f.service.Prepare(ctx, filemanager.ChatInput{Scope: bobScope, MessageKey: "message-1", Attachments: []filemanager.IncomingAttachment{attachment("notes.txt", []byte("private bob"), nil)}})
			if err != nil || len(bob.Files) != 1 {
				t.Fatal("bob preparation failed")
			}
			if bob.Files[0].FileID != first.FileID || bob.Files[0].StorageKey == first.StorageKey {
				t.Fatal("numeric owner scope test did not isolate bytes")
			}
			if foreign, err := f.st.GetUserFileForUser("bob", second.FileID); err != nil || foreign != nil {
				t.Fatal("foreign file visible")
			}
			forged := bobScope
			forged.SessionID = f.scope.SessionID
			forged.ConversationID = "999999"
			before := calls.Load()
			if _, err := f.service.Prepare(ctx, filemanager.ChatInput{Scope: forged, MessageKey: "forged", Attachments: []filemanager.IncomingAttachment{input}}); err == nil || calls.Load() != before {
				t.Fatal("invalid conversation downloaded file")
			}
			if err := f.st.DeleteUserData("alice"); err != nil {
				t.Fatal(err)
			}
			operations, err := f.st.ListFileOperations(ctx, "alice")
			if err != nil || len(operations) != 0 {
				t.Fatal("file receipts survived user purge")
			}
		})
	}
}

func TestChatFilesConcurrentIDsAndReservations(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := fixture(t, backend)
			const n = 16
			var wg sync.WaitGroup
			ids := make(chan string, n)
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					id, err := f.st.NextUserFileID(context.Background(), "alice")
					ids <- id
					errs <- err
				}()
			}
			wg.Wait()
			close(ids)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			seen := map[string]bool{}
			for id := range ids {
				if seen[id] {
					t.Fatal("duplicate file id")
				}
				seen[id] = true
			}
			var opens atomic.Int32
			successes := make(chan *filemanager.PreparedMessage, n)
			unexpected := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p, err := f.service.Prepare(context.Background(), filemanager.ChatInput{Scope: f.scope, MessageKey: "concurrent", Attachments: []filemanager.IncomingAttachment{attachment("a.txt", []byte("a"), &opens)}})
					if err == nil {
						successes <- p
					} else if !errors.Is(err, filemanager.ErrFileOperationPending) {
						unexpected <- err
					}
				}()
			}
			wg.Wait()
			close(successes)
			successful := 0
			for prepared := range successes {
				successful++
				if len(prepared.Files) != 1 {
					t.Fatal("completed duplicate request lost its file receipt")
				}
			}
			if successful == 0 {
				t.Fatal("no request completed the shared ingestion")
			}
			close(unexpected)
			for err := range unexpected {
				t.Fatal(err)
			}
			if opens.Load() != 1 {
				t.Fatalf("opened %d times", opens.Load())
			}
			files, err := f.st.GetUserFilesByUser("alice")
			if err != nil || len(files) != 1 {
				t.Fatalf("duplicate metadata: %d %v", len(files), err)
			}
		})
	}
}

type failingFinalize struct{ filemanager.ChatBackend }

func (f failingFinalize) CompleteFileUpload(context.Context, *model.FileOperation, int64, *model.UserFile) (bool, error) {
	return false, errors.New("injected metadata failure")
}

type failedSave struct{ filestore.FileStore }

func (f failedSave) Save(string, io.Reader) (string, error) {
	return "", errors.New("injected object failure")
}

func TestChatFilesFailureRecoveryAndInspection(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := fixture(t, backend)
			ctx := context.Background()
			for index, tc := range []struct {
				name  string
				data  []byte
				lines int
			}{{"empty.txt", nil, 0}, {"lf.txt", []byte("a\nb\n"), 2}, {"crlf.txt", []byte("a\r\nb"), 2}, {"binary.txt", []byte{0, 1, 2}, -1}, {"bad.txt", []byte{255, 254}, -1}} {
				prepared := prepare(t, f, fmt.Sprint("inspect", index), attachment(tc.name, tc.data, nil))
				if len(prepared.Files) != 1 {
					t.Fatal(prepared.Failures)
				}
				lines := prepared.Files[0].Details.LineCount
				if tc.lines < 0 {
					if lines != nil {
						t.Fatal("binary line count invented")
					}
				} else if lines == nil || *lines != tc.lines {
					t.Fatalf("line count for %s", tc.name)
				}
			}
			broken := &filemanager.ChatService{Store: failingFinalize{f.st}, Files: f.files}
			failed, err := broken.Prepare(ctx, filemanager.ChatInput{Scope: f.scope, MessageKey: "bad-metadata", Attachments: []filemanager.IncomingAttachment{attachment("fail.txt", []byte("x"), nil)}})
			if err != nil || len(failed.Files) != 0 || len(failed.Failures) != 1 {
				t.Fatal("metadata failure acknowledged as success")
			}
			operations, err := f.st.ListFileOperations(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range operations {
				if op.Kind == "upload" && op.State == "failed" {
					if _, exists, _ := f.files.Stat(op.File.StorageKey); exists {
						t.Fatal("orphan object survived failure")
					}
				}
			}
			broken.Files = failedSave{f.files}
			broken.Store = f.st
			failed, err = broken.Prepare(ctx, filemanager.ChatInput{Scope: f.scope, MessageKey: "bad-object", Attachments: []filemanager.IncomingAttachment{attachment("fail.txt", []byte("x"), nil)}})
			if err != nil || len(failed.Files) != 0 || len(failed.Failures) != 1 {
				t.Fatal("object failure acknowledged")
			}
			var opens atomic.Int32
			oversize := attachment("large.txt", []byte("x"), &opens)
			oversize.DeclaredSize = filemanager.MaxChatUploadBytes + 1
			if _, err := f.service.Prepare(ctx, filemanager.ChatInput{Scope: f.scope, MessageKey: "too-big", Attachments: []filemanager.IncomingAttachment{oversize}}); err == nil || opens.Load() != 0 {
				t.Fatal("declared limit not enforced before download")
			}
			actual := attachment("large.txt", bytes.Repeat([]byte{'x'}, filemanager.MaxChatUploadBytes+1), nil)
			actual.DeclaredSize = 0
			p := prepare(t, f, "actual-too-big", actual)
			if len(p.Files) != 0 || len(p.Failures) != 1 {
				t.Fatal("actual limit bypassed")
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := f.service.Prepare(cancelled, filemanager.ChatInput{Scope: f.scope, MessageKey: "cancel", Attachments: []filemanager.IncomingAttachment{attachment("a", []byte("x"), &opens)}}); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation ignored")
			}
			id, err := f.st.NextUserFileID(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			pending := &model.FileOperation{UserID: "alice", Key: "stale", Kind: "upload", State: "pending", File: &model.UserFile{UserID: "alice", FileID: id, SessionID: f.scope.SessionID, StorageKey: "alice/stale", Details: model.FileDetails{IngestionKey: "stale"}}}
			if _, _, err := f.st.ClaimFileOperation(ctx, pending); err != nil {
				t.Fatal(err)
			}
			if _, err := f.files.Save("alice/stale", strings.NewReader("partial")); err != nil {
				t.Fatal(err)
			}
			if err := f.service.Recover(ctx, "alice", time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, exists, _ := f.files.Stat("alice/stale"); exists {
				t.Fatal("stale bytes not cleaned")
			}
			if ok, err := f.st.CompleteFileUpload(ctx, pending, 1, pending.File); err != nil || ok {
				t.Fatalf("stale producer published: %v %v", ok, err)
			}
		})
	}
}

func TestChatFilesDeliveryReceipts(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := fixture(t, backend)
			p := prepare(t, f, "source", attachment("a.txt", []byte("owned data"), nil))
			file := p.Files[0]
			ctx := context.Background()
			var calls atomic.Int32
			sender := filemanager.FileSenderFunc(func(ctx context.Context, user string, file *model.UserFile, r io.Reader) (filemanager.FileReceipt, error) {
				calls.Add(1)
				data, _ := io.ReadAll(r)
				if user != "alice" || string(data) != "owned data" {
					t.Error("wrong recipient/bytes")
				}
				return filemanager.FileReceipt{ID: "receipt-1"}, nil
			})
			for i := 0; i < 2; i++ {
				result, err := f.service.Send(ctx, f.scope, "same-delivery", file.FileID, sender)
				if err != nil || result.State != "sent" || result.Receipt != "receipt-1" {
					t.Fatalf("delivery: %+v %v", result, err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("duplicate transport delivery")
			}
			reopened := &filemanager.ChatService{Store: f.reopen(), Files: f.files}
			result, err := reopened.Send(ctx, f.scope, "same-delivery", file.FileID, sender)
			if err != nil || result.State != "sent" || calls.Load() != 1 {
				t.Fatal("receipt lost on restart")
			}
			for _, state := range []string{"failed", "unknown"} {
				var n int
				bad := filemanager.FileSenderFunc(func(context.Context, string, *model.UserFile, io.Reader) (filemanager.FileReceipt, error) {
					n++
					return filemanager.FileReceipt{}, &filemanager.DeliveryError{Code: "secret URL must not escape", Definite: state == "failed"}
				})
				for i := 0; i < 2; i++ {
					result, err := f.service.Send(ctx, f.scope, state, file.FileID, bad)
					if err != nil || result.State != state || strings.Contains(result.ErrorCode, "secret") {
						t.Fatal("incorrect failure classification")
					}
				}
				if n != 1 {
					t.Fatal("failed/unknown delivery retried automatically")
				}
			}
			runtime := &filemanager.Runtime{Service: f.service, Scope: f.scope, TurnID: "turn", Sender: sender}
			if _, err := runtime.Send(ctx, "bob", file.FileID); err == nil {
				t.Fatal("forged owner sent file")
			}
			if _, err := f.service.Send(ctx, f.scope, "foreign", "999999", sender); err == nil {
				t.Fatal("missing file delivered")
			}
			if _, err := runtime.Send(ctx, "alice", file.FileID); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.Send(ctx, "alice", file.FileID); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatal("same-turn delivery not deduplicated")
			}
		})
	}
}

func TestChatFilesPartialBatchCancellationAndArchivedScope(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			f := fixture(t, backend)
			ctx := context.Background()
			bad := filemanager.IncomingAttachment{Name: "bad.txt", Open: func(context.Context) (io.ReadCloser, error) { return nil, io.ErrUnexpectedEOF }}
			partial := prepare(t, f, "partial", attachment("ok.txt", []byte("safe"), nil), bad)
			if len(partial.Files) != 1 || len(partial.Failures) != 1 || !strings.Contains(partial.Text, "not confirmed as saved") {
				t.Fatal("partial batch not reported")
			}
			replay := prepare(t, f, "partial", bad)
			if !replay.Replay || len(replay.Files) != 1 || len(replay.Failures) != 1 {
				t.Fatal("partial receipt not durable")
			}
			unknown := prepare(t, f, "pdf", filemanager.IncomingAttachment{Name: "paper.pdf", MIMEType: "application/pdf", Open: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("%PDF-1.5\n")), nil
			}})
			if len(unknown.Files) != 1 || unknown.Files[0].Details.LineCount != nil {
				t.Fatal("PDF treated as plain text")
			}
			cancelled, cancel := context.WithCancel(ctx)
			interrupted := filemanager.IncomingAttachment{Name: "interrupted.txt", Open: func(context.Context) (io.ReadCloser, error) {
				cancel()
				return io.NopCloser(strings.NewReader("do not publish")), nil
			}}
			if _, err := f.service.Prepare(cancelled, filemanager.ChatInput{Scope: f.scope, MessageKey: "interrupted", Attachments: []filemanager.IncomingAttachment{interrupted}}); !errors.Is(err, context.Canceled) {
				t.Fatal("stream cancellation ignored")
			}
			if err := f.service.Recover(ctx, "alice", time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			after := prepare(t, f, "interrupted", interrupted)
			if !after.Replay || len(after.Files) != 0 || len(after.Failures) == 0 {
				t.Fatal("interrupted message not recoverable")
			}
			conversation, err := f.st.GetUserConversation("alice", f.scope.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			conversation.Archived = true
			if err := f.st.PutConversation(conversation); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			if _, err := f.service.Prepare(ctx, filemanager.ChatInput{Scope: f.scope, MessageKey: "archived", Attachments: []filemanager.IncomingAttachment{attachment("a", []byte("x"), &calls)}}); err == nil || calls.Load() != 0 {
				t.Fatal("archived scope downloaded bytes")
			}
		})
	}
}

// Simulate an accepted COMMIT whose response was lost, over the real backend.
type lostCommitReply struct {
	filemanager.ChatBackend
	hideLookup bool
}

func (f lostCommitReply) CompleteFileUpload(ctx context.Context, op *model.FileOperation, revision int64, file *model.UserFile) (bool, error) {
	ok, err := f.ChatBackend.CompleteFileUpload(ctx, op, revision, file)
	if err != nil || !ok {
		return ok, err
	}
	return false, io.ErrUnexpectedEOF
}
func (f lostCommitReply) GetUserFileForUser(user, id string) (*model.UserFile, error) {
	if f.hideLookup {
		return nil, io.ErrUnexpectedEOF
	}
	return f.ChatBackend.GetUserFileForUser(user, id)
}
func TestChatFilesLostCommitReplyNeverDeletesPublishedBytes(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		for _, hidden := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/lookup_hidden_%v", backend, hidden), func(t *testing.T) {
				f := fixture(t, backend)
				uncertain := &filemanager.ChatService{Store: lostCommitReply{ChatBackend: f.st, hideLookup: hidden}, Files: f.files}
				prepared, err := uncertain.Prepare(context.Background(), filemanager.ChatInput{Scope: f.scope, MessageKey: "lost-commit", Attachments: []filemanager.IncomingAttachment{attachment("safe.txt", []byte("durable bytes"), nil)}})
				if err != nil {
					t.Fatal(err)
				}
				rows, err := f.st.GetUserFilesByUser("alice")
				if err != nil || len(rows) != 1 {
					t.Fatal("accepted commit lost metadata")
				}
				reader, err := f.files.Open(rows[0].StorageKey)
				if err != nil {
					t.Fatal("accepted commit lost published bytes")
				}
				defer reader.Close()
				data, err := io.ReadAll(reader)
				if err != nil || string(data) != "durable bytes" {
					t.Fatal("published object corrupted")
				}
				if !hidden && (len(prepared.Files) != 1 || len(prepared.Failures) != 0) {
					t.Fatal("committed file not confirmed on fresh lookup")
				}
				if hidden && (len(prepared.Files) != 0 || len(prepared.Failures) != 1) {
					t.Fatal("unconfirmed commit misreported")
				}
			})
		}
	}
}
