package model

import "testing"

func TestNewScheduleMessageMetaKeepsFullResultOffTheSummary(t *testing.T) {
	meta := NewScheduleMessageMeta(&TaskSchedule{
		ScheduleID: "sch-1", Name: "4h review", Status: TaskScheduleActive,
		LastRunStatus: TaskRunSucceeded, LastConclusion: "BTC held the range with no policy breach.",
		RunCount: 9, IntervalSeconds: 14400, SourceConversationID: "alice-c0001",
		SourceSessionID: "alice-conversation-s0001",
	})
	if MessageMetaString(meta, "kind") != MessageMetaKindSchedule || MessageMetaString(meta, "widget") != MessageWidgetStatus {
		t.Fatalf("meta = %#v", meta)
	}
	if MessageMetaString(meta, "summary") != "succeeded" || MessageMetaString(meta, "title") != "4h review" {
		t.Fatalf("summary/title = %#v", meta)
	}
	body, _ := meta["schedule"].(map[string]any)
	if body == nil || body["last_conclusion"] != "BTC held the range with no policy breach." {
		t.Fatalf("schedule body = %#v", body)
	}
	source, _ := meta["source"].(map[string]any)
	if source == nil || source["kind"] != MessageMetaKindSchedule || source["id"] != "sch-1" || source["conversation_id"] != "alice-c0001" {
		t.Fatalf("schedule source = %#v", source)
	}
}

func TestHydrateUsageMetaRoundTrip(t *testing.T) {
	msg := &Message{CostCredits: 1.25, DurationMs: 840}
	msg.HydrateUsageMeta()
	if msg.Metadata[messageMetaCostCredits] != 1.25 {
		t.Fatalf("cost meta = %#v", msg.Metadata)
	}
	loaded := &Message{Metadata: msg.Metadata}
	loaded.HydrateUsageMeta()
	if loaded.CostCredits != 1.25 || loaded.DurationMs != 840 {
		t.Fatalf("hydrated = %+v", loaded)
	}
}

func TestCanonicalAgentTypeAndMessageKind(t *testing.T) {
	if CanonicalAgentType(AgentTypeLow) != AgentTypeCore || CanonicalAgentType(AgentTypeConversation) != AgentTypeCore {
		t.Fatal("deprecated tiers must display as core")
	}
	if CanonicalAgentType(AgentTypeSchedule) != AgentTypeSchedule || CanonicalAgentType(AgentTypeAlert) != AgentTypeAlert {
		t.Fatal("schedule and alert must stay distinct")
	}
	if AgentTypeForMessage(nil, AgentTypeUser) != AgentTypeCore {
		t.Fatal("regular chat messages are core")
	}
	if AgentTypeForMessage(NewScheduleMessageMeta(&TaskSchedule{ScheduleID: "1", Name: "n"}), AgentTypeCore) != AgentTypeSchedule {
		t.Fatal("schedule metadata must select schedule type")
	}
	if AgentTypeForMessage(NewAlertMessageMeta("a", "fired", "", nil), AgentTypeCore) != AgentTypeAlert {
		t.Fatal("alert metadata must select alert type")
	}
}

func TestSessionHasScheduleTag(t *testing.T) {
	if (&Session{}).HasScheduleTag() || (&Session{Tags: []string{"chat"}}).HasScheduleTag() {
		t.Fatal("untagged sessions are not schedule workers")
	}
	if !(&Session{Tags: []string{"schedule"}}).HasScheduleTag() || !(&Session{Tags: []string{"schedule:9"}}).HasScheduleTag() {
		t.Fatal("schedule tags must match")
	}
}

func TestNewAlertMessageMetaKeepsDetailOffTheSummary(t *testing.T) {
	meta := NewAlertMessageMeta("BTCUSDT 1h close", "fired", "Price crossed 74000", map[string]any{
		"symbol": "BTCUSDT", "interval": "1h", "rule_id": "rule-9", "conversation_id": "alice-c0001",
	})
	if MessageMetaString(meta, "kind") != MessageMetaKindAlert || MessageMetaString(meta, "title") != "BTCUSDT 1h close" {
		t.Fatalf("meta = %#v", meta)
	}
	body, _ := meta["alert"].(map[string]any)
	if body == nil || body["last_conclusion"] != "Price crossed 74000" || body["symbol"] != "BTCUSDT" {
		t.Fatalf("alert body = %#v", body)
	}
	if MessageMetaString(meta, "origin_id") != "rule-9" {
		t.Fatalf("alert origin_id = %#v", meta)
	}
	source, _ := meta["source"].(map[string]any)
	if source == nil || source["kind"] != MessageMetaKindAlert || source["id"] != "rule-9" || source["symbol"] != "BTCUSDT" {
		t.Fatalf("alert source = %#v", source)
	}
}
