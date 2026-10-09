package engine

import (
	"errors"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// ErrInvalidFinalOutput means the provider returned no valid final answer and
// no structured tool call. The turn stays recoverable. Tool names written as
// prose are never executed.
var ErrInvalidFinalOutput = errors.New("assistant output was not a valid final answer")

const invalidOutputCorrection = "The previous model response was not a valid final answer. Do not describe tool calls as text such as [Tool Calls: ...]. Use the structured tool interface, or write the visible answer for the user. Do not invent tool arguments."

// maxInvalidOutputContinuations is how many extra in-turn model calls may
// follow an invalid completion before the turn returns ErrInvalidFinalOutput.
// This is a same-process guard, not the durable ten-retry budget.
const maxInvalidOutputContinuations = 2

type assistantChoiceKind int

const (
	assistantChoiceInvalid assistantChoiceKind = iota
	assistantChoiceTools
	assistantChoiceFinal
)

// classifyAssistantChoice decides whether a provider choice may execute tools,
// commit a visible answer, or must be continued as invalid output.
// Structured tool calls win even when finish_reason disagrees. Prose that
// merely looks like a tool list is never turned into a call.
func classifyAssistantChoice(choice openai.ChatCompletionChoice) assistantChoiceKind {
	if len(choice.Message.ToolCalls) > 0 {
		return assistantChoiceTools
	}
	content := strings.TrimSpace(choice.Message.Content)
	if content == "" || isToolOnlyPlaceholder(content) || providerProtocolLeak(content) {
		return assistantChoiceInvalid
	}
	switch choice.FinishReason {
	case openai.FinishReasonLength, openai.FinishReasonContentFilter, openai.FinishReasonToolCalls:
		// A tool finish with no structured call is not permission to parse the
		// sentence into a tool, and it is not a final answer.
		return assistantChoiceInvalid
	}
	return assistantChoiceFinal
}

func isToolOnlyPlaceholder(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "[Tool Calls:") && strings.HasSuffix(content, "]")
}

func providerProtocolLeak(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	return strings.HasPrefix(lower, "to=functions.") ||
		strings.HasPrefix(lower, "commentary to=functions.") ||
		strings.HasPrefix(lower, "assistantcommentary to=functions.")
}
