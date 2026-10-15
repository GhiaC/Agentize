package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func TestLoadOwnedSessionRequiresUserAndSelectsOwner(t *testing.T) {
	st := newScopedLookupStore(t)
	e := &Engine{Sessions: st}

	if _, err := e.loadOwnedSession("", "5"); err == nil || !strings.Contains(err.Error(), "look up with user id") {
		t.Fatalf("unscoped load = %v", err)
	}
	got, err := e.loadOwnedSession("alice", "5")
	if err != nil || got == nil || got.Title != "alice-chat" {
		t.Fatalf("alice session = %+v, err=%v", got, err)
	}
	got, err = e.loadOwnedSession("bob", "5")
	if err != nil || got == nil || got.Title != "bob-chat" {
		t.Fatalf("bob session = %+v, err=%v", got, err)
	}
}

func TestScheduledExecuteBindsOwnerContext(t *testing.T) {
	st := newScopedLookupStore(t)
	e := &Engine{Sessions: st}
	var seenUser, seenTitle string
	scheduler := NewTaskScheduler(st, func(ctx context.Context, schedule *model.TaskSchedule) (string, error) {
		seenUser = model.UserIDFrom(ctx)
		session, err := e.loadOwnedSession(model.UserIDFrom(ctx), schedule.SessionID)
		if err != nil {
			return "", err
		}
		seenTitle = session.Title
		return "ok", nil
	}, nil)
	now := time.Now()
	schedule := &model.TaskSchedule{
		ScheduleID: "9", UserID: "alice", SessionID: "5",
		Name: "watch", Prompt: "check", IntervalSeconds: 3600,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(-time.Second),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.PutTaskSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	scheduler.execute(context.Background(), "alice", "9")
	if seenUser != "alice" || seenTitle != "alice-chat" {
		t.Fatalf("executor saw user=%q title=%q", seenUser, seenTitle)
	}
}

func TestNextSourceMessageDoesNotTouchOtherOwner(t *testing.T) {
	st := newScopedLookupStore(t)
	scheduler := NewTaskScheduler(st, func(context.Context, *model.TaskSchedule) (string, error) {
		return "", nil
	}, nil)

	if id, seq := scheduler.nextSourceMessage("", "5"); id != "" || seq != 0 {
		t.Fatalf("unscoped source message = %q %d", id, seq)
	}
	id, seq := scheduler.nextSourceMessage("alice", "5")
	if id == "" || seq != 4 {
		t.Fatalf("alice message id=%q seq=%d", id, seq)
	}
	bob, err := st.GetUserSession("bob", "5")
	if err != nil || bob.MessageSeq != 9 {
		t.Fatalf("bob seq = %d, err=%v", bob.MessageSeq, err)
	}
	alice, err := st.GetUserSession("alice", "5")
	if err != nil || alice.MessageSeq != 4 {
		t.Fatalf("alice seq = %d, err=%v", alice.MessageSeq, err)
	}
}

func newScopedLookupStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	alice := model.NewSessionWithID("alice", "5", model.AgentTypeConversation)
	alice.Title = "alice-chat"
	alice.MessageSeq = 3
	bob := model.NewSessionWithID("bob", "5", model.AgentTypeConversation)
	bob.Title = "bob-chat"
	bob.MessageSeq = 9
	if err := st.Put(alice); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(bob); err != nil {
		t.Fatal(err)
	}
	return st
}
