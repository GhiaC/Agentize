package agentize

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/gin-gonic/gin"
)

// TestUserDetailPage_CoreSystemPromptCard exercises the full route: with no live
// Core wired, Agentize installs the store-only preview provider, and the user
// detail page renders the new "Core System Prompt" card (collapsed, badged
// PREVIEW) alongside the now-collapsible secondary cards.
func TestUserDetailPage_CoreSystemPromptCard(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	dbStore, err := store.NewDBStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("failed to create db store: %v", err)
	}

	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("failed to create agentize: %v", err)
	}

	const userID = "user-1"
	user, err := dbStore.GetOrCreateUser(userID)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	user.ContextSummary = model.SummaryEntries{"prefers concise answers"}
	user.ContextTags = []string{"concise", "very-long-context-tag-that-must-wrap-on-mobile"}
	user.Name = "Ali"
	user.Username = "alice"
	user.ActiveConversationID = "user-1-c0001"
	if err := dbStore.PutUser(user); err != nil {
		t.Fatalf("save user: %v", err)
	}
	// Seed the active conversation. Its session—not the internal Core session—is
	// the source of Session Context in the prompt preview.
	if err := dbStore.Put(&model.Session{
		SessionID: "conv-s1",
		UserID:    userID,
		AgentType: model.AgentTypeConversation,
		Summary:   model.SummaryEntries{"remembers the user prefers Go"},
		Tags:      []string{"golang"},
	}); err != nil {
		t.Fatalf("seed conversation session: %v", err)
	}
	if err := dbStore.PutConversation(model.NewConversation(userID, "user-1-c0001", "conv-s1", "Go work", "test-model", 1)); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1") // no creds in test: register dashboard in dev mode
	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agentize/debug/users/"+userID, nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user detail status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		"Core System Prompt",            // the new card
		"PREVIEW",                       // no live Core wired → store preview
		"collapsible-card",              // the secondary cards are collapsible
		"collapsible-section",           // each prompt section is its own collapsible
		"Core Controller",               // a real prompt section rendered
		"Required",                      // section classification badges
		"Static",                        //   "
		"remembers the user prefers Go", // the seeded Core summary surfaced
		"prefers concise answers",       // persisted User Context surfaced
		"Conversations",                 // user-facing list replaces raw sessions
		"<details", // native collapsible markup
		"Durable facts that must stay true across conversations.",
		"Delete this fact?",
		"class=\"tag-badges\"", // tags wrap instead of overflowing mobile scroll
		">Ali<",                         // host display name
		">alice<",                       // host username
		">Delete all</button>",          // compact wipe control; details live in confirm
		"summarization logs",            // confirm lists logs + user context
		"user context (cross-conversation summary and tags)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("user detail page should contain %q", want)
		}
	}

	if strings.Contains(body, "Delete all user data (messages, sessions, quota, consumption, invoices)") {
		t.Error("delete button label should be short; the long explanation belongs in the confirm dialog")
	}

	for _, removed := range []string{"Core Agent (Brain)", "Opened Files", "Nonsense" + " Count", "Last " + "Nonsense"} {
		if strings.Contains(body, removed) {
			t.Errorf("removed user-page section leaked: %q", removed)
		}
	}
}

func TestUserContextFactDelete(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)
	dbStore, err := store.NewDBStoreWithPath(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	user, err := dbStore.GetOrCreateUser("user-1")
	if err != nil {
		t.Fatal(err)
	}
	user.ContextSummary = model.SummaryEntries{"keep this", "drop this"}
	user.ContextTags = []string{"keep-tag", "drop-tag"}
	if err := dbStore.PutUser(user); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/agentize/debug/users/user-1/context/summary/1/delete?confirm=user-1", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound && rec.Code != http.StatusOK {
		t.Fatalf("delete fact status = %d body=%s", rec.Code, rec.Body.String())
	}
	user, _ = dbStore.GetUser("user-1")
	if len(user.ContextSummary) != 1 || user.ContextSummary[0] != "keep this" {
		t.Fatalf("summary after delete = %#v", user.ContextSummary)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/agentize/debug/users/user-1/context/tag/delete?confirm=user-1", strings.NewReader("tag=drop-tag"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(rec, req)
	user, _ = dbStore.GetUser("user-1")
	if len(user.ContextTags) != 1 || user.ContextTags[0] != "keep-tag" {
		t.Fatalf("tags after delete = %#v", user.ContextTags)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/agentize/debug/users/user-1/context/tag/edit?confirm=user-1", strings.NewReader("old_tag=keep-tag&new_tag=renamed"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(rec, req)
	user, _ = dbStore.GetUser("user-1")
	if len(user.ContextTags) != 1 || user.ContextTags[0] != "renamed" {
		t.Fatalf("tags after edit = %#v", user.ContextTags)
	}
}
