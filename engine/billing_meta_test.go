package engine

import (
	"fmt"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestBillingChannelFromMeta(t *testing.T) {
	if got := billingChannelFromMeta(map[string]any{"kind": "alert"}, nil); got != BillingChannelAlert {
		t.Fatalf("alert kind = %q", got)
	}
	if got := billingChannelFromMeta(map[string]any{"kind": "schedule"}, nil); got != BillingChannelScheduler {
		t.Fatalf("schedule kind = %q", got)
	}
	if got := billingChannelFromMeta(map[string]any{"kind": "money-confirm"}, nil); got != BillingChannelMoneyManagement {
		t.Fatalf("money-confirm kind = %q", got)
	}
	if got := billingChannelFromMeta(nil, &model.Session{Tags: []string{"schedule:daily"}}); got != BillingChannelScheduler {
		t.Fatalf("schedule tag = %q", got)
	}
	if got := billingChannelFromMeta(nil, &model.Session{AgentType: model.AgentTypeCore}); got != BillingChannelChat {
		t.Fatalf("chat = %q", got)
	}
}

func TestUsageBillingMetaKeepsToolAction(t *testing.T) {
	meta := usageBillingMeta(nil, &model.Session{AgentType: model.AgentTypeSchedule}, map[string]any{"action": "run"})
	if meta["channel"] != BillingChannelScheduler || meta["action"] != "run" || meta["agent_type"] != "schedule" {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestUserVisibleBlockedMessageStripsPrefix(t *testing.T) {
	if got := UserVisibleBlockedMessage(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	got := UserVisibleBlockedMessage(fmt.Errorf("CREDIT_INSUFFICIENT: Open Billing, add credit, then try again."))
	if got != "Open Billing, add credit, then try again." {
		t.Fatalf("got %q", got)
	}
	if !IsBillingBlock(fmt.Errorf("QUOTA_EXCEEDED: limit reached")) {
		t.Fatal("expected billing block")
	}
	if IsBillingBlock(fmt.Errorf("provider timeout")) {
		t.Fatal("timeout is not a billing block")
	}
}
