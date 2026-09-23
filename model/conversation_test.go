package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConversationRunStateRoundTrip(t *testing.T) {
	conv := NewConversation("alice", "alice-c0001", "alice-s0001", "plan", "gpt", 1)
	conv.RunState = &ConversationRunState{
		Phase: "tool_executing", Detail: "Price history", Active: true,
		UserMessageID: "alice-s0001-m0003", UpdatedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(conv)
	if err != nil {
		t.Fatal(err)
	}
	var got Conversation
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.RunState == nil || !got.RunState.Active || got.RunState.Phase != "tool_executing" || got.RunState.UserMessageID != "alice-s0001-m0003" {
		t.Fatalf("run state = %#v", got.RunState)
	}
}

func TestPreserveChosenTitleKeepsStoredName(t *testing.T) {
	chosenAt := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	stored := NewConversation("alice", "1", "1", "BTC support review", "m", 1)
	stored.TitleUpdatedAt = chosenAt

	stale := *stored
	stale.Title = "New market conversation"
	stale.TitleUpdatedAt = time.Time{}
	stale.RunState = &ConversationRunState{Phase: "thinking", Active: true}
	PreserveChosenTitle(&stale, stored)
	if stale.Title != "BTC support review" || !stale.TitleUpdatedAt.Equal(chosenAt) {
		t.Fatalf("stale persist clobbered title: %+v", stale)
	}
	if stale.RunState == nil || !stale.RunState.Active {
		t.Fatal("run state must still apply")
	}

	rename := *stored
	rename.SetChosenTitle("User name", chosenAt.Add(time.Minute))
	PreserveChosenTitle(&rename, stored)
	if rename.Title != "User name" {
		t.Fatalf("explicit later rename was blocked: %+v", rename)
	}

	automatic := *stored
	automatic.Title = "Updated Market Plan"
	automatic.TitleUpdatedAt = chosenAt.Add(time.Minute)
	PreserveChosenTitle(&automatic, stored)
	if automatic.Title != "BTC support review" || !automatic.TitleUpdatedAt.Equal(chosenAt) {
		t.Fatalf("automatic newer timestamp replaced a chosen title: %+v", automatic)
	}

	first := NewConversation("alice", "2", "2", "New market conversation", "", 2)
	generated := *first
	generated.Title = "Updated Market Plan"
	generated.TitleUpdatedAt = time.Now()
	PreserveChosenTitle(&generated, first)
	if generated.Title != "Updated Market Plan" {
		t.Fatalf("first generated title was dropped: %+v", generated)
	}
}

func TestPlanMissingTitlesLeavesTitlesThatAreAlreadySet(t *testing.T) {
	conversation := NewConversation("u", "1", "1", "ETH funding review", "", 1)
	session := &Session{Title: "BTC support review"}
	plan := PlanMissingTitles(session.Title, conversation)
	if plan.Generate || plan.WriteSession || plan.WriteConversation {
		t.Fatalf("set titles must not be rewritten: %+v", plan)
	}
	syncTitle, applied := ApplyMissingTitle(session, conversation, "Generated title")
	if session.Title != "BTC support review" || syncTitle != "" || applied != "" {
		t.Fatalf("apply rewrote a set title: session=%q sync=%q applied=%q", session.Title, syncTitle, applied)
	}

	untitled := NewConversation("u", "2", "2", "New market conversation", "", 2)
	session = &Session{Title: "BTC support review"}
	syncTitle, applied = ApplyMissingTitle(session, untitled, "Generated title")
	if session.Title != "BTC support review" || syncTitle != "BTC support review" || applied != "" {
		t.Fatalf("only the untitled conversation should be filled: session=%q sync=%q applied=%q", session.Title, syncTitle, applied)
	}

	session = &Session{}
	named := NewConversation("u", "3", "3", "ETH funding review", "", 3)
	syncTitle, applied = ApplyMissingTitle(session, named, "Generated title")
	if session.Title != "ETH funding review" || syncTitle != "" || applied != "" {
		t.Fatalf("only the untitled session should be filled: session=%q sync=%q applied=%q", session.Title, syncTitle, applied)
	}

	session = &Session{}
	syncTitle, applied = ApplyMissingTitle(session, untitled, "Market plan")
	if session.Title != "Market plan" || syncTitle != "Market plan" || applied != "Market plan" {
		t.Fatalf("both empty titles should be created: session=%q sync=%q applied=%q", session.Title, syncTitle, applied)
	}

	locked := NewConversation("u", "4", "4", "Untitled", "", 4)
	locked.TitleUpdatedAt = time.Now()
	session = &Session{Title: "BTC support review"}
	plan = PlanMissingTitles(session.Title, locked)
	if plan.Generate || plan.WriteSession || plan.WriteConversation {
		t.Fatalf("a chosen conversation title must block generation: %+v", plan)
	}
	syncTitle, applied = ApplyMissingTitle(session, locked, "Generated title")
	if session.Title != "BTC support review" || syncTitle != "" || applied != "" {
		t.Fatalf("chosen placeholder replaced a session title: session=%q sync=%q", session.Title, syncTitle)
	}

	session = &Session{Title: "New market conversation"}
	plan = PlanMissingTitles(session.Title, nil)
	if plan.Generate || plan.WriteSession || plan.WriteConversation {
		t.Fatalf("a missing conversation must not be given a generated title: %+v", plan)
	}

	again := NewConversation("u", "5", "5", "ETH funding review", "", 5)
	session = &Session{Title: "New market conversation"}
	syncTitle, applied = ApplyMissingTitle(session, again, "Generated again")
	if session.Title != "ETH funding review" || syncTitle != "" || applied != "" {
		t.Fatalf("wiped session title caused another conversation name: session=%q sync=%q applied=%q", session.Title, syncTitle, applied)
	}

	for _, title := range []string{"TTTTTTTTT", "Not Change", "Title: Momentum Scan Enhancements"} {
		if TitleRequestAllowed(title, nil) {
			t.Fatalf("named session %q must not request a title without a conversation", title)
		}
		untitled := NewConversation("u", "6", "6", "New market conversation", "", 6)
		if TitleRequestAllowed(title, untitled) {
			t.Fatalf("named session %q must not request a title", title)
		}
		named := NewConversation("u", "7", "7", title, "", 7)
		if TitleRequestAllowed("", named) || TitleRequestAllowed("New market conversation", named) {
			t.Fatalf("named conversation %q must not request a title", title)
		}
	}
}

func TestGenerateConversationID(t *testing.T) {
	got := GenerateConversationID("alice", 1)
	if got != "1" {
		t.Fatalf("id = %q, want 1", got)
	}
	if got != GenerateConversationID("alice", 1) {
		t.Fatal("id must be deterministic")
	}
	if GenerateConversationID("alice", 2) == got {
		t.Fatal("seq must change the id")
	}
}

func TestGenerateConversationID_NoSlug(t *testing.T) {
	id := GenerateConversationID("alice", 1)
	for _, banned := range []string{"btc", "plan", "low", "high", "core"} {
		if id == "alice-c-"+banned+"-s0001" {
			t.Fatalf("id unexpectedly used slug form involving %s: %s", banned, id)
		}
	}
}

func TestSessionSubAgentRules(t *testing.T) {
	main := NewSessionWithID("u", "u-conv-s0001", AgentTypeConversation)
	if main.IsSubAgent() || !main.CanCreateSubAgent() {
		t.Fatalf("main conv session: IsSubAgent=%v CanCreate=%v", main.IsSubAgent(), main.CanCreateSubAgent())
	}
	child := NewSessionWithID("u", "u-sub-s0001", AgentTypeSub)
	child.ParentSessionID = main.SessionID
	if !child.IsSubAgent() {
		t.Fatal("child should be a sub-agent")
	}
	if child.CanCreateSubAgent() {
		t.Fatal("sub-agent must not create further sub-agents")
	}
	low := NewSessionWithID("u", "u-low-s0001", AgentTypeLow)
	if low.CanCreateSubAgent() {
		t.Fatal("legacy low sessions must not create conversation sub-agents")
	}
}
