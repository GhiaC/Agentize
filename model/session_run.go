package model

import "time"

const (
	SessionRunQueued          = "queued"
	SessionRunRunning         = "running"
	SessionRunWaitingApproval = "waiting_approval"
	SessionRunSucceeded       = "succeeded"
	SessionRunFailed          = "failed"
	SessionRunCancelled       = "cancelled"
	SessionRunInterrupted     = "interrupted"
	SessionAdmitAccepted      = "accepted"
	SessionAdmitDuplicate     = "duplicate"
	SessionAdmitRejected      = "rejected"
	SessionQueueCapacity      = 20
)

// SessionRun is one admitted unit of work for a main session, in admission order.
type SessionRun struct {
	RunID          string
	UserID         string
	SessionID      string
	AdmissionSeq   int64
	OriginKind     string
	IdempotencyKey string
	Content        string
	Status         string
	Phase          string
	Version        int64
	Fence          int64
	Attempts       int64
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SessionRunInput is the producer request stored before execution starts.
type SessionRunInput struct {
	UserID         string
	SessionID      string
	OriginKind     string
	IdempotencyKey string
	Content        string
}

// SessionAdmitResult is the durable acceptance record. QueueFull is returned as
// ErrSessionQueueFull from the store, never as a successful ack.
type SessionAdmitResult struct {
	Run         SessionRun
	Outcome     string
	QueuedCount int
}

// SessionExecutionSnapshot is the session projection rebuilt from run rows.
type SessionExecutionSnapshot struct {
	UserID        string
	SessionID     string
	ActiveRunID   string
	Revision      int64
	Fence         int64
	QueuedCount   int
	NextAdmission int64
}

func SessionRunTerminal(status string) bool {
	switch status {
	case SessionRunSucceeded, SessionRunFailed, SessionRunCancelled, SessionRunInterrupted:
		return true
	default:
		return false
	}
}
