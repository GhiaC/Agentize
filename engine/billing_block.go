package engine

import (
	"strings"
)

var billingBlockPrefixes = []string{
	"QUOTA_EXCEEDED:",
	"CREDIT_INSUFFICIENT:",
	"QUOTA_BLOCKED:",
}

// DefaultBillingRequiredMessage is the durable chat/scheduler copy when a turn
// is blocked because free quota is exhausted and there is no spendable credit.
const DefaultBillingRequiredMessage = "Your free usage limit is used up. Open Billing, add credit, then try again."

// IsBillingBlock reports whether err is a quota/credit gate, not a transport failure.
func IsBillingBlock(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, prefix := range billingBlockPrefixes {
		if strings.Contains(msg, prefix) {
			return true
		}
	}
	return false
}

// UserVisibleBlockedMessage strips technical billing prefixes so chat and
// scheduler transcripts show the same operator-facing sentence.
func UserVisibleBlockedMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	for _, prefix := range billingBlockPrefixes {
		if strings.HasPrefix(msg, prefix) {
			msg = strings.TrimSpace(strings.TrimPrefix(msg, prefix))
			break
		}
	}
	if msg == "" {
		return DefaultBillingRequiredMessage
	}
	return msg
}
