package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

func TestConversationFileMigrationPreservesLegacyRows(t *testing.T) {
	for _, backend := range []string{"postgres", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var st *SQLiteStore
			if backend == "postgres" {
				pg := openPostgreSQLTestStore(t)
				st = pg.SQLiteStore
			} else {
				sql, err := NewSQLiteStore(filepath.Join(t.TempDir(), "old.db"))
				if err != nil {
					t.Fatal(err)
				}
				st = sql
				t.Cleanup(func() { st.Close() })
			}
			old := &model.UserFile{UserID: "legacy-user", FileID: "000042", SessionID: "000007", Name: "legacy.txt", StorageKey: "legacy/object", Source: model.FileSourceUploaded, Size: 4, CreatedAt: time.Now()}
			if err := st.PutUserFile(old); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`ALTER TABLE user_files DROP COLUMN details`); err != nil {
				t.Fatal(err)
			}
			if backend == "postgres" {
				if _, err := st.db.Exec(`DELETE FROM schema_version WHERE version=7`); err != nil {
					t.Fatal(err)
				}
				if err := initPostgreSQLSchema(context.Background(), st.db); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := st.db.Exec(`DELETE FROM schema_version WHERE version=21`); err != nil {
					t.Fatal(err)
				}
				if err := st.runMigrations(); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := st.GetUserFileForUser(old.UserID, old.FileID)
			if err != nil || loaded == nil || loaded.StorageKey != old.StorageKey || loaded.Name != old.Name || loaded.Details.LineCount != nil {
				t.Fatal("migration changed old file or invented metadata")
			}
			id, err := st.NextUserFileID(context.Background(), old.UserID)
			if err != nil || id != model.FormatID(43) {
				t.Fatalf("migration counter seed=%q err=%v", id, err)
			}
		})
	}
}

func TestFileUploadFinalizationIsFencedAndAtomic(t *testing.T) {
	st := openPostgreSQLTestStore(t)
	ctx := context.Background()
	file := &model.UserFile{UserID: "owner", SessionID: "1", FileID: "1", Name: "chat.txt", StorageKey: "owner/object", CreatedAt: time.Now(), Details: model.FileDetails{IngestionKey: "operation"}}
	op := &model.FileOperation{UserID: "owner", Key: "operation", Kind: "upload", State: "pending", File: file}
	if _, _, err := st.ClaimFileOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CompleteFileUpload(ctx, op, 999, file); err != nil || ok {
		t.Fatal("stale upload committed")
	}
	if row, _ := st.GetUserFileForUser("owner", "1"); row != nil {
		t.Fatal("metadata published without reservation")
	}
	if ok, err := st.CompleteFileUpload(ctx, op, 1, file); err != nil || !ok {
		t.Fatalf("valid upload not finalized: %v %v", ok, err)
	}
	records, err := st.ListFileOperations(ctx, "owner")
	if err != nil || len(records) != 1 || records[0].State != "ready" {
		t.Fatal("metadata and operation diverged")
	}
	raw, err := json.Marshal(records[0].File.Details)
	if err != nil || string(raw) == "{}" {
		t.Fatal("provenance missing")
	}
}

func TestFileOperationBackendConformance(t *testing.T) {
	for _, backend := range conformanceBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			st := backend.newStore(t)
			defer st.Close()
			ctx := context.Background()
			first, err := st.NextUserFileID(ctx, "one")
			if err != nil {
				t.Fatal(err)
			}
			second, err := st.NextUserFileID(ctx, "one")
			if err != nil || second == first {
				t.Fatal("counter not atomic")
			}
			other, err := st.NextUserFileID(ctx, "two")
			if err != nil || other != first {
				t.Fatal("counter not owner scoped")
			}
			op := &model.FileOperation{UserID: "one", Key: "shared-key", Kind: "delivery", State: "sending"}
			claimed, record, err := st.ClaimFileOperation(ctx, op)
			if err != nil || !claimed || record.Revision != 1 {
				t.Fatal("cannot reserve operation")
			}
			replay := &model.FileOperation{UserID: "one", Key: "shared-key", Kind: "delivery", State: "sending"}
			if claimed, _, err := st.ClaimFileOperation(ctx, replay); err != nil || claimed {
				t.Fatal("duplicate reservation")
			}
			op.State = "sent"
			op.Receipt = "receipt"
			if ok, err := st.UpdateFileOperation(ctx, op, 1); err != nil || !ok {
				t.Fatal("cannot finalize delivery")
			}
			if ok, err := st.UpdateFileOperation(ctx, replay, 1); err != nil || ok {
				t.Fatal("stale delivery overwrote receipt")
			}
			separate := &model.FileOperation{UserID: "two", Key: "shared-key", Kind: "delivery", State: "sending"}
			if claimed, _, err := st.ClaimFileOperation(ctx, separate); err != nil || !claimed {
				t.Fatal("operation key leaked across owners")
			}
			records, err := st.ListFileOperations(ctx, "one")
			if err != nil || len(records) != 1 || records[0].State != "sent" || records[0].Receipt != "receipt" {
				t.Fatal("owner receipt not persisted")
			}
		})
	}
}
