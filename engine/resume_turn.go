package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ErrNothingToResume means the latest turn already has a final response and
// its last tool calls succeeded.
var ErrNothingToResume = errors.New("nothing to resume")

type toolReplayKey struct{}

type toolReplay struct {
	ToolID    string
	MessageID string
}

func withToolReplay(ctx context.Context, replay toolReplay) context.Context {
	return context.WithValue(ctx, toolReplayKey{}, replay)
}

func toolReplayFrom(ctx context.Context) (toolReplay, bool) {
	replay, ok := ctx.Value(toolReplayKey{}).(toolReplay)
	if !ok || strings.TrimSpace(replay.ToolID) == "" {
		return toolReplay{}, false
	}
	return replay, true
}

// ResumeTool is one existing tool call that must run again. Successful calls
// are not included.
type ResumeTool struct {
	Call        openai.ToolCall
	ToolID      string
	MessageID   string
	ResultIndex int
}

// TurnResumePlan describes how to continue the latest turn without deleting
// its user message, assistant tool-call message, or tool-call rows.
type TurnResumePlan struct {
	Resumable     bool
	UserMessageID string
	UserContent   string
	Retry         []ResumeTool
}

// TurnCanResume reports whether the latest turn stopped on a failed tool call
// or never reached a final assistant response.
func TurnCanResume(sessionMsgs []openai.ChatCompletionMessage, stored []*model.Message, tools []*model.ToolCall) bool {
	return planTurnResume(sessionMsgs, stored, tools).Resumable
}

// ResumeConversation continues the conversation's main session from the last
// unfinished tool call or incomplete DAG. Admitted messages and tool rows stay.
func (e *Engine) ResumeConversation(ctx context.Context, userID, conversationID string) (string, int, error) {
	conv, err := e.GetConversation(userID, conversationID)
	if err != nil {
		return "", 0, err
	}
	return e.ResumeIncompleteTurn(model.WithUserID(ctx, userID), conv.SessionID)
}

// ResumeIncompleteTurn re-runs only the latest unfinished tool calls, then
// continues the LLM loop from the existing transcript.
func (e *Engine) ResumeIncompleteTurn(ctx context.Context, sessionID string) (string, int, error) {
	if e == nil {
		return "", 0, errors.New("engine is nil")
	}
	e.ensureSessionProgress()
	if !e.IsDBReady() {
		return "", 0, errors.New("database is not ready. Call Init() first")
	}
	if e.llmClient == nil {
		return "", 0, errors.New("LLM client is not configured. Call UseLLMConfig first")
	}
	key := e.sessionKey(ctx, sessionID)
	sessionMu := e.getSessionMutex(key)
	sessionMu.Lock()
	defer sessionMu.Unlock()

	e.sessionProgress.SetInProgress(key, true)
	defer e.sessionProgress.SetInProgress(key, false)

	session, err := e.loadSession(ctx, sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get session: %w", err)
	}
	if session.UserID != "" {
		ctx = model.WithUserID(ctx, session.UserID)
	}
	stored, err := e.Sessions.GetUserMessagesBySession(session.UserID, session.SessionID)
	if err != nil {
		return "", 0, err
	}
	tools, err := e.Sessions.GetUserToolCallsBySession(session.UserID, session.SessionID)
	if err != nil {
		return "", 0, err
	}
	plan := planTurnResume(session.Msgs, stored, tools)
	if !plan.Resumable {
		return "", 0, ErrNothingToResume
	}

	session.Msgs = ensureTurnToolMessages(session.Msgs, toolsForUser(tools, plan.UserMessageID))
	plan = planTurnResume(session.Msgs, stored, tools)
	if err := e.replayFailedTools(ctx, session, plan); err != nil {
		e.persistSessionRunState(session, StatusError, err.Error(), false, plan.UserMessageID)
		return "", 0, err
	}
	session.UpdatedAt = time.Now()
	if err := e.Sessions.Put(session); err != nil {
		return "", 0, err
	}

	rec := model.NewRouteTraceBuilder(session, plan.UserContent)
	rec.SetUserMessageID(plan.UserMessageID)
	rec.SetKind("turn")
	ctx = WithUserMessageID(ctx, plan.UserMessageID)
	ctx = withTurnRecorder(ctx, rec)
	traceStart := time.Now()
	persistTurnTrace(e.Sessions, rec, 0)
	defer func() { persistTurnTrace(e.Sessions, rec, time.Since(traceStart)) }()

	e.persistSessionRunState(session, StatusThinking, "Resuming from the last step", true, plan.UserMessageID)
	response, tokens, err := e.processChatRequest(ctx, sessionID, QueueUser)
	if err != nil {
		rec.Fail(err.Error())
		e.persistSessionRunState(session, StatusError, err.Error(), false, plan.UserMessageID)
		return "", tokens, err
	}
	e.persistSessionRunState(session, StatusCompleted, "", false, plan.UserMessageID)
	e.touchOwningConversation(session)
	log.Log.Infof("[Engine] ✅ Resumed turn | SessionID: %s | ResponseLen: %d", sessionID, len(response))
	return response, tokens, nil
}

func (e *Engine) replayFailedTools(ctx context.Context, session *model.Session, plan TurnResumePlan) error {
	if len(plan.Retry) == 0 {
		return nil
	}
	for _, item := range plan.Retry {
		if err := e.markToolPending(session, item); err != nil {
			return err
		}
		replayCtx := withToolReplay(ctx, toolReplay{ToolID: item.ToolID, MessageID: item.MessageID})
		result, _ := e.executeTool(replayCtx, session, item.MessageID, item.Call)
		session.Msgs = replaceToolResult(session.Msgs, item, result)
	}
	return nil
}

func (e *Engine) markToolPending(session *model.Session, item ResumeTool) error {
	if e == nil || e.Sessions == nil || strings.TrimSpace(item.ToolID) == "" {
		return nil
	}
	tools, err := e.Sessions.GetUserToolCallsBySession(session.UserID, session.SessionID)
	if err != nil {
		return err
	}
	for _, tool := range tools {
		if tool == nil || tool.ToolID != item.ToolID {
			continue
		}
		if item.MessageID != "" && tool.MessageID != item.MessageID {
			continue
		}
		tool.Status = model.ToolCallStatusPending
		tool.Error = ""
		tool.UpdatedAt = time.Now()
		return e.Sessions.PutToolCall(tool)
	}
	return nil
}

func planTurnResume(sessionMsgs []openai.ChatCompletionMessage, stored []*model.Message, tools []*model.ToolCall) TurnResumePlan {
	user := lastStoredUserMessage(stored)
	if user == nil {
		userContent := ""
		for i := len(sessionMsgs) - 1; i >= 0; i-- {
			if sessionMsgs[i].Role == openai.ChatMessageRoleUser {
				userContent = sessionMsgs[i].Content
				break
			}
		}
		if strings.TrimSpace(userContent) == "" || turnHasFinalAssistant(sessionMsgs, -1) {
			return TurnResumePlan{}
		}
		return TurnResumePlan{Resumable: true, UserContent: userContent}
	}
	if storedTurnFinished(stored, user) {
		return TurnResumePlan{}
	}
	turnTools := toolsForUser(tools, user.MessageID)
	plan := TurnResumePlan{
		Resumable:     true,
		UserMessageID: user.MessageID,
		UserContent:   user.Content,
	}
	lastBatch := latestToolBatch(turnTools)
	if len(lastBatch) == 0 {
		return plan
	}
	resultAt := toolResultIndexes(sessionMsgs)
	for _, tool := range lastBatch {
		if tool == nil {
			continue
		}
		idx, ok := resultAt[tool.ToolCallID]
		content := ""
		if ok {
			content = sessionMsgs[idx].Content
		}
		if toolSucceeded(tool.Status, content) {
			continue
		}
		args := tool.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		plan.Retry = append(plan.Retry, ResumeTool{
			Call: openai.ToolCall{
				ID:   tool.ToolCallID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tool.FunctionName,
					Arguments: args,
				},
			},
			ToolID:      tool.ToolID,
			MessageID:   tool.MessageID,
			ResultIndex: mapResultIndex(ok, idx),
		})
	}
	return plan
}

func mapResultIndex(ok bool, idx int) int {
	if !ok {
		return -1
	}
	return idx
}

func lastStoredUserMessage(stored []*model.Message) *model.Message {
	var last *model.Message
	for _, msg := range stored {
		if msg == nil || msg.Role != openai.ChatMessageRoleUser {
			continue
		}
		if last == nil || msg.SeqID >= last.SeqID {
			last = msg
		}
	}
	return last
}

func storedTurnFinished(stored []*model.Message, user *model.Message) bool {
	if user == nil {
		return false
	}
	for _, msg := range stored {
		if msg == nil || msg.SeqID <= user.SeqID {
			continue
		}
		if isFinalAssistantStored(msg) {
			return true
		}
	}
	return false
}

func isFinalAssistantStored(msg *model.Message) bool {
	if msg == nil || msg.Role != openai.ChatMessageRoleAssistant {
		return false
	}
	if msg.HasToolCalls {
		return false
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" || strings.HasPrefix(content, "[Tool Calls:") {
		return false
	}
	return true
}

func turnHasFinalAssistant(msgs []openai.ChatCompletionMessage, lastUser int) bool {
	if lastUser < 0 {
		lastUser = -1
		for i, msg := range msgs {
			if msg.Role == openai.ChatMessageRoleUser {
				lastUser = i
			}
		}
	}
	for i := lastUser + 1; i < len(msgs); i++ {
		msg := msgs[i]
		if msg.Role == openai.ChatMessageRoleAssistant && len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) != "" && !strings.HasPrefix(strings.TrimSpace(msg.Content), "[Tool Calls:") {
			return true
		}
	}
	return false
}

func toolsForUser(tools []*model.ToolCall, userMessageID string) []*model.ToolCall {
	if strings.TrimSpace(userMessageID) == "" {
		return nil
	}
	out := make([]*model.ToolCall, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && tool.UserMessageID == userMessageID {
			out = append(out, tool)
		}
	}
	return out
}

func latestToolBatch(tools []*model.ToolCall) []*model.ToolCall {
	var latest *model.ToolCall
	for _, tool := range tools {
		if tool == nil || strings.TrimSpace(tool.MessageID) == "" {
			continue
		}
		if latest == nil || tool.CreatedAt.After(latest.CreatedAt) {
			latest = tool
		}
	}
	if latest == nil {
		return nil
	}
	out := make([]*model.ToolCall, 0, len(tools))
	for _, tool := range tools {
		if tool != nil && tool.MessageID == latest.MessageID {
			out = append(out, tool)
		}
	}
	return out
}

func toolResultIndexes(msgs []openai.ChatCompletionMessage) map[string]int {
	out := map[string]int{}
	for i, msg := range msgs {
		if msg.Role == openai.ChatMessageRoleTool && msg.ToolCallID != "" {
			out[msg.ToolCallID] = i
		}
	}
	return out
}

func toolSucceeded(status, content string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case model.ToolCallStatusFailed, model.ToolCallStatusPending, "":
		return false
	}
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "Error executing tool ") {
		return false
	}
	if strings.HasPrefix(content, "Tool ") && strings.Contains(content, "was not executed") {
		return false
	}
	return true
}

// ensureTurnToolMessages appends any tool-call transcript that was stripped
// from session history. It never removes a message.
func ensureTurnToolMessages(msgs []openai.ChatCompletionMessage, tools []*model.ToolCall) []openai.ChatCompletionMessage {
	if len(tools) == 0 {
		return msgs
	}
	present := toolResultIndexes(msgs)
	called := map[string]bool{}
	for _, msg := range msgs {
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				called[call.ID] = true
			}
		}
	}
	batches := map[string][]*model.ToolCall{}
	var order []string
	for _, tool := range tools {
		if tool == nil || tool.ToolCallID == "" {
			continue
		}
		if _, ok := batches[tool.MessageID]; !ok {
			order = append(order, tool.MessageID)
		}
		batches[tool.MessageID] = append(batches[tool.MessageID], tool)
	}
	out := append([]openai.ChatCompletionMessage(nil), msgs...)
	for _, messageID := range order {
		batch := batches[messageID]
		var missing []openai.ToolCall
		for _, tool := range batch {
			if called[tool.ToolCallID] {
				continue
			}
			args := tool.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			missing = append(missing, openai.ToolCall{
				ID:   tool.ToolCallID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tool.FunctionName,
					Arguments: args,
				},
			})
		}
		if len(missing) > 0 {
			out = append(out, openai.ChatCompletionMessage{
				Role:      openai.ChatMessageRoleAssistant,
				ToolCalls: missing,
			})
		}
		for _, tool := range batch {
			if _, ok := present[tool.ToolCallID]; ok {
				continue
			}
			content := tool.Response
			if strings.TrimSpace(content) == "" && tool.Status == model.ToolCallStatusFailed {
				content = tool.Error
			}
			out = append(out, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    content,
				Name:       tool.FunctionName,
				ToolCallID: tool.ToolCallID,
			})
		}
	}
	return out
}

func replaceToolResult(msgs []openai.ChatCompletionMessage, item ResumeTool, result string) []openai.ChatCompletionMessage {
	out := append([]openai.ChatCompletionMessage(nil), msgs...)
	for i := range out {
		if out[i].Role == openai.ChatMessageRoleTool && out[i].ToolCallID == item.Call.ID {
			out[i].Content = result
			out[i].Name = item.Call.Function.Name
			return out
		}
	}
	return append(out, openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    result,
		Name:       item.Call.Function.Name,
		ToolCallID: item.Call.ID,
	})
}
