package model

import "testing"

type conversationLookupFake struct {
	owned    map[string]*Conversation
	unscoped int
}

func (f *conversationLookupFake) Get(string) (*Session, error) { return nil, nil }
func (f *conversationLookupFake) Put(*Session) error           { return nil }
func (f *conversationLookupFake) Delete(string) error          { return nil }
func (f *conversationLookupFake) DeleteUserSession(string, string) error {
	return nil
}
func (f *conversationLookupFake) List(string) ([]*Session, error) { return nil, nil }
func (f *conversationLookupFake) GetNextSessionSeq(string, AgentType) (int, error) {
	return 1, nil
}
func (f *conversationLookupFake) GetUserSession(string, string) (*Session, error) {
	return nil, nil
}
func (f *conversationLookupFake) GetConversationBySession(string) (*Conversation, error) {
	f.unscoped++
	return &Conversation{UserID: "bob", ConversationID: "5", SessionID: "5", Title: "bob"}, nil
}
func (f *conversationLookupFake) GetUserConversationBySession(userID, sessionID string) (*Conversation, error) {
	return f.owned[userID+"/"+sessionID], nil
}
func (f *conversationLookupFake) PutConversation(*Conversation) error { return nil }

func TestConversationLookupDoesNotFallBackToAnotherOwner(t *testing.T) {
	fake := &conversationLookupFake{owned: map[string]*Conversation{
		"alice/5": {UserID: "alice", ConversationID: "5", SessionID: "5", Title: "alice"},
	}}
	got := conversationForSession(fake, "alice", "5")
	if got == nil || got.Title != "alice" || fake.unscoped != 0 {
		t.Fatalf("alice conversation = %+v unscoped=%d", got, fake.unscoped)
	}
	if conversationForSession(fake, "", "5") != nil || fake.unscoped != 0 {
		t.Fatalf("empty owner used unscoped lookup (%d)", fake.unscoped)
	}
	if conversationForSession(fake, "carol", "5") != nil || fake.unscoped != 0 {
		t.Fatalf("missing owner fell back to another user (%d)", fake.unscoped)
	}
}
