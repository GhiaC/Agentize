package components

import (
	"strings"
	"testing"
)

func TestEntityIDUsesNumericDisplay(t *testing.T) {
	out := EntityID("alice-c0001")
	if !strings.Contains(out, ">1</code>") {
		t.Fatalf("visible id should be numeric, got %s", out)
	}
	if strings.Contains(out, "alice-c0001") {
		t.Fatal("legacy concat must not appear in the visible label")
	}
}

func TestEntityIDLinkKeepsStoredHref(t *testing.T) {
	out := EntityIDLink("2", "/agentize/debug/users/alice/sessions/2")
	if !strings.Contains(out, `href="/agentize/debug/users/alice/sessions/2"`) {
		t.Fatalf("href must keep the stored id, got %s", out)
	}
	if !strings.Contains(out, ">2</code>") {
		t.Fatalf("label should be numeric, got %s", out)
	}
}
