package engine

import (
	"context"
	"strings"

	"github.com/ghiac/agentize/model"
)

const (
	BillingChannelChat            = "chat"
	BillingChannelAlert           = "alert"
	BillingChannelScheduler       = "scheduler"
	BillingChannelMoneyManagement = "money_management"
)

type billingChannelCtxKey struct{}

func withBillingChannel(ctx context.Context, channel string) context.Context {
	if ctx == nil || strings.TrimSpace(channel) == "" {
		return ctx
	}
	return context.WithValue(ctx, billingChannelCtxKey{}, channel)
}

func billingChannelFromCtx(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	channel, _ := ctx.Value(billingChannelCtxKey{}).(string)
	return strings.TrimSpace(channel)
}

func billingChannelFromMeta(meta map[string]any, session *model.Session) string {
	kind := strings.ToLower(model.MessageMetaString(meta, "kind"))
	if kind == "" && meta != nil {
		if source, ok := meta["source"].(map[string]any); ok {
			kind = strings.ToLower(model.MessageMetaString(source, "kind"))
		}
	}
	switch kind {
	case model.MessageMetaKindAlert:
		return BillingChannelAlert
	case model.MessageMetaKindSchedule:
		return BillingChannelScheduler
	case "money-confirm", "money_management", "money-management":
		return BillingChannelMoneyManagement
	}
	if session != nil {
		if session.HasScheduleTag() || session.AgentType == model.AgentTypeSchedule {
			return BillingChannelScheduler
		}
		if session.AgentType == model.AgentTypeAlert {
			return BillingChannelAlert
		}
	}
	return BillingChannelChat
}

func usageBillingMeta(ctx context.Context, session *model.Session, extra map[string]any) map[string]any {
	channel := billingChannelFromCtx(ctx)
	if channel == "" {
		channel = billingChannelFromMeta(nil, session)
	}
	out := map[string]any{"channel": channel}
	if session != nil && session.AgentType != "" {
		out["agent_type"] = string(session.AgentType)
	}
	for key, value := range extra {
		if value != nil {
			out[key] = value
		}
	}
	return out
}
