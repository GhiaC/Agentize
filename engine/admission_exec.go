package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

// ErrSessionQueueFull is returned when a session already holds its admission capacity.
var ErrSessionQueueFull = store.ErrSessionQueueFull

type admittedRunContextKey struct{}

type admittedRunContext struct {
	UserID string
	Run    *model.SessionRun
}

func withAdmittedRun(ctx context.Context, userID string, run *model.SessionRun) context.Context {
	return context.WithValue(ctx, admittedRunContextKey{}, admittedRunContext{UserID: userID, Run: run})
}

func admittedRunFrom(ctx context.Context) (admittedRunContext, bool) {
	run, ok := ctx.Value(admittedRunContextKey{}).(admittedRunContext)
	return run, ok && run.Run != nil
}

// admitAndRun persists the input, then executes every claimed head run in
// admission order. It returns the response for this input only.
func (e *Engine) admitAndRun(ctx context.Context, sessionID string, msg IncomingMessage) (string, int, error) {
	userID := strings.TrimSpace(model.UserIDFrom(ctx))
	if userID == "" {
		return "", 0, errors.New("owner is required")
	}
	origin := "user"
	if msg.queueClass() == QueueDeferred {
		origin = "deferred"
	}
	keyName := ""
	if msg.Metadata != nil {
		if raw, ok := msg.Metadata["idempotency_key"].(string); ok {
			keyName = strings.TrimSpace(raw)
		}
	}
	admitted, err := e.Sessions.AdmitSessionRun(model.SessionRunInput{
		UserID: userID, SessionID: sessionID, OriginKind: origin,
		IdempotencyKey: keyName, Content: msg.Content,
	})
	if errors.Is(err, store.ErrSessionQueueFull) {
		metrics.SessionAdmission("rejected")
		return "", 0, err
	}
	if err != nil {
		metrics.SessionAdmission("error")
		return "", 0, err
	}
	metrics.SessionAdmission(admitted.Outcome)
	if admitted.Outcome == model.SessionAdmitDuplicate && model.SessionRunTerminal(admitted.Run.Status) {
		return "", 0, nil
	}

	key := e.sessionKey(ctx, sessionID)
	sessionMu := e.getSessionMutex(key)
	sessionMu.Lock()
	defer sessionMu.Unlock()
	e.sessionProgress.SetInProgress(key, true)
	defer e.sessionProgress.SetInProgress(key, false)

	var response string
	var tokens int
	var runErr error
	processed := false
	for {
		run, claimErr := e.Sessions.ClaimHeadSessionRun(userID, sessionID, "engine")
		if claimErr != nil {
			metrics.SessionClaim("error")
			return response, tokens, claimErr
		}
		if run == nil {
			metrics.SessionClaim("held")
			if !processed {
				current, getErr := e.Sessions.GetSessionRun(userID, sessionID, admitted.Run.RunID)
				if getErr == nil && current != nil && model.SessionRunTerminal(current.Status) {
					return response, tokens, runErr
				}
				metrics.MessageQueued("agent")
				return queuedAckMessage, tokens, nil
			}
			return response, tokens, runErr
		}
		metrics.SessionClaim("claimed")
		class := QueueUser
		if run.OriginKind == "deferred" || run.OriginKind == "alert" || run.OriginKind == "schedule" {
			class = QueueDeferred
		}
		runCtx := withAdmittedRun(ctx, userID, run)
		text, used, bodyErr := e.processOneMessageBody(runCtx, sessionID, IncomingMessage{Content: run.Content, Metadata: msg.Metadata, Queue: class})
		status := model.SessionRunSucceeded
		errText := ""
		if bodyErr != nil {
			status = model.SessionRunFailed
			errText = bodyErr.Error()
		}
		if finErr := e.Sessions.FinalizeSessionRun(userID, sessionID, run.RunID, run.Fence, status, errText); finErr != nil {
			return text, tokens + used, finErr
		}
		if run.RunID == admitted.Run.RunID {
			processed = true
			response = text
			tokens += used
			runErr = bodyErr
			if bodyErr != nil {
				return response, tokens, runErr
			}
		}
	}
}
