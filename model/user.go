package model

import (
	"strings"
	"time"
)

// User represents a user in the system
type User struct {
	// UserID is the unique identifier for the user.
	// Agentize-generated ids are random 8-digit numbers. Hosts may still supply
	// their own id (for example a Telegram user id); that value is kept as-is.
	UserID string

	// User information (optional)
	Name     string // User's display name (optional)
	Username string // User's username (optional)

	// Ban status
	IsBanned   bool      // Whether the user is currently banned
	BanUntil   time.Time // When the ban expires (zero time means permanent ban)
	BanMessage string    // Message to show to banned users

	// Active session IDs per agent type
	// Key: AgentType (core, high, low), Value: SessionID
	// This is persisted to database and loaded on startup
	ActiveSessionIDs map[AgentType]string

	// Active conversation for ChatBot Core tools (list/select/send).
	// Distinct from ActiveSessionIDs which is keyed by agent type.
	ActiveConversationID string

	// Cross-conversation memory. Session-specific title/summary/tags remain on
	// Session; these fields contain only durable facts useful to future sessions.
	ContextSummary SummaryEntries
	ContextTags    []string

	// SessionSeq is the per-user increment for SessionID (all agent types share
	// one counter). SessionSeqs is the deprecated per-agent-type map; new code
	// must not write it. Planned for removal with the concat ID helpers.
	SessionSeq int
	// Deprecated: per-agent-type session counters used by concatenated session
	// ids ({UserID}-{AgentType}-s{seq}). Use SessionSeq.
	SessionSeqs map[AgentType]int

	// FileSeq is the per-user increment for UserFile and OpenedFile ids.
	FileSeq int
	// WorkflowSeq is the per-user increment for WorkflowRun ids.
	WorkflowSeq int
	// ScheduleSeq is the per-user increment for TaskSchedule ids.
	ScheduleSeq int
	// ReviewSeq is the per-user increment for ReviewRequest ids.
	ReviewSeq int

	// Metadata
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewUser creates a new user. An empty userID is filled with a random 8-digit
// Agentize user id. A host-supplied id is stored unchanged.
func NewUser(userID string) *User {
	now := time.Now()
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = GenerateUserID()
	}
	return &User{
		UserID:           userID,
		IsBanned:         false,
		BanUntil:         time.Time{},
		BanMessage:       "",
		ActiveSessionIDs: make(map[AgentType]string),
		SessionSeqs:      make(map[AgentType]int),
		ContextSummary:   SummaryEntries{},
		ContextTags:      []string{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// IsCurrentlyBanned checks if the user is currently banned
func (u *User) IsCurrentlyBanned() bool {
	if !u.IsBanned {
		return false
	}
	// If BanUntil is zero, it's a permanent ban
	if u.BanUntil.IsZero() {
		return true
	}
	// Check if ban has expired
	return time.Now().Before(u.BanUntil)
}

// Ban bans the user for a specified duration
// If duration is 0, it's a permanent ban
func (u *User) Ban(duration time.Duration, message string) {
	u.IsBanned = true
	if duration > 0 {
		u.BanUntil = time.Now().Add(duration)
	} else {
		u.BanUntil = time.Time{} // Zero time means permanent
	}
	u.BanMessage = message
	u.UpdatedAt = time.Now()
}

// Unban removes the ban from the user
func (u *User) Unban() {
	u.IsBanned = false
	u.BanUntil = time.Time{}
	u.BanMessage = ""
	u.UpdatedAt = time.Now()
}

// ResetAfterDataDelete clears session pointers, ID counters, and
// cross-conversation memory. The user row itself is kept. Unbans the user.
func (u *User) ResetAfterDataDelete() {
	u.ActiveSessionIDs = make(map[AgentType]string)
	u.SessionSeqs = make(map[AgentType]int)
	u.ActiveConversationID = ""
	u.ContextSummary = SummaryEntries{}
	u.ContextTags = []string{}
	u.SessionSeq = 0
	u.FileSeq = 0
	u.WorkflowSeq = 0
	u.ScheduleSeq = 0
	u.ReviewSeq = 0
	u.Unban()
}

// GetActiveSessionID returns the active session ID for a given agent type
// Returns empty string if no active session exists
func (u *User) GetActiveSessionID(agentType AgentType) string {
	if u.ActiveSessionIDs == nil {
		return ""
	}
	return u.ActiveSessionIDs[agentType]
}

// SetActiveSessionID sets the active session ID for a given agent type
func (u *User) SetActiveSessionID(agentType AgentType, sessionID string) {
	if u.ActiveSessionIDs == nil {
		u.ActiveSessionIDs = make(map[AgentType]string)
	}
	u.ActiveSessionIDs[agentType] = sessionID
	u.UpdatedAt = time.Now()
}

// NextSessionSeq increments and returns the next per-user session sequence.
// Agent type is ignored; sessions of every type share one numeric counter.
func (u *User) NextSessionSeq(agentType AgentType) int {
	_ = agentType
	u.UpdatedAt = time.Now()
	return NextSeq(&u.SessionSeq)
}

// GetSessionSeq returns the current per-user session sequence.
func (u *User) GetSessionSeq(agentType AgentType) int {
	_ = agentType
	if u == nil {
		return 0
	}
	return u.SessionSeq
}

// NextFileID increments the per-user file counter and returns a numeric FileID.
func (u *User) NextFileID() string {
	u.UpdatedAt = time.Now()
	return FormatID(NextSeq(&u.FileSeq))
}

// NextWorkflowID increments the per-user workflow counter and returns a numeric WorkflowID.
func (u *User) NextWorkflowID() string {
	u.UpdatedAt = time.Now()
	return FormatID(NextSeq(&u.WorkflowSeq))
}

// NextScheduleID increments the per-user schedule counter and returns a numeric ScheduleID.
func (u *User) NextScheduleID() string {
	u.UpdatedAt = time.Now()
	return FormatID(NextSeq(&u.ScheduleSeq))
}

// NextReviewID increments the per-user review counter and returns a numeric review ID.
func (u *User) NextReviewID() string {
	u.UpdatedAt = time.Now()
	return FormatID(NextSeq(&u.ReviewSeq))
}
