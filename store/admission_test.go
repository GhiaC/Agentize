package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestSessionAdmissionFIFOCapacityFenceAndOwners(t *testing.T) {
	st := openAdmissionStore(t)
	alice := model.SessionRunInput{UserID: "alice", SessionID: "1", OriginKind: "user", Content: "A", IdempotencyKey: "a"}
	first, err := st.AdmitSessionRun(alice)
	if err != nil || first.Outcome != model.SessionAdmitAccepted || first.Run.AdmissionSeq != 1 {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	again, err := st.AdmitSessionRun(alice)
	if err != nil || again.Outcome != model.SessionAdmitDuplicate || again.Run.RunID != first.Run.RunID {
		t.Fatalf("duplicate = %#v err=%v", again, err)
	}
	for _, content := range []string{"B", "C", "D"} {
		if _, err := st.AdmitSessionRun(model.SessionRunInput{UserID: "alice", SessionID: "1", OriginKind: "deferred", Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	bob, err := st.AdmitSessionRun(model.SessionRunInput{UserID: "bob", SessionID: "1", Content: "other"})
	if err != nil || bob.Run.RunID != "1" {
		t.Fatalf("bob shares alice sequence space: %#v err=%v", bob, err)
	}

	var order []string
	for {
		run, err := st.ClaimHeadSessionRun("alice", "1", "worker-a")
		if err != nil {
			t.Fatal(err)
		}
		if run == nil {
			break
		}
		order = append(order, run.Content)
		if err := st.FinalizeSessionRun("alice", "1", run.RunID, run.Fence, model.SessionRunSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.FinalizeSessionRun("alice", "1", run.RunID, run.Fence, model.SessionRunFailed, "stale"); !errors.Is(err, ErrSessionRunFence) && err != nil {
			t.Fatalf("terminal rerun err = %v", err)
		}
	}
	if got := join(order); got != "A B C D" {
		t.Fatalf("order = %s", got)
	}
	if err := st.FinalizeSessionRun("alice", "1", first.Run.RunID, 0, model.SessionRunSucceeded, ""); err != nil {
		t.Fatalf("repeat success = %v", err)
	}
	if err := st.FinalizeSessionRun("alice", "1", first.Run.RunID, 0, model.SessionRunFailed, "stale"); !errors.Is(err, ErrSessionRunFence) {
		t.Fatalf("conflicting finalize = %v", err)
	}

	for i := 0; i < model.SessionQueueCapacity; i++ {
		if _, err := st.AdmitSessionRun(model.SessionRunInput{UserID: "alice", SessionID: "1", Content: "q"}); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
	}
	if _, err := st.AdmitSessionRun(model.SessionRunInput{UserID: "alice", SessionID: "1", Content: "overflow"}); !errors.Is(err, ErrSessionQueueFull) {
		t.Fatalf("overflow err = %v", err)
	}
	snap, err := st.SnapshotSessionExecution("alice", "1")
	if err != nil || snap.QueuedCount != model.SessionQueueCapacity {
		t.Fatalf("snapshot = %#v err=%v", snap, err)
	}
}

func TestSessionAdmissionConcurrentIdempotency(t *testing.T) {
	st := openAdmissionStore(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := st.AdmitSessionRun(model.SessionRunInput{
				UserID: "alice", SessionID: "9", Content: "once", IdempotencyKey: "same",
			})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- got.Run.RunID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("idempotency split into %s and %s", first, id)
		}
	}
}

func openAdmissionStore(t *testing.T) Store {
	t.Helper()
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func join(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += " "
		}
		out += part
	}
	return out
}
