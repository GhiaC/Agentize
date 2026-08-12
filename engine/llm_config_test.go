package engine

import "testing"

func TestLLMConfigMaxLLMIterations(t *testing.T) {
	tests := []struct {
		name string
		cfg  LLMConfig
		want int
	}{
		{name: "default", want: defaultMaxLLMIterations},
		{name: "configured", cfg: LLMConfig{MaxLLMIterations: 12}, want: 12},
		{name: "negative uses default", cfg: LLMConfig{MaxLLMIterations: -1}, want: defaultMaxLLMIterations},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.maxLLMIterations(); got != tt.want {
				t.Fatalf("maxLLMIterations() = %d, want %d", got, tt.want)
			}
		})
	}
}
