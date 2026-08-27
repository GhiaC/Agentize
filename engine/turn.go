package engine

import (
	"context"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

type userMessageIDKey struct{}
type turnRecorderKey struct{}

// WithUserMessageID tags ctx with the user-message row that owns the current
// turn. Tool persistence and the live status channel read it so every tool,
// approval, and DAG node joins the same message.
func WithUserMessageID(ctx context.Context, id string) context.Context {
	id = strings.TrimSpace(id)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, userMessageIDKey{}, id)
}

// UserMessageIDFrom returns the turn's user-message id, or "".
func UserMessageIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(userMessageIDKey{}).(string)
	return id
}

func withTurnRecorder(ctx context.Context, b *model.RouteTraceBuilder) context.Context {
	return context.WithValue(ctx, turnRecorderKey{}, b)
}

func turnRecorderFrom(ctx context.Context) *model.RouteTraceBuilder {
	b, _ := ctx.Value(turnRecorderKey{}).(*model.RouteTraceBuilder)
	return b
}

func persistTurnTrace(st store.Store, b *model.RouteTraceBuilder, total time.Duration) {
	trace := b.Build(total)
	if trace == nil || st == nil {
		return
	}
	if err := st.PutRouteTrace(trace); err != nil {
		metrics.RouteTrace("error", trace.NodeCount())
		log.Log.Warnf("[Engine] ⚠️  Failed to persist turn DAG | TraceID: %s | Error: %v", trace.TraceID, err)
		return
	}
	metrics.RouteTrace(trace.Status, trace.NodeCount())
}
