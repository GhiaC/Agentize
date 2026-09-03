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
	user.ContextTags = []string{"concise"}
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
		"<details",                      // native collapsible markup
		"Cross-conversation facts",      // dedicated User Context card
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
