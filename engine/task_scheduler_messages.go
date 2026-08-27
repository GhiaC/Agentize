package engine

import (
	"context"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func taskScheduleStatusMessageID(sourceSessionID, scheduleID string) string {
	return sourceSessionID + "-schedule-" + scheduleID
}

func scheduleRunMessageID(sourceSessionID, runID, role string) string {
	return strings.TrimSpace(sourceSessionID) + "-schrun-" + strings.TrimSpace(runID) + "-" + strings.TrimSpace(role)
}

// FormatTaskScheduleMessage is the one-line collapsed summary previously stored
// as a durable widget. Kept for hosts that still render leftover widget rows.
func FormatTaskScheduleMessage(schedule *model.TaskSchedule) string {
	if schedule == nil {
		return ""
	}
	status := strings.TrimSpace(string(schedule.LastRunStatus))
	if status == "" {
		status = string(schedule.Status)
	}
	if status == "" {
		status = "—"
	}
	return "⏱️ " + schedule.Name + " · " + status
}

func (s *TaskScheduler) publishScheduleState(ctx context.Context, schedule *model.TaskSchedule) {
	if s == nil || schedule == nil {
		return
	}
	s.mu.Lock()
	fn := s.messageFunc
	s.mu.Unlock()
	if fn == nil {
		return
	}
	snapshot := *schedule
	if _, err := fn(ctx, &TaskScheduleMessageUpdate{
		Schedule: &snapshot, ScheduleID: schedule.ScheduleID,
		ConversationID: schedule.SourceConversationID, Phase: StatusCustom,
	}); err != nil {
		log.Log.Warnf("[TaskScheduler] schedule state callback failed for %s: %v", schedule.ScheduleID, err)
	}
}

// publishRunTranscript writes this run's prompt and LLM result into the source
// chat as ordinary user/assistant messages so every execution stays visible.
func (s *TaskScheduler) publishRunTranscript(ctx context.Context, schedule *model.TaskSchedule, runID string) {
	if s == nil || schedule == nil || strings.TrimSpace(schedule.SourceSessionID) == "" {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		runID = newTaskID("run")
	}
	source := strings.TrimSpace(schedule.SourceSessionID)
	sameSession := strings.TrimSpace(schedule.SessionID) == source
	now := time.Now()

	var user, assistant *model.Message
	persist := true
	if sameSession {
		user, assistant = s.latestVisibleRunPair(source)
		if user != nil || assistant != nil {
			persist = false
		} else {
			user, assistant = s.synthesizeRunPair(schedule, runID, now)
		}
	} else {
		srcUser, srcAsst := s.latestVisibleRunPair(schedule.SessionID)
		if srcUser != nil {
			user = cloneMessageOntoSession(srcUser, source, scheduleRunMessageID(source, runID, "1-user"), now)
		}
		if srcAsst != nil {
			assistant = cloneMessageOntoSession(srcAsst, source, scheduleRunMessageID(source, runID, "2-assistant"), now.Add(time.Millisecond))
		}
		if user == nil && assistant == nil {
			user, assistant = s.synthesizeRunPair(schedule, runID, now)
		}
	}

	if persist {
		if user != nil {
			s.publishMessage(ctx, schedule, user, StatusCustom, true)
		}
		if assistant != nil {
			s.publishMessage(ctx, schedule, assistant, StatusCompleted, true)
		}
	}
	s.publishScheduleState(ctx, schedule)
}

func (s *TaskScheduler) latestVisibleRunPair(sessionID string) (user, assistant *model.Message) {
	if s == nil || s.store == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	msgs, err := s.store.GetMessagesBySessionPage(sessionID, 40, 0)
	if err != nil {
		return nil, nil
	}
	for _, item := range msgs {
		if item == nil {
			continue
		}
		if assistant == nil && item.Role == openai.ChatMessageRoleAssistant && strings.TrimSpace(item.Content) != "" {
			assistant = item
			continue
		}
		if user == nil && item.Role == openai.ChatMessageRoleUser && strings.TrimSpace(item.Content) != "" {
			user = item
		}
		if user != nil && assistant != nil {
			break
		}
	}
	return user, assistant
}

func (s *TaskScheduler) synthesizeRunPair(schedule *model.TaskSchedule, runID string, now time.Time) (user, assistant *model.Message) {
	if schedule == nil {
		return nil, nil
	}
	source := strings.TrimSpace(schedule.SourceSessionID)
	prompt := strings.TrimSpace(schedule.Prompt)
	reply := strings.TrimSpace(schedule.LastOutput)
	if reply == "" {
		reply = strings.TrimSpace(schedule.LastConclusion)
	}
	if reply == "" {
		reply = strings.TrimSpace(schedule.LastError)
	}
	if prompt != "" {
		user = &model.Message{
			MessageID: scheduleRunMessageID(source, runID, "1-user"),
			UserID:    schedule.UserID, SessionID: source,
			Role: openai.ChatMessageRoleUser, Content: prompt,
			AgentType: schedule.AgentType, ContentType: model.ContentTypeText,
			CreatedAt: now,
		}
	}
	if reply != "" {
		assistant = &model.Message{
			MessageID: scheduleRunMessageID(source, runID, "2-assistant"),
			UserID:    schedule.UserID, SessionID: source,
			Role: openai.ChatMessageRoleAssistant, Content: reply,
			AgentType: schedule.AgentType, ContentType: model.ContentTypeText,
			CreatedAt: now.Add(time.Millisecond),
		}
	}
	return user, assistant
}

func cloneMessageOntoSession(src *model.Message, sessionID, messageID string, now time.Time) *model.Message {
	if src == nil {
		return nil
	}
	out := *src
	out.MessageID = messageID
	out.SessionID = sessionID
	out.CreatedAt = now
	out.Metadata = nil
	if out.ContentType == model.ContentTypeWidget {
		out.ContentType = model.ContentTypeText
	}
	return &out
}

func (s *TaskScheduler) publishMessage(
	ctx context.Context,
	schedule *model.TaskSchedule,
	message *model.Message,
	phase StatusPhase,
	sendAsNew bool,
) {
	if message == nil {
		return
	}
	if err := s.store.PutMessage(message); err != nil {
		log.Log.Warnf("[TaskScheduler] failed to persist chat message %s: %v", message.MessageID, err)
		return
	}
	s.mu.Lock()
	fn := s.messageFunc
	s.mu.Unlock()
	if fn == nil {
		return
	}
	scheduleSnapshot := *schedule
	if _, err := fn(ctx, &TaskScheduleMessageUpdate{
		Message: message, Schedule: &scheduleSnapshot, ScheduleID: schedule.ScheduleID,
		ConversationID: schedule.SourceConversationID, Phase: phase, SendAsNew: sendAsNew,
	}); err != nil {
		log.Log.Warnf("[TaskScheduler] chat message callback failed for %s: %v", schedule.ScheduleID, err)
	}
}
