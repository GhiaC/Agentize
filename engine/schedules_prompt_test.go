package engine

import (
	"strings"
	"testing"
)

func TestDefaultSummarizationPromptIsAppendOnlyDurableMemory(t *testing.T) {
	prompts := DefaultSummarizationPrompts()
	system := prompts.SummarySystemPrompt
	for _, want := range []string{
		"append-only",
		"cannot edit",
		"return []",
		"proper name",
		"notable action",
	} {
		if !strings.Contains(strings.ToLower(system), strings.ToLower(want)) {
			t.Errorf("summary system prompt missing %q", want)
		}
	}
	if !strings.Contains(prompts.SummaryUserPromptTemplate, "permanent memory") {
		t.Fatal("user template must tell the model this is permanent memory")
	}
}
