package store

import (
	"github.com/ghiac/agentize/model"
)

func fillSessionIDs(session *model.Session) {
	if session == nil {
		return
	}
	if session.Seq <= 0 {
		session.Seq = model.SeqFromID(session.SessionID)
	}
	session.SessionID = model.EnsureID(session.SessionID, session.Seq)
}

func fillConversationIDs(c *model.Conversation) {
	if c == nil {
		return
	}
	if c.Seq <= 0 {
		c.Seq = model.SeqFromID(c.ConversationID)
	}
	c.ConversationID = model.EnsureID(c.ConversationID, c.Seq)
}

func fillMessageIDs(m *model.Message) {
	if m == nil {
		return
	}
	if m.SeqID <= 0 {
		m.SeqID = model.SeqFromID(m.MessageID)
	}
	m.MessageID = model.EnsureID(m.MessageID, m.SeqID)
}

func fillToolCallIDs(tc *model.ToolCall) {
	if tc == nil {
		return
	}
	seq := model.SeqFromID(tc.ToolID)
	tc.ToolID = model.EnsureID(tc.ToolID, seq)
}

func fillUserFileIDs(f *model.UserFile) {
	if f == nil {
		return
	}
	seq := model.SeqFromID(f.FileID)
	f.FileID = model.EnsureID(f.FileID, seq)
}

func fillOpenedFileIDs(f *model.OpenedFile) {
	if f == nil {
		return
	}
	seq := model.SeqFromID(f.FileID)
	f.FileID = model.EnsureID(f.FileID, seq)
}

func fillLogIDs(log *model.SummarizationLog) {
	if log == nil {
		return
	}
	seq := model.SeqFromID(log.LogID)
	log.LogID = model.EnsureID(log.LogID, seq)
}

func fillWorkflowIDs(w *model.WorkflowRun) {
	if w == nil {
		return
	}
	seq := model.SeqFromID(w.WorkflowID)
	w.WorkflowID = model.EnsureID(w.WorkflowID, seq)
}

func fillRouteTraceIDs(t *model.RouteTrace) {
	if t == nil {
		return
	}
	seq := model.SeqFromID(t.TraceID)
	t.TraceID = model.EnsureID(t.TraceID, seq)
}

func fillScheduleIDs(s *model.TaskSchedule) {
	if s == nil {
		return
	}
	seq := model.SeqFromID(s.ScheduleID)
	s.ScheduleID = model.EnsureID(s.ScheduleID, seq)
}

// parseToolSeqFromToolID extracts sequence number from ToolID.
// Numeric ids parse directly; deprecated `{SessionID}-t{seq}` ids still work.
func parseToolSeqFromToolID(toolID string) int {
	return model.SeqFromID(toolID)
}
