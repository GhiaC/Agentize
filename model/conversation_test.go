package model

import "testing"

func TestGenerateConversationID(t *testing.T) {
	got := GenerateConversationID("alice", 1)
	if got != "alice-c0001" {
		t.Fatalf("id = %q, want alice-c0001", got)
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
