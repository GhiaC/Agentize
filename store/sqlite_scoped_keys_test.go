package store

import (
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func TestSQLiteNumericIDsAreNotGlobalPrimaryKeys(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	alice := model.NewSessionWithType("alice", model.AgentTypeLow)
	alice.SessionID = "2"
	alice.Seq = 2
	bob := model.NewSessionWithType("bob", model.AgentTypeLow)
	bob.SessionID = "2"
	bob.Seq = 2
	if err := st.Put(alice); err != nil {
		t.Fatalf("Put alice session 2: %v", err)
	}
	if err := st.Put(bob); err != nil {
		t.Fatalf("Put bob session 2: %v", err)
	}
	if _, err := st.Get("2"); err == nil {
		t.Fatal("unscoped Get(2) must fail closed when two users share the number")
	}
	got, err := st.GetUserSession("alice", "2")
	if err != nil || got == nil || got.UserID != "alice" {
		t.Fatalf("GetUserSession(alice, 2) = %+v, err=%v", got, err)
	}
	got, err = st.GetUserSession("bob", "2")
	if err != nil || got == nil || got.UserID != "bob" {
		t.Fatalf("GetUserSession(bob, 2) = %+v, err=%v", got, err)
	}

	aliceMsg := &model.Message{
		MessageID: "1", SeqID: 1, UserID: "alice", SessionID: "2",
		Role: openai.ChatMessageRoleUser, Content: "from alice", CreatedAt: time.Now(),
	}
	bobMsg := &model.Message{
		MessageID: "1", SeqID: 1, UserID: "bob", SessionID: "2",
		Role: openai.ChatMessageRoleUser, Content: "from bob", CreatedAt: time.Now(),
	}
	if err := st.PutMessage(aliceMsg); err != nil {
		t.Fatalf("PutMessage alice: %v", err)
	}
	if err := st.PutMessage(bobMsg); err != nil {
		t.Fatalf("PutMessage bob: %v", err)
	}
	aliceItems, err := st.GetUserMessagesBySessionPage("alice", "2", 8, 0)
	if err != nil || len(aliceItems) != 1 || aliceItems[0].Content != "from alice" {
		t.Fatalf("alice messages = %+v, err=%v", aliceItems, err)
	}
	bobItems, err := st.GetUserMessagesBySessionPage("bob", "2", 8, 0)
	if err != nil || len(bobItems) != 1 || bobItems[0].Content != "from bob" {
		t.Fatalf("bob messages = %+v, err=%v", bobItems, err)
	}

	if err := st.DeleteUserSession("alice", "2"); err != nil {
		t.Fatalf("DeleteUserSession alice: %v", err)
	}
	if _, err := st.GetUserSession("alice", "2"); err == nil {
		t.Fatal("alice session 2 still present after owner-scoped delete")
	}
	if _, err := st.GetUserSession("bob", "2"); err != nil {
		t.Fatalf("bob session 2 was deleted by alice's delete: %v", err)
	}
}
