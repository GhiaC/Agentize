package engine

import (
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
