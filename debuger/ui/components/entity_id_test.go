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
	out := EntityIDLink("alice-conv-s0001", "/agentize/debug/sessions/alice-conv-s0001")
	if !strings.Contains(out, `href="/agentize/debug/sessions/alice-conv-s0001"`) {
		t.Fatalf("href must keep the stored id, got %s", out)
	}
	if !strings.Contains(out, ">1</code>") {
		t.Fatalf("label should be numeric, got %s", out)
	}
}
