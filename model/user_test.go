package model

import "testing"

func TestDisplayLabelPrefersNameAndUsername(t *testing.T) {
	if got := (&User{UserID: "12345678", Name: "Ada", Username: "ada"}).DisplayLabel(); got != "Ada (@ada)" {
		t.Fatalf("label = %q", got)
	}
	if got := (&User{UserID: "12345678", Username: "ada"}).DisplayLabel(); got != "@ada" {
		t.Fatalf("username label = %q", got)
	}
	if got := (&User{UserID: "12345678"}).DisplayLabel(); got != "12345678" {
		t.Fatalf("id fallback = %q", got)
	}
}

func TestResetAfterDataDelete(t *testing.T) {
	u := NewUser("user-1")
	u.ContextSummary = SummaryEntries{"durable fact"}
	u.ContextTags = []string{"tag"}
	u.ActiveConversationID = "1"
	u.SessionSeq = 4
	u.FileSeq = 2
	u.WorkflowSeq = 3
	u.ScheduleSeq = 5
	u.ReviewSeq = 6
	u.SetActiveSessionID(AgentTypeLow, "1")
	u.Ban(0, "blocked")

	u.ResetAfterDataDelete()

	if u.UserID != "user-1" {
		t.Fatalf("user id must be kept, got %q", u.UserID)
	}
	if u.IsCurrentlyBanned() {
		t.Fatal("user should be unbanned")
	}
	if len(u.ContextSummary) != 0 || len(u.ContextTags) != 0 {
		t.Fatalf("context must be empty: summary=%v tags=%v", u.ContextSummary, u.ContextTags)
	}
	if u.ActiveConversationID != "" || u.GetActiveSessionID(AgentTypeLow) != "" {
		t.Fatalf("session pointers must be cleared: conv=%q session=%q", u.ActiveConversationID, u.GetActiveSessionID(AgentTypeLow))
	}
	if u.SessionSeq != 0 || u.FileSeq != 0 || u.WorkflowSeq != 0 || u.ScheduleSeq != 0 || u.ReviewSeq != 0 {
		t.Fatalf("counters must be zero: %+v", u)
	}
}
