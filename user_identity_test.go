package agentize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghiac/agentize/store"
)

func TestUpdateUserIdentityPersistsNameAndUsername(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)

	dbStore, err := store.NewDBStoreWithPath(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}

	if err := ag.UpdateUserIdentity("user-1", "Ali", "alice"); err != nil {
		t.Fatalf("UpdateUserIdentity: %v", err)
	}
	got, err := dbStore.GetUser("user-1")
	if err != nil || got == nil {
		t.Fatalf("GetUser: user=%v err=%v", got, err)
	}
	if got.Name != "Ali" || got.Username != "alice" {
		t.Fatalf("identity = %q / %q, want Ali / alice", got.Name, got.Username)
	}

	if err := ag.UpdateUserIdentity("user-1", "", ""); err != nil {
		t.Fatalf("empty update: %v", err)
	}
	got, _ = dbStore.GetUser("user-1")
	if got.Name != "Ali" || got.Username != "alice" {
		t.Fatalf("empty update wiped identity: %+v", got)
	}

	if err := ag.UpdateUserIdentity("user-1", "Ali", "alice2"); err != nil {
		t.Fatalf("username change: %v", err)
	}
	got, _ = dbStore.GetUser("user-1")
	if got.Username != "alice2" || got.Name != "Ali" {
		t.Fatalf("updated identity = %+v", got)
	}
}

func TestUpdateUserIdentityRequiresUserID(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)
	ag, err := NewWithOptions(knowledge, &Options{FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if err := ag.UpdateUserIdentity("  ", "Ali", "alice"); err == nil {
		t.Fatal("expected error for empty user id")
	}
}
