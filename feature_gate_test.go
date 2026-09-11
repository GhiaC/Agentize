package agentize

import "testing"

func TestFeatureEnabledFailsClosed(t *testing.T) {
	var ag *Agentize
	if ag.FeatureEnabled("u1", "daily_missions") {
		t.Fatal("nil agent must be off")
	}
	ag = &Agentize{}
	if ag.FeatureEnabled("u1", "daily_missions") {
		t.Fatal("unset gate must be off")
	}
	ag.SetFeatureGate(featureMap{"u1": {"daily_missions": true, "sepolia_payments": true}})
	if !ag.FeatureEnabled("u1", "daily_missions") || !ag.FeatureEnabled("u1", "sepolia_payments") {
		t.Fatal("enabled flags")
	}
	if ag.FeatureEnabled("u2", "daily_missions") {
		t.Fatal("other user must be off")
	}
	if ag.FeatureEnabled("u1", "") || ag.FeatureEnabled("", "daily_missions") {
		t.Fatal("empty ids must be off")
	}
}

type featureMap map[string]map[string]bool

func (m featureMap) Enabled(userID, flag string) bool { return m[userID][flag] }
