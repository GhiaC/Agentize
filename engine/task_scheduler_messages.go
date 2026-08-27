package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func taskScheduleStatusMessageID(sourceSessionID, scheduleID string) string {
	return sourceSessionID + "-schedule-" + scheduleID
}

func (s *TaskScheduler) withScheduleStatus(ctx context.Context, schedule *model.TaskSchedule) context.Context {
	s.mu.Lock()
	fn := s.messageFunc
	s.mu.Unlock()
	if fn == nil || schedule == nil {
		return ctx
	}
	return WithStatusFunc(ctx, func(status *StatusUpdate) {
		if status == nil || status.Phase != StatusCustom || strings.TrimSpace(status.Detail) == "" {
			return
		}
		s.publishToolMessage(ctx, schedule, status)
	})
}

// FormatTaskScheduleMessage is the one-line collapsed summary stored as Content.
// Full run details live on Message.Metadata so the chat stays a compact chip.
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

func (s *TaskScheduler) publishFinalMessage(ctx context.Context, schedule *model.TaskSchedule) {
	if schedule == nil || strings.TrimSpace(schedule.SourceSessionID) == "" {
		return
	}
	messageID := strings.TrimSpace(schedule.StatusMessageID)
	if messageID == "" { // Backward compatibility for schedules created before this field existed.
		messageID = taskScheduleStatusMessageID(schedule.SourceSessionID, schedule.ScheduleID)
		schedule.StatusMessageID = messageID
		if err := s.store.PutTaskSchedule(schedule); err != nil {
			log.Log.Warnf("[TaskScheduler] failed to persist status message id for %s: %v", schedule.ScheduleID, err)
		}
	}
	message := &model.Message{
		MessageID: messageID, UserID: schedule.UserID, SessionID: schedule.SourceSessionID,
		Role: openai.ChatMessageRoleAssistant, Content: FormatTaskScheduleMessage(schedule),
		AgentType: schedule.AgentType, ContentType: model.ContentTypeWidget,
		Metadata:  model.NewScheduleMessageMeta(schedule),
		CreatedAt: time.Now(),
	}
	s.publishMessage(ctx, schedule, message, StatusCompleted, false)
}

func (s *TaskScheduler) publishToolMessage(ctx context.Context, schedule *model.TaskSchedule, status *StatusUpdate) {
	messageID := strings.TrimSpace(status.MessageID)
	sendAsNew := status.SendAsNewMessage
	createdAt := schedule.CreatedAt
	prefixSourceSession := true
	switch {
	case status.SendAsNewMessage:
		messageID = newTaskID("smsg")
		createdAt = time.Now()
	case messageID != "":
		hash := sha256.Sum256([]byte(messageID))
		messageID = fmt.Sprintf("status-%x", hash[:8])
		createdAt = time.Now()
	default:
		// update_status without an explicit target is the schedule's progress
		// message. It is deliberately overwritten by the compact final result.
		messageID = strings.TrimSpace(schedule.StatusMessageID)
		if messageID == "" {
			messageID = taskScheduleStatusMessageID(schedule.SourceSessionID, schedule.ScheduleID)
		}
		prefixSourceSession = false
	}
	if prefixSourceSession {
		messageID = schedule.SourceSessionID + "-" + messageID
	}
	message := &model.Message{
		MessageID: messageID,
		UserID:    schedule.UserID, SessionID: schedule.SourceSessionID,
		Role: openai.ChatMessageRoleAssistant, Content: strings.TrimSpace(status.Detail),
		AgentType: schedule.AgentType, ContentType: model.ContentTypeText, CreatedAt: createdAt,
	}
	s.publishMessage(ctx, schedule, message, status.Phase, sendAsNew)
}

func (s *TaskScheduler) publishMessage(
	ctx context.Context,
	schedule *model.TaskSchedule,
	message *model.Message,
	phase StatusPhase,
	sendAsNew bool,
) {
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
	currentDeliveryID := ""
	if message.MessageID == schedule.StatusMessageID {
		currentDeliveryID = schedule.StatusDeliveryID
	}
	scheduleSnapshot := *schedule
	deliveryID, err := fn(ctx, &TaskScheduleMessageUpdate{
		Message: message, Schedule: &scheduleSnapshot, ScheduleID: schedule.ScheduleID,
		ConversationID: schedule.SourceConversationID, DeliveryID: currentDeliveryID,
		Phase: phase, SendAsNew: sendAsNew,
	})
	if err != nil {
		log.Log.Warnf("[TaskScheduler] chat message callback failed for %s: %v", schedule.ScheduleID, err)
		return
	}
	if !sendAsNew && message.MessageID == schedule.StatusMessageID && strings.TrimSpace(deliveryID) != "" && deliveryID != schedule.StatusDeliveryID {
		schedule.StatusDeliveryID = strings.TrimSpace(deliveryID)
		s.persistStatusDeliveryID(schedule.ScheduleID, schedule.StatusDeliveryID)
	}
}

func (s *TaskScheduler) persistStatusDeliveryID(scheduleID, deliveryID string) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	current, err := s.store.GetTaskSchedule(scheduleID)
	if err != nil || current == nil || current.StatusDeliveryID == deliveryID {
		return
	}
	current.StatusDeliveryID = deliveryID
	current.UpdatedAt = time.Now()
	if err := s.store.PutTaskSchedule(current); err != nil {
		log.Log.Warnf("[TaskScheduler] failed to persist delivery id for %s: %v", scheduleID, err)
	}
}
