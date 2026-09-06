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

func (s *TaskScheduler) nextSourceMessage(userID, sessionID string) (id string, seq int) {
	sessionID = strings.TrimSpace(sessionID)
	userID = strings.TrimSpace(userID)
	if s == nil || s.store == nil || sessionID == "" {
		return "", 0
	}
	session, err := s.store.GetUserSession(userID, sessionID)
	if err != nil || session == nil {
		session, err = s.store.Get(sessionID)
	}
	if err != nil || session == nil {
		return "", 0
	}
	id, seq = session.GenerateMessageIDWithSeq()
	if putErr := s.store.Put(session); putErr != nil {
		log.Log.Warnf("[TaskScheduler] failed to persist source message seq for %s: %v", sessionID, putErr)
	}
	return id, seq
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

func (s *TaskScheduler) publishRunPrompt(ctx context.Context, schedule *model.TaskSchedule, runID string) {
	if s == nil || schedule == nil {
		return
	}
	source := strings.TrimSpace(schedule.SourceSessionID)
	prompt := strings.TrimSpace(schedule.Prompt)
	if source == "" || prompt == "" {
		return
	}
	runID = strings.TrimSpace(runID)
	_ = runID
	userID, userSeq := s.nextSourceMessage(schedule.UserID, source)
	if userID == "" {
		log.Log.Warnf("[TaskScheduler] skipped run prompt: source session %s has no message id", source)
		return
	}
	user := &model.Message{
		MessageID: userID, SeqID: userSeq,
		UserID: schedule.UserID, SessionID: source,
		Role: openai.ChatMessageRoleUser, Content: prompt,
		AgentType: model.AgentTypeSchedule, ContentType: model.ContentTypeText,
		Metadata:  model.NewScheduleMessageMeta(schedule),
		CreatedAt: time.Now(),
	}
	sameSession := strings.TrimSpace(schedule.SessionID) == source
	s.emitChatMessage(ctx, schedule, user, StatusReceived, true, !sameSession)
}

// publishRunTranscript writes this run's prompt and LLM result into the source
// chat as ordinary user/assistant messages so every execution stays visible.
func (s *TaskScheduler) publishRunTranscript(ctx context.Context, schedule *model.TaskSchedule, runID string) {
	if s == nil || schedule == nil || strings.TrimSpace(schedule.SourceSessionID) == "" {
		return
	}
	runID = strings.TrimSpace(runID)
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
			user, assistant = s.synthesizeRunPair(schedule, now)
		}
	} else {
		user = s.latestSourceUser(source)
		_, srcAsst := s.latestVisibleRunPair(schedule.SessionID)
		if srcAsst != nil {
			id, seq := s.nextSourceMessage(schedule.UserID, source)
			assistant = cloneMessageOntoSession(srcAsst, source, id, seq, now, schedule)
		}
		if assistant == nil {
			assistant = s.synthesizeRunAssistant(schedule, now)
		}
		persist = assistant != nil
	}

	if sameSession {
		s.emitChatMessage(ctx, schedule, user, StatusCustom, true, persist)
	}
	s.emitChatMessage(ctx, schedule, assistant, StatusCompleted, true, persist)
	if user == nil {
		user = s.latestSourceUser(source)
	}
	s.mirrorRunAuditToSource(schedule, user)
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

func (s *TaskScheduler) latestSourceUser(sessionID string) *model.Message {
	if s == nil || s.store == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	msgs, err := s.store.GetMessagesBySessionPage(sessionID, 40, 0)
	if err != nil {
		return nil
	}
	for _, item := range msgs {
		if item != nil && item.Role == openai.ChatMessageRoleUser && strings.TrimSpace(item.Content) != "" {
			return item
		}
	}
	return nil
}

func (s *TaskScheduler) synthesizeRunPair(schedule *model.TaskSchedule, now time.Time) (user, assistant *model.Message) {
	if schedule == nil {
		return nil, nil
	}
	source := strings.TrimSpace(schedule.SourceSessionID)
	prompt := strings.TrimSpace(schedule.Prompt)
	if prompt != "" {
		id, seq := s.nextSourceMessage(schedule.UserID, source)
		if id != "" {
			user = &model.Message{
				MessageID: id, SeqID: seq,
				UserID: schedule.UserID, SessionID: source,
				Role: openai.ChatMessageRoleUser, Content: prompt,
				AgentType: model.AgentTypeSchedule, ContentType: model.ContentTypeText,
				Metadata:  model.NewScheduleMessageMeta(schedule),
				CreatedAt: now,
			}
		}
	}
	assistant = s.synthesizeRunAssistant(schedule, now)
	return user, assistant
}

func (s *TaskScheduler) synthesizeRunAssistant(schedule *model.TaskSchedule, now time.Time) *model.Message {
	if schedule == nil {
		return nil
	}
	source := strings.TrimSpace(schedule.SourceSessionID)
	reply := strings.TrimSpace(schedule.LastOutput)
	if reply == "" {
		reply = strings.TrimSpace(schedule.LastConclusion)
	}
	if reply == "" {
		reply = strings.TrimSpace(schedule.LastError)
	}
	if reply == "" {
		return nil
	}
	id, seq := s.nextSourceMessage(schedule.UserID, source)
	if id == "" {
		return nil
	}
	return &model.Message{
		MessageID: id, SeqID: seq,
		UserID: schedule.UserID, SessionID: source,
		Role: openai.ChatMessageRoleAssistant, Content: reply,
		AgentType: model.AgentTypeSchedule, ContentType: model.ContentTypeText,
		Metadata:  model.NewScheduleMessageMeta(schedule),
		CreatedAt: now.Add(time.Millisecond),
	}
}

func cloneMessageOntoSession(src *model.Message, sessionID string, messageID string, seq int, now time.Time, schedule *model.TaskSchedule) *model.Message {
	if src == nil || strings.TrimSpace(messageID) == "" {
		return nil
	}
	out := *src
	out.MessageID = messageID
	out.SeqID = seq
	out.SessionID = sessionID
	out.CreatedAt = now
	out.AgentType = model.AgentTypeSchedule
	out.Metadata = model.NewScheduleMessageMeta(schedule)
	out.HydrateUsageMeta()
	if out.ContentType == model.ContentTypeWidget {
		out.ContentType = model.ContentTypeText
	}
	return &out
}

func (s *TaskScheduler) mirrorRunAuditToSource(schedule *model.TaskSchedule, sourceUser *model.Message) {
	if s == nil || s.store == nil || schedule == nil || sourceUser == nil {
		return
	}
	source := strings.TrimSpace(sourceUser.SessionID)
	worker := strings.TrimSpace(schedule.SessionID)
	userID := strings.TrimSpace(schedule.UserID)
	sourceUserID := strings.TrimSpace(sourceUser.MessageID)
	if source == "" || worker == "" || source == worker || sourceUserID == "" {
		return
	}
	traces, err := s.store.GetUserRouteTracesBySession(userID, worker)
	if err != nil {
		traces, err = s.store.GetRouteTracesBySession(worker)
	}
	var latest *model.RouteTrace
	if err == nil {
		for _, tr := range traces {
			if tr == nil || (tr.Kind != "turn" && strings.TrimSpace(tr.Kind) != "") {
				continue
			}
			if strings.TrimSpace(tr.UserID) != "" && tr.UserID != userID {
				continue
			}
			latest = tr
			break
		}
	}
	workerUserMessageID := ""
	if latest != nil {
		workerUserMessageID = strings.TrimSpace(latest.UserMessageID)
		cloned := *latest
		if len(latest.Nodes) > 0 {
			cloned.Nodes = append([]model.RouteNode(nil), latest.Nodes...)
		}
		if len(latest.Edges) > 0 {
			cloned.Edges = append([]model.RouteEdge(nil), latest.Edges...)
		}
		if sess, sessErr := s.store.GetUserSession(userID, source); sessErr == nil && sess != nil {
			cloned.TraceID = sess.GenerateRouteTraceID()
			if putErr := s.store.Put(sess); putErr != nil {
				log.Log.Warnf("[TaskScheduler] failed to persist source trace seq: %v", putErr)
			}
		}
		cloned.SessionID = source
		cloned.UserID = userID
		cloned.UserMessageID = sourceUserID
		cloned.Kind = "turn"
		if putErr := s.store.PutRouteTrace(&cloned); putErr != nil {
			log.Log.Warnf("[TaskScheduler] failed to copy turn DAG onto source chat: %v", putErr)
		}
	}
	tools, toolErr := s.store.GetUserToolCallsBySession(userID, worker)
	if toolErr != nil {
		return
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if workerUserMessageID != "" && strings.TrimSpace(tool.UserMessageID) != workerUserMessageID {
			continue
		}
		copied := *tool
		copied.SessionID = source
		copied.UserMessageID = sourceUserID
		copied.AgentType = model.AgentTypeSchedule
		if putErr := s.store.PutToolCall(&copied); putErr != nil {
			log.Log.Warnf("[TaskScheduler] failed to copy tool %s onto source chat: %v", tool.ToolID, putErr)
		}
	}
}

func (s *TaskScheduler) publishMessage(
	ctx context.Context,
	schedule *model.TaskSchedule,
	message *model.Message,
	phase StatusPhase,
	sendAsNew bool,
) {
	s.emitChatMessage(ctx, schedule, message, phase, sendAsNew, true)
}

func (s *TaskScheduler) emitChatMessage(
	ctx context.Context,
	schedule *model.TaskSchedule,
	message *model.Message,
	phase StatusPhase,
	sendAsNew bool,
	persist bool,
) {
	if message == nil {
		return
	}
	if persist {
		if err := s.store.PutMessage(message); err != nil {
			log.Log.Warnf("[TaskScheduler] failed to persist chat message %s: %v", message.MessageID, err)
			return
		}
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
