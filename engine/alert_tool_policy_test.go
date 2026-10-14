package engine

import (
	"context"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func TestAutomatedAlertToolPolicyBlocksEvenForcedCall(t *testing.T) {
	ctx := context.WithValue(context.Background(), disabledToolsContextKey{}, true)
	e := &Engine{}
	result, img := e.executeTool(ctx, &model.Session{SessionID: "chat", UserID: "owner"}, "message", openai.ToolCall{Function: openai.FunctionCall{Name: "open_trade", Arguments: `{"symbol":"BTCUSDT"}`}})
	if img != nil || result != "Tools are disabled for this automated alert turn." {
		t.Fatalf("forced call escaped policy: %q", result)
	}
	if toolsDisabled(context.Background()) {
		t.Fatal("policy escaped alert turn")
	}
}
