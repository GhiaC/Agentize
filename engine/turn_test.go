package engine

import (
	"context"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestWithUserMessageIDRoundTrip(t *testing.T) {
	ctx := WithUserMessageID(context.Background(), " sess-m0003 ")
	if got := UserMessageIDFrom(ctx); got != "sess-m0003" {
		t.Fatalf("UserMessageIDFrom = %q", got)
	}
	if got := UserMessageIDFrom(context.Background()); got != "" {
		t.Fatalf("empty ctx = %q", got)
	}
}

func TestTurnRecorderNilSafe(t *testing.T) {
	ctx := context.Background()
	if rec := turnRecorderFrom(ctx); rec != nil {
		t.Fatalf("expected nil recorder, got %+v", rec)
	}
	rec := turnRecorderFrom(withTurnRecorder(ctx, nil))
	rec.Approval("t", "T", "x", model.RouteStatusOK, 0)
}
