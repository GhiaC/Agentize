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
		t.Fatalf("append summary = %q", got)
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

func TestReplaceSummaryEntriesCapsAndDedupes(t *testing.T) {
	got := ReplaceSummaryEntries([]string{" Ali is 29. ", "ali is 29.", "joined TechCorp", ""})
	if len(got) != 2 || got[0] != "Ali is 29." || got[1] != "joined TechCorp" {
		t.Fatalf("replace = %#v", got)
	}
	overflow := make([]string, 0, MaxSummaryEntries+5)
	for i := 0; i < MaxSummaryEntries+5; i++ {
		overflow = append(overflow, strings.Repeat("f", i+1))
	}
	capped := ReplaceSummaryEntries(overflow)
	if len(capped) != MaxSummaryEntries {
		t.Fatalf("cap = %d, want %d", len(capped), MaxSummaryEntries)
	}
}

func TestAppendSummaryEntriesDropsOldestWhenOverCap(t *testing.T) {
	existing := make(SummaryEntries, 0, MaxSummaryEntries)
	for i := 0; i < MaxSummaryEntries; i++ {
		existing = append(existing, strings.Repeat("x", i+1))
	}
	got := AppendSummaryEntries(existing, "newest")
	if len(got) != MaxSummaryEntries {
		t.Fatalf("len = %d", len(got))
	}
	if got[len(got)-1] != "newest" || got[0] != "xx" {
		t.Fatalf("oldest should drop: first=%q last=%q", got[0], got[len(got)-1])
	}
}

func TestRemoveSummaryEntryAndEditTag(t *testing.T) {
	facts := RemoveSummaryEntry(SummaryEntries{"a", "b", "c"}, 1)
	if strings.Join(facts, "|") != "a|c" {
		t.Fatalf("remove = %v", facts)
	}
	tags := EditTag([]string{"master-watch", "old"}, "old", "ten-minute-alerts", MaxSessionTags)
	if strings.Join(tags, ",") != "master-watch,ten-minute-alerts" {
		t.Fatalf("edit tags = %v", tags)
	}
	if got := RemoveTag(tags, "Master-Watch"); strings.Join(got, ",") != "ten-minute-alerts" {
		t.Fatalf("remove tag = %v", got)
	}
}

func TestApplyDurableSummaryResponseReplacesOrKeeps(t *testing.T) {
	existing := SummaryEntries{"old fact"}
	got := applyDurableSummaryResponse(existing, `["Ali is 29.","Master Watch: 10-minute alerts."]`)
	if strings.Join(got, "|") != "Ali is 29.|Master Watch: 10-minute alerts." {
		t.Fatalf("replace = %v", got)
	}
	if kept := applyDurableSummaryResponse(existing, `[]`); strings.Join(kept, "|") != "old fact" {
		t.Fatalf("empty array should keep existing, got %v", kept)
	}
	if kept := applyDurableSummaryResponse(existing, "Persian speaker recap paragraph"); strings.Join(kept, "|") != "old fact" {
		t.Fatalf("non-array recap must not append, got %v", kept)
	}
}
