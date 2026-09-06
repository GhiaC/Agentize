package core

import (
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

// putUserFile inserts a minimal uploaded user file directly into the store so the
// User Files prompt section and the cache behavior can be exercised.
func putUserFile(t *testing.T, st interface {
	PutUserFile(*model.UserFile) error
}, userID, fileID, name string) {
	t.Helper()
	uf := &model.UserFile{
		FileID:     fileID,
		UserID:     userID,
		SessionID:  userID + "-low-s0001",
		Name:       name,
		MIMEType:   "application/pdf",
		Size:       2048,
		StorageKey: userID + "/" + fileID + "-" + name,
		Source:     model.FileSourceUploaded,
		CreatedAt:  time.Now(),
	}
	if err := st.PutUserFile(uf); err != nil {
		t.Fatalf("PutUserFile: %v", err)
	}
}

func TestBuildUserFilesPrompt_ListsFiles(t *testing.T) {
	ch, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	if got := ch.buildUserFilesPrompt("u1"); got != "" {
		t.Fatalf("expected empty User Files section when user has no files, got: %q", got)
	}

	putUserFile(t, sqliteStore, "u1", "u1-low-s0001-uf0001", "report.pdf")

	got := ch.buildUserFilesPrompt("u1")
	if !strings.Contains(got, "# User Files") {
		t.Errorf("User Files section missing header; got: %q", got)
	}
	if !strings.Contains(got, "u1-low-s0001-uf0001") || !strings.Contains(got, "report.pdf") {
		t.Errorf("User Files section should list the file ID and name; got: %q", got)
	}
}

func TestGenerateSystemPrompt_CachesUntilInvalidated(t *testing.T) {
	ch, sqliteStore := newTestCoreHandler(t, []string{"researcher"})

	first, err := ch.generateSystemPrompt("u1", nil)
	if err != nil {
		t.Fatalf("generateSystemPrompt (first): %v", err)
	}
	if strings.Contains(strings.Join(first, " "), "prefers Go") {
		t.Fatal("did not expect user context before it was stored")
	}

	user, err := sqliteStore.GetOrCreateUser("u1")
	if err != nil {
		t.Fatal(err)
	}
	user.ContextSummary = model.SummaryEntries{"prefers Go"}
	if err := sqliteStore.PutUser(user); err != nil {
		t.Fatal(err)
	}
	cached, err := ch.generateSystemPrompt("u1", nil)
	if err != nil {
		t.Fatalf("generateSystemPrompt (cached): %v", err)
	}
	if strings.Contains(strings.Join(cached, " "), "prefers Go") {
		t.Error("cached system prompt should not reflect new user context until invalidated")
	}

	ch.invalidateSystemPrompt("u1")
	rebuilt, err := ch.generateSystemPrompt("u1", nil)
	if err != nil {
		t.Fatalf("generateSystemPrompt (rebuilt): %v", err)
	}
	if !strings.Contains(strings.Join(rebuilt, " "), "prefers Go") {
		t.Error("after invalidation the system prompt should include user context")
	}
}

func TestGenerateSystemPrompt_RebuildsWhenCoreSessionSummarized(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})

	cs := &model.Session{UserID: "u1", AgentType: model.AgentTypeCore, Summary: model.SummaryEntries{"old fact"}}
	ch.coreSessions["u1"] = cs

	first, err := ch.generateSystemPrompt("u1", cs)
	if err != nil {
		t.Fatalf("generateSystemPrompt (first): %v", err)
	}
	if !strings.Contains(strings.Join(first, " "), "old fact") {
		t.Fatal("expected initial session fact in the prompt")
	}

	cs.Summary = model.SummaryEntries{"updated fact"}
	cached, _ := ch.generateSystemPrompt("u1", cs)
	if strings.Contains(strings.Join(cached, " "), "updated fact") {
		t.Error("expected cached prompt while SummarizedAt is unchanged")
	}

	cs.SummarizedAt = time.Now().Add(time.Hour)
	rebuilt, _ := ch.generateSystemPrompt("u1", cs)
	if !strings.Contains(strings.Join(rebuilt, " "), "updated fact") {
		t.Error("a newer SummarizedAt should force a rebuild of the cached system prompt")
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1048576: "1.0 MB",
	}
	for in, want := range cases {
		if got := humanizeBytes(in); got != want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
