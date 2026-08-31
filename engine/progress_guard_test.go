package engine

import (
	"fmt"
	"testing"
)

func TestProgressGuard_FirstMessage(t *testing.T) {
	pg := NewProgressGuard()
	queued := pg.TryQueue("u1", "hi")
	if queued {
		t.Error("first message should not be queued (no in-progress state)")
	}
}

func TestProgressGuard_QueueWhileInProgress(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("u1", true)

	queued := pg.TryQueue("u1", "msg1")
	if !queued {
		t.Fatal("expected message to be queued while in-progress")
	}

	queued2 := pg.TryQueue("u1", "msg2")
	if !queued2 {
		t.Fatal("expected second message to be queued too")
	}

	drained := pg.DrainQueue("u1")
	if len(drained) != 2 {
		t.Fatalf("expected 2 queued messages, got %d", len(drained))
	}
	if drained[0].Content != "msg1" || drained[1].Content != "msg2" {
		t.Errorf("unexpected queue contents: %v", queuedContents(drained))
	}

	drained2 := pg.DrainQueue("u1")
	if len(drained2) != 0 {
		t.Errorf("expected empty queue after drain, got %d", len(drained2))
	}
}

func TestProgressGuard_QueueCapEnforced(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("u1", true)

	over := maxQueuedPerKey + 5
	for i := 0; i < over; i++ {
		if !pg.TryQueue("u1", fmt.Sprintf("msg%d", i)) {
			t.Fatalf("TryQueue should stay true while in-progress (msg %d)", i)
		}
	}

	drained := pg.DrainQueue("u1")
	if len(drained) != maxQueuedPerKey {
		t.Fatalf("queue should be capped at %d, got %d", maxQueuedPerKey, len(drained))
	}
	if drained[0].Content != "msg0" || drained[maxQueuedPerKey-1].Content != fmt.Sprintf("msg%d", maxQueuedPerKey-1) {
		t.Errorf("cap should keep the oldest %d messages, got first=%q last=%q",
			maxQueuedPerKey, drained[0].Content, drained[len(drained)-1].Content)
	}
}

func TestProgressGuard_DoneReleasesKey(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("u1", true)

	queued := pg.TryQueue("u1", "queued-msg")
	if !queued {
		t.Fatal("expected queued while in-progress")
	}

	pg.SetInProgress("u1", false)

	queued2 := pg.TryQueue("u1", "new-msg")
	if queued2 {
		t.Error("after SetInProgress(false), TryQueue should return false")
	}
}

func TestProgressGuard_DeferredWaitsSeparateFromUser(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("s1", true)

	if !pg.TryQueue("s1", "user-1") {
		t.Fatal("user follow-up should queue")
	}
	if !pg.TryQueueMessage("s1", QueuedMessage{Content: "alert-1", Metadata: map[string]any{"kind": "alert"}}, QueueDeferred) {
		t.Fatal("alert should queue as deferred")
	}
	if !pg.TryQueueDeferred("s1", "schedule-1") {
		t.Fatal("schedule should queue as deferred")
	}

	users := pg.DrainQueue("s1")
	if len(users) != 1 || users[0].Content != "user-1" {
		t.Fatalf("user queue = %#v", users)
	}
	deferred := pg.DrainDeferred("s1")
	if len(deferred) != 2 || deferred[0].Content != "alert-1" || deferred[1].Content != "schedule-1" {
		t.Fatalf("deferred queue = %#v", deferred)
	}
	if got, _ := deferred[0].Metadata["kind"].(string); got != "alert" {
		t.Fatalf("deferred metadata = %#v", deferred[0].Metadata)
	}
}

func TestProgressGuard_TakePrefersUserThenDeferred(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("s1", true)
	_ = pg.TryQueue("s1", "u1")
	_ = pg.TryQueueDeferred("s1", "d1")

	got, ok := pg.TakeUser("s1")
	if !ok || got.Content != "u1" {
		t.Fatalf("TakeUser = %#v %v", got, ok)
	}
	if _, still := pg.TakeUser("s1"); still {
		t.Fatal("user queue should be empty")
	}
	got, ok = pg.TakeDeferred("s1")
	if !ok || got.Content != "d1" {
		t.Fatalf("TakeDeferred = %#v %v", got, ok)
	}
}
