package model

import "testing"

func TestSessionAddUsageAccumulatesTokensAndCost(t *testing.T) {
	s := NewSessionWithType("user-1", AgentTypeConversation)
	s.AddUsage(100, 20, 0.014)
	s.AddUsage(50, 10, 0.007)
	if s.PromptTokens != 150 || s.CompletionTokens != 30 || s.TotalTokens != 180 {
		t.Fatalf("tokens prompt=%d completion=%d total=%d", s.PromptTokens, s.CompletionTokens, s.TotalTokens)
	}
	if s.CostCredits < 0.02 || s.CostCredits > 0.022 {
		t.Fatalf("cost = %v", s.CostCredits)
	}
	clone := s.Clone()
	if clone.PromptTokens != s.PromptTokens || clone.CostCredits != s.CostCredits {
		t.Fatalf("clone usage prompt=%d cost=%v", clone.PromptTokens, clone.CostCredits)
	}
}
