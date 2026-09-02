package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryEntriesLoadsLegacyScalarAndPersistsArray(t *testing.T) {
	var session Session
	if err := json.Unmarshal([]byte(`{"UserID":"u1","SessionID":"s1","Summary":"legacy fact"}`), &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Summary) != 1 || session.Summary[0] != "legacy fact" {
		t.Fatalf("legacy summary = %#v", session.Summary)
	}
	session.Summary = AppendSummaryEntries(session.Summary, "new fact", "legacy fact", " ")
	if got := strings.Join(session.Summary, "|"); got != "legacy fact|new fact" {
		t.Fatalf("append-only summary = %q", got)
	}
	encoded, err := json.Marshal(&session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"Summary":["legacy fact","new fact"]`) {
		t.Fatalf("summary did not persist as array: %s", encoded)
	}
}

func TestSummaryEntriesNilPersistenceUsesEmptyArray(t *testing.T) {
	encoded, err := json.Marshal(struct {
		Summary SummaryEntries `json:"Summary"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"Summary":[]}` {
		t.Fatalf("nil summary must retain the array schema, got %s", encoded)
	}
}
