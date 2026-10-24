package engine

import "strings"

const (
	// PublicIncompleteResponse also covers failures stored by older versions.
	PublicIncompleteResponse = "The AI provider returned an incomplete response. Retry continues from the last step."
	// PublicModelTimeout is the chat-facing sentence for an http.Client deadline
	// that expired while the provider was still writing the completion.
	PublicModelTimeout = "The model ran out of time while writing its reply. Retry continues from the last step."
	// PublicProviderError is the chat-facing sentence for any other LLM failure
	// whose raw text would expose transport or provider internals.
	PublicProviderError = "The AI provider could not complete this request. Please try again."
	// PublicTurnFailed is the chat-facing sentence when the failure is internal
	// and has no more specific public form.
	PublicTurnFailed = "The assistant could not complete this turn."
	// PublicTurnStopped is the chat-facing sentence when the caller cancelled the turn.
	PublicTurnStopped = "This turn was stopped."
)

// PublicTurnError is the sentence persisted on a conversation and shown in the
// chat. The raw error stays in the server log.
func PublicTurnError(err error) string {
	if err == nil {
		return ""
	}
	return PublicTurnErrorText(err.Error())
}

// PublicTurnErrorText maps an already-formatted failure onto a stable sentence.
// Sentences this package and the host already wrote are kept as-is.
func PublicTurnErrorText(msg string) string {
	text := strings.TrimSpace(msg)
	if text == "" {
		return PublicTurnFailed
	}
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "no choices in llm response") || strings.Contains(low, "llm response has no choices"):
		return PublicIncompleteResponse
	case isModelTimeoutText(low):
		return PublicModelTimeout
	case strings.Contains(low, "llm request failed"):
		return publicProviderText(low)
	case strings.Contains(low, "context canceled") || strings.Contains(low, "context cancelled"):
		return PublicTurnStopped
	case looksInternalError(low):
		return PublicTurnFailed
	default:
		return text
	}
}

func isModelTimeoutText(low string) bool {
	return strings.Contains(low, "client.timeout") ||
		strings.Contains(low, "while reading body") ||
		strings.Contains(low, "awaiting headers") ||
		strings.Contains(low, "timeout awaiting response")
}

func publicProviderText(low string) string {
	switch {
	case strings.Contains(low, "status code: 402") || strings.Contains(low, "insufficient credit"):
		return "The configured AI provider has insufficient credits. Add credits or select a funded model, then try again."
	case strings.Contains(low, "status code: 429"):
		return "The AI provider is rate-limiting requests. Wait a moment, then try again."
	case strings.Contains(low, "status code: 401") || strings.Contains(low, "status code: 403"):
		return "The configured AI provider rejected its credentials. Verify the API key and model access."
	default:
		return PublicProviderError
	}
}

func looksInternalError(low string) bool {
	for _, needle := range []string{
		"net/http",
		"net/url",
		"runtime error",
		"sql:",
		"dial tcp",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}
