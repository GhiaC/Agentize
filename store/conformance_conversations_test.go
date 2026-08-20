package store

import (
	"errors"
	"testing"

	"github.com/ghiac/agentize/model"
)

func testConversations(t *testing.T, st Store) {
	if err := st.PutConversation(&model.Conversation{ConversationID: "x-c0001"}); !errors.Is(err, ErrValidation) {
		t.Errorf("PutConversation missing fields: got %v, want ErrValidation", err)
	}

	session := newSession("alice", model.AgentTypeConversation)
	mustPutSession(t, st, session)
	conv := model.NewConversation("alice", "alice-c0001", session.SessionID, "plan", "m1", 1)
	if err := st.PutConversation(conv); err != nil {
		t.Fatalf("PutConversation: %v", err)
	}
	got, err := st.GetConversation("alice-c0001")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Title != "plan" || got.SessionID != session.SessionID || got.Model != "m1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	bySession, err := st.GetConversationBySession(session.SessionID)
	if err != nil || bySession == nil || bySession.ConversationID != "alice-c0001" {
		t.Fatalf("GetConversationBySession: %v %+v", err, bySession)
	}
	if seq, err := st.GetNextConversationSeq("alice"); err != nil || seq != 2 {
		t.Fatalf("GetNextConversationSeq = %d %v, want 2", seq, err)
	}

	secondSession := model.NewSessionWithID("alice", "alice-conv-s0002", model.AgentTypeConversation)
	mustPutSession(t, st, secondSession)
	second := model.NewConversation("alice", "alice-c0002", secondSession.SessionID, "later", "m2", 2)
	if err := st.PutConversation(second); err != nil {
		t.Fatalf("PutConversation 2: %v", err)
	}
	if err := st.TouchConversationBySession(session.SessionID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	list, err := st.ListConversations("alice")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListConversations: %d %v", len(list), err)
	}
	if list[0].ConversationID != "alice-c0001" {
		t.Fatalf("last used first, got %s", list[0].ConversationID)
	}
	all, err := st.ListAllConversations()
	if err != nil || len(all) < 2 {
		t.Fatalf("ListAllConversations: %d %v", len(all), err)
	}
	if err := st.DeleteConversation("alice-c0001"); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := st.GetConversation("alice-c0001"); err == nil {
		t.Fatal("deleted conversation still gettable")
	}
}
