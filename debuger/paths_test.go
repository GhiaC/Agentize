package debuger

import "testing"

func TestSessionPathIncludesUser(t *testing.T) {
	got := SessionPath("alice", "2")
	want := "/agentize/debug/users/alice/sessions/2"
	if got != want {
		t.Fatalf("SessionPath = %q, want %q", got, want)
	}
	if ToolCallPath("alice", "2", "1") != "/agentize/debug/users/alice/sessions/2/tool-calls/1" {
		t.Fatalf("ToolCallPath = %q", ToolCallPath("alice", "2", "1"))
	}
	if WorkflowPath("bob", "3") != "/agentize/debug/users/bob/workflows/3" {
		t.Fatalf("WorkflowPath = %q", WorkflowPath("bob", "3"))
	}
	if RoutePath("alice", "2", "4") != "/agentize/debug/users/alice/sessions/2/routes/4" {
		t.Fatalf("RoutePath = %q", RoutePath("alice", "2", "4"))
	}
}
