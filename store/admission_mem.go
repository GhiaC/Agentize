package store

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
)

// admissionMem is the FIFO admission book for backends that do not have the
// SQL tables. PostgreSQL and SQLite use store/admission.go instead.
type admissionMem struct {
	mu   sync.Mutex
	exec map[string]*memExecution
}

type memExecution struct {
	next    int64
	active  string
	lease   string
	expires time.Time
	fence   int64
	rev     int64
	runs    []*model.SessionRun
}

func newAdmissionMem() *admissionMem {
	return &admissionMem{exec: map[string]*memExecution{}}
}

func memKey(userID, sessionID string) string { return userID + "\x00" + sessionID }

func (b *admissionMem) AdmitSessionRun(in model.SessionRunInput) (model.SessionAdmitResult, error) {
	in.UserID = strings.TrimSpace(in.UserID)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.Content = strings.TrimSpace(in.Content)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.UserID == "" || in.SessionID == "" || in.Content == "" {
		return model.SessionAdmitResult{}, fmt.Errorf("session admission requires owner, session, and content")
	}
	if in.OriginKind == "" {
		in.OriginKind = "user"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ex := b.execution(in.UserID, in.SessionID)
	if in.IdempotencyKey != "" {
		for _, run := range ex.runs {
			if run.IdempotencyKey == in.IdempotencyKey {
				return model.SessionAdmitResult{Run: *run, Outcome: model.SessionAdmitDuplicate, QueuedCount: memOpen(ex)}, nil
			}
		}
	}
	if memOpen(ex) >= model.SessionQueueCapacity {
		return model.SessionAdmitResult{}, ErrSessionQueueFull
	}
	now := time.Now()
	run := &model.SessionRun{
		RunID: fmt.Sprintf("%d", ex.next), UserID: in.UserID, SessionID: in.SessionID,
		AdmissionSeq: ex.next, OriginKind: in.OriginKind, IdempotencyKey: in.IdempotencyKey,
		Content: in.Content, Status: model.SessionRunQueued, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	ex.next++
	ex.rev++
	ex.runs = append(ex.runs, run)
	return model.SessionAdmitResult{Run: *run, Outcome: model.SessionAdmitAccepted, QueuedCount: memOpen(ex) - 1}, nil
}

func (b *admissionMem) ClaimHeadSessionRun(userID, sessionID, workerID string) (*model.SessionRun, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(workerID) == "" {
		return nil, fmt.Errorf("session claim requires owner, session, and worker")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ex := b.execution(userID, sessionID)
	now := time.Now()
	if ex.active != "" && ex.expires.After(now) {
		return nil, nil
	}
	if ex.active != "" && !ex.expires.After(now) {
		for _, run := range ex.runs {
			if run.RunID == ex.active && run.Status == model.SessionRunRunning {
				run.Status = model.SessionRunInterrupted
				run.Error = "lease expired"
				run.UpdatedAt = now
				run.Version++
			}
		}
		ex.active = ""
		ex.lease = ""
		ex.rev++
	}
	var head *model.SessionRun
	for _, run := range ex.runs {
		if run.Status == model.SessionRunQueued && (head == nil || run.AdmissionSeq < head.AdmissionSeq) {
			head = run
		}
	}
	if head == nil {
		return nil, nil
	}
	ex.fence++
	head.Status = model.SessionRunRunning
	head.Fence = ex.fence
	head.Attempts++
	head.UpdatedAt = now
	head.Version++
	ex.active = head.RunID
	ex.lease = workerID
	ex.expires = now.Add(sessionRunLease)
	ex.rev++
	copy := *head
	return &copy, nil
}

func (b *admissionMem) HeartbeatSessionRun(userID, sessionID, runID, workerID string, fence int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ex := b.execution(userID, sessionID)
	if ex.active != runID || ex.lease != workerID || ex.fence != fence {
		return ErrSessionRunFence
	}
	ex.expires = time.Now().Add(sessionRunLease)
	return nil
}

func (b *admissionMem) FinalizeSessionRun(userID, sessionID, runID string, fence int64, status, errText string) error {
	if !model.SessionRunTerminal(status) {
		return fmt.Errorf("session finalize requires a terminal status")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ex := b.execution(userID, sessionID)
	for _, run := range ex.runs {
		if run.RunID != runID {
			continue
		}
		if model.SessionRunTerminal(run.Status) {
			if run.Status == status {
				return nil
			}
			return ErrSessionRunFence
		}
		if run.Fence != fence {
			return ErrSessionRunFence
		}
		run.Status = status
		run.Error = strings.TrimSpace(errText)
		run.UpdatedAt = time.Now()
		run.Version++
		if ex.active == runID {
			ex.active = ""
			ex.lease = ""
			ex.expires = time.Time{}
		}
		ex.rev++
		return nil
	}
	return fmt.Errorf("session run not found")
}

func (b *admissionMem) GetSessionRun(userID, sessionID, runID string) (*model.SessionRun, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ex := b.execution(userID, sessionID)
	for _, run := range ex.runs {
		if run.RunID == runID {
			copy := *run
			return &copy, nil
		}
	}
	return nil, nil
}

func (b *admissionMem) SnapshotSessionExecution(userID, sessionID string) (model.SessionExecutionSnapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ex := b.execution(userID, sessionID)
	return model.SessionExecutionSnapshot{
		UserID: userID, SessionID: sessionID, ActiveRunID: ex.active,
		Revision: ex.rev, Fence: ex.fence, QueuedCount: memOpen(ex), NextAdmission: ex.next,
	}, nil
}

func (b *admissionMem) execution(userID, sessionID string) *memExecution {
	key := memKey(userID, sessionID)
	ex := b.exec[key]
	if ex == nil {
		ex = &memExecution{next: 1}
		b.exec[key] = ex
	}
	return ex
}

func memOpen(ex *memExecution) int {
	n := 0
	for _, run := range ex.runs {
		if !model.SessionRunTerminal(run.Status) {
			n++
		}
	}
	return n
}
