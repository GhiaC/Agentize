package engine

import (
	"context"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func TestDrainSessionQueuesLeavesDeferredOnCancel(t *testing.T) {
	e := &Engine{sessionProgress: NewProgressGuard()}
	e.sessionProgress.SetInProgress("sess", true)
	if !e.sessionProgress.TryQueueDeferred("sess", "alert fired") {
		t.Fatal("deferred message should queue while busy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.drainSessionQueues(ctx, "sess", "sess")
	item, ok := e.sessionProgress.TakeDeferred("sess")
	if !ok || item.Content != "alert fired" {
		t.Fatalf("cancelled drain must leave deferred queued, got %#v ok=%v", item, ok)
	}
}

func TestMirrorRunAuditToSourceCopiesDAGAndTools(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	source := model.NewSessionWithID("user-1", "1", model.AgentTypeConversation)
	worker := model.NewSessionWithID("user-1", "2", model.AgentTypeCore)
	worker.Tags = []string{"schedule", "schedule:9"}
	if err := st.Put(source); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(worker); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.PutRouteTrace(&model.RouteTrace{
		TraceID: "1", SessionID: worker.SessionID, UserID: "user-1",
		UserMessageID: "1", Kind: "turn", Status: "ok", CreatedAt: now,
		Nodes: []model.RouteNode{{ID: "n1", Type: model.RouteNodeToolCall, Label: "get_account_info", ToolID: "1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutToolCall(&model.ToolCall{
		ToolID: "1", ToolCallID: "call-1", UserID: "user-1", SessionID: worker.SessionID,
		UserMessageID: "1", FunctionName: "get_account_info", Status: model.ToolCallStatusSuccess,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	scheduler := NewTaskScheduler(st, func(context.Context, *model.TaskSchedule) (string, error) {
		return "ok", nil
	}, nil)
	sourceUser := &model.Message{MessageID: "3", SessionID: source.SessionID, UserID: "user-1"}
	scheduler.mirrorRunAuditToSource(&model.TaskSchedule{
		UserID: "user-1", SessionID: worker.SessionID, SourceSessionID: source.SessionID, ScheduleID: "9",
	}, sourceUser)

	traces, err := st.GetUserRouteTracesBySession("user-1", source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || traces[0].UserMessageID != "3" || traces[0].Kind != "turn" {
		t.Fatalf("source traces = %#v", traces)
	}
	if len(traces[0].Nodes) != 1 || traces[0].Nodes[0].ToolID != "1" {
		t.Fatalf("copied DAG nodes = %#v", traces[0].Nodes)
	}
	tools, err := st.GetUserToolCallsBySession("user-1", source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].UserMessageID != "3" || tools[0].AgentType != model.AgentTypeSchedule {
		t.Fatalf("copied tools = %#v", tools)
	}
}
