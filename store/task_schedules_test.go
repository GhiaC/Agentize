package store

import (
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

func TestSQLiteTaskSchedulePersistence(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	testTaskSchedules(t, st)
}

func testTaskSchedules(t *testing.T, st Store) {
	now := time.Now().Truncate(time.Second)
	schedule := &model.TaskSchedule{
		ScheduleID: "sch-1", UserID: "user-1", SessionID: "session-1",
		Name: "Monitor", Prompt: "check", IntervalSeconds: 60,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.PutTaskSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	run := &model.TaskScheduleRun{
		RunID: "run-1", ScheduleID: schedule.ScheduleID,
		UserID: schedule.UserID, SessionID: schedule.SessionID,
		Status: model.TaskRunSucceeded, Output: "raw", Conclusion: "done",
		StartedAt: now, CompletedAt: now.Add(time.Second),
	}
	if err := st.PutTaskScheduleRun(run); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetTaskSchedule(schedule.ScheduleID)
	if err != nil || got == nil {
		t.Fatalf("get schedule: got=%#v err=%v", got, err)
	}
	if got.Name != schedule.Name || !got.NextRunAt.Equal(schedule.NextRunAt) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	list, err := st.ListTaskSchedules("user-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: len=%d err=%v", len(list), err)
	}
	if other, err := st.ListTaskSchedules("other"); err != nil || len(other) != 0 {
		t.Fatalf("owner filter: len=%d err=%v", len(other), err)
	}
	runs, err := st.ListTaskScheduleRuns(schedule.UserID, schedule.ScheduleID, 10)
	if err != nil || len(runs) != 1 || runs[0].Conclusion != "done" {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}

	if err := st.DeleteTaskSchedule(schedule.UserID, schedule.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetTaskSchedule(schedule.ScheduleID); err != nil || got != nil {
		t.Fatalf("schedule remained after delete: %#v err=%v", got, err)
	}
	runs, err = st.ListTaskScheduleRuns(schedule.UserID, schedule.ScheduleID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs remained after delete: %#v err=%v", runs, err)
	}
}

func testWorkflows(t *testing.T, st Store) {
	now := time.Now().Truncate(time.Second)
	workflow := &model.WorkflowRun{
		WorkflowID: "wf-1", UserID: "user-1", SessionID: "session-1",
		Name: "Release", Status: model.WorkflowRunning,
		Tasks: []*model.WorkflowTask{
			{
				ID: "draft", Name: "Draft", Tool: "update_status",
				Arguments: map[string]any{"message": "drafting"},
				Status:    model.WorkflowTaskSucceeded, Output: "draft", StartedAt: now, CompletedAt: now,
			},
			{
				ID: "publish", Name: "Publish", Tool: "update_status",
				Arguments: map[string]any{"message": "{{tasks.draft.output}}"},
				DependsOn: []string{"draft"}, Status: model.WorkflowTaskRunning, StartedAt: now,
			},
		},
		CreatedAt: now, UpdatedAt: now, StartedAt: now,
	}
	if err := st.PutWorkflowRun(workflow); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetWorkflowRun(workflow.WorkflowID)
	if err != nil || got == nil {
		t.Fatalf("get workflow: got=%#v err=%v", got, err)
	}
	if got.Name != workflow.Name || len(got.Tasks) != 2 || got.Tasks[1].DependsOn[0] != "draft" {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	got.Status = model.WorkflowSucceeded
	got.Tasks[1].Status = model.WorkflowTaskSucceeded
	got.Tasks[1].Output = "published"
	got.Tasks[1].CompletedAt = now.Add(time.Second)
	got.UpdatedAt = now.Add(time.Second)
	got.CompletedAt = now.Add(time.Second)
	if err := st.PutWorkflowRun(got); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListWorkflowRuns("user-1", 10)
	if err != nil || len(list) != 1 || list[0].Status != model.WorkflowSucceeded {
		t.Fatalf("list workflows: %#v err=%v", list, err)
	}
	if other, err := st.ListWorkflowRuns("other", 10); err != nil || len(other) != 0 {
		t.Fatalf("owner filter: len=%d err=%v", len(other), err)
	}
	if missing, err := st.GetWorkflowRun("missing"); err != nil || missing != nil {
		t.Fatalf("missing workflow: %#v err=%v", missing, err)
	}
}

func testTaskScheduleOwnerIsolation(t *testing.T, st Store) {
	now := time.Now().Truncate(time.Second)
	alice := &model.TaskSchedule{
		ScheduleID: "1", UserID: "alice", SessionID: "1",
		Name: "Alice", Prompt: "alice work", IntervalSeconds: 60,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	bob := &model.TaskSchedule{
		ScheduleID: "1", UserID: "bob", SessionID: "1",
		Name: "Bob", Prompt: "bob work", IntervalSeconds: 60,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(2 * time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.PutTaskSchedule(alice); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTaskSchedule(bob); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTaskScheduleRun(&model.TaskScheduleRun{
		RunID: "run-alice", ScheduleID: "1", UserID: "alice", SessionID: "1",
		Status: model.TaskRunSucceeded, Output: "alice-out", StartedAt: now, CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTaskScheduleRun(&model.TaskScheduleRun{
		RunID: "run-bob", ScheduleID: "1", UserID: "bob", SessionID: "1",
		Status: model.TaskRunSucceeded, Output: "bob-out", StartedAt: now, CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := st.GetTaskSchedule("1"); err == nil {
		t.Fatal("GetTaskSchedule with colliding numeric ids must fail closed")
	}

	aliceRuns, err := st.ListTaskScheduleRuns("alice", "1", 10)
	if err != nil || len(aliceRuns) != 1 || aliceRuns[0].Output != "alice-out" {
		t.Fatalf("alice runs=%#v err=%v", aliceRuns, err)
	}
	bobRuns, err := st.ListTaskScheduleRuns("bob", "1", 10)
	if err != nil || len(bobRuns) != 1 || bobRuns[0].Output != "bob-out" {
		t.Fatalf("bob runs=%#v err=%v", bobRuns, err)
	}

	if err := st.DeleteTaskSchedule("alice", "1"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetUserTaskSchedule("alice", "1"); err != nil || got != nil {
		t.Fatalf("alice schedule remained: %#v err=%v", got, err)
	}
	if got, err := st.GetUserTaskSchedule("bob", "1"); err != nil || got == nil || got.Name != "Bob" {
		t.Fatalf("bob schedule was deleted: %#v err=%v", got, err)
	}
	if runs, err := st.ListTaskScheduleRuns("bob", "1", 10); err != nil || len(runs) != 1 {
		t.Fatalf("bob runs after alice delete: %#v err=%v", runs, err)
	}
	if got, err := st.GetTaskSchedule("1"); err != nil || got == nil || got.UserID != "bob" {
		t.Fatalf("admin get after alice delete: %#v err=%v", got, err)
	}

	carolSess := model.NewSessionWithID("carol", "1", model.AgentTypeLow)
	daveSess := model.NewSessionWithID("dave", "1", model.AgentTypeLow)
	mustPutSession(t, st, carolSess)
	mustPutSession(t, st, daveSess)
	carolSched := &model.TaskSchedule{
		ScheduleID: "1", UserID: "carol", SessionID: "1",
		Name: "Carol", Prompt: "carol work", IntervalSeconds: 60,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	daveSched := &model.TaskSchedule{
		ScheduleID: "1", UserID: "dave", SessionID: "1",
		Name: "Dave", Prompt: "dave work", IntervalSeconds: 60,
		Status: model.TaskScheduleActive, NextRunAt: now.Add(time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.PutTaskSchedule(carolSched); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTaskSchedule(daveSched); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTaskScheduleRun(&model.TaskScheduleRun{
		RunID: "run-carol", ScheduleID: "1", UserID: "carol", SessionID: "1",
		Status: model.TaskRunSucceeded, StartedAt: now, CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTaskScheduleRun(&model.TaskScheduleRun{
		RunID: "run-dave", ScheduleID: "1", UserID: "dave", SessionID: "1",
		Status: model.TaskRunSucceeded, StartedAt: now, CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUserData("carol"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetUserTaskSchedule("dave", "1"); err != nil || got == nil {
		t.Fatalf("dave schedule removed by carol DeleteUserData: %#v err=%v", got, err)
	}
	if runs, err := st.ListTaskScheduleRuns("dave", "1", 10); err != nil || len(runs) != 1 {
		t.Fatalf("dave runs removed by carol DeleteUserData: %#v err=%v", runs, err)
	}
}

func testOpenedFileOwnerIsolation(t *testing.T, st Store) {
	alice := model.NewSessionWithID("alice", "1", model.AgentTypeLow)
	bob := model.NewSessionWithID("bob", "1", model.AgentTypeLow)
	mustPutSession(t, st, alice)
	mustPutSession(t, st, bob)

	aliceFile := model.NewOpenedFile(alice, "/secret.md", "secret")
	bobFile := model.NewOpenedFile(bob, "/public.md", "public")
	if err := st.AddOpenedFile(aliceFile); err != nil {
		t.Fatal(err)
	}
	if err := st.AddOpenedFile(bobFile); err != nil {
		t.Fatal(err)
	}

	aliceOpen, err := st.GetCurrentlyOpenedFilesBySession("alice", "1")
	if err != nil || len(aliceOpen) != 1 || aliceOpen[0].FilePath != "/secret.md" {
		t.Fatalf("alice open=%#v err=%v", aliceOpen, err)
	}
	bobOpen, err := st.GetCurrentlyOpenedFilesBySession("bob", "1")
	if err != nil || len(bobOpen) != 1 || bobOpen[0].FilePath != "/public.md" {
		t.Fatalf("bob open=%#v err=%v", bobOpen, err)
	}
	if err := st.CloseOpenedFile("alice", "1", "/secret.md"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.GetCurrentlyOpenedFilesBySession("alice", "1"); len(cur) != 0 {
		t.Fatalf("alice still open: %#v", cur)
	}
	if cur, _ := st.GetCurrentlyOpenedFilesBySession("bob", "1"); len(cur) != 1 {
		t.Fatalf("bob closed by alice: %#v", cur)
	}

	if err := st.DeleteUserData("alice"); err != nil {
		t.Fatal(err)
	}
	if files, err := st.GetUserOpenedFilesBySession("bob", "1"); err != nil || len(files) != 1 {
		t.Fatalf("bob opened files after alice delete: %#v err=%v", files, err)
	}
}
