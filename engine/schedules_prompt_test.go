package engine

import (
	"strings"
	"testing"
)

func TestDefaultSummarizationPromptIsUpdatableDurableMemory(t *testing.T) {
	prompts := DefaultSummarizationPrompts()
	system := prompts.SummarySystemPrompt
	for _, want := range []string{
		"Maximum 20",
		"Prefer updating",
		"return []",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("summary system prompt missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(system), "append-only") || strings.Contains(strings.ToLower(system), "cannot edit") {
		t.Fatal("summary prompt must allow updating existing facts")
	}
	if !strings.Contains(prompts.SummaryUserPromptTemplate, "full updated list") {
		t.Fatal("user template must ask for the complete updated list")
	}
}
