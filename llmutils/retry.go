package llmutils

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
)

const (
	// DefaultLLMMaxRetries is the default number of retries for LLM calls (3 attempts total).
	DefaultLLMMaxRetries = 3
	// DefaultLLMInitialBackoff is the initial backoff duration before first retry.
	DefaultLLMInitialBackoff = 1 * time.Second
	// TurnNetworkRetryMax is how many extra attempts a conversation turn makes
	// after a fast network failure. Payment, auth, and access errors are never retried.
	TurnNetworkRetryMax = 10
	// TurnNetworkRetryUnit is multiplied by the 1-based retry count (30s, 60s, …).
	TurnNetworkRetryUnit = 30 * time.Second
	// ClientTimeoutRetryMax is how many extra attempts follow an http.Client
	// deadline. Those attempts already waited for the whole completion, so the
	// budget stays small.
	ClientTimeoutRetryMax = 2
	// ClientTimeoutRetryDelay is the pause before repeating a client-timeout.
	ClientTimeoutRetryDelay = 5 * time.Second
)

// afterRetry waits for a backoff. Tests replace it to avoid sleeping 30s+.
var afterRetry = time.After

// isRetriableError returns true if the error is transient and worth retrying.
func isRetriableError(err error) bool {
	return IsRetriableNetworkError(err)
}

// IsRetriableNetworkError reports a dropped connection, timeout, or 5xx/429.
// Payment, credential, and access-denied errors must fail closed immediately.
// A caller-cancelled turn is not a transient network blip. http.Client.Timeout
// also reports itself as context.DeadlineExceeded, so that match is checked
// only after a real client timeout has been recognized.
func IsRetriableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if isPaymentOrAccessError(msg) {
		return false
	}
	if IsClientTimeout(err) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "temporary failure") ||
		strings.Contains(msg, "server misbehaving") {
		return true
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPStatusCode == 401 || apiErr.HTTPStatusCode == 402 || apiErr.HTTPStatusCode == 403 || apiErr.HTTPStatusCode == 404 {
			return false
		}
		if apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode >= 500 {
			return true
		}
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		if reqErr.HTTPStatusCode == 401 || reqErr.HTTPStatusCode == 402 || reqErr.HTTPStatusCode == 403 {
			return false
		}
		if reqErr.HTTPStatusCode == 429 || reqErr.HTTPStatusCode >= 500 || reqErr.HTTPStatusCode == 0 {
			return true
		}
	}
	return false
}

// IsClientTimeout reports http.Client.Timeout, including the failure that
// happens while the response body is still being read. Go marks that error as
// context.DeadlineExceeded, and in Go 1.25 the caller's own deadline is also a
// net.Error, so the message is what separates a client timer from a turn the
// caller already ended.
func IsClientTimeout(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "client.timeout") ||
		strings.Contains(msg, "while reading body") ||
		strings.Contains(msg, "awaiting headers") ||
		strings.Contains(msg, "timeout awaiting response")
}

func isPaymentOrAccessError(msg string) bool {
	for _, needle := range []string{
		"payment required",
		"insufficient credit",
		"insufficient quota",
		"credits exhausted",
		"credit exhausted",
		"billing",
		"unauthorized",
		"forbidden",
		"access denied",
		"permission denied",
		"invalid api key",
		"incorrect api key",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// NetworkRetryDelay is 30s * retryCount, clamped to TurnNetworkRetryMax.
func NetworkRetryDelay(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	if retryCount > TurnNetworkRetryMax {
		retryCount = TurnNetworkRetryMax
	}
	return TurnNetworkRetryUnit * time.Duration(retryCount)
}

// RetryNetworkError waits retryCount * 30s unless the context is cancelled.
func RetryNetworkError(ctx context.Context, retryCount int) error {
	return Wait(ctx, NetworkRetryDelay(retryCount))
}

// Wait pauses for d unless the context is cancelled. Tests replace afterRetry.
func Wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-afterRetry(d):
		return nil
	}
}

// CreateChatCompletionWithRetry calls client.CreateChatCompletion with up to DefaultLLMMaxRetries
// attempts and exponential backoff on retriable errors. All LLM calls should go through this
// for consistent resilience (e.g. connection reset by peer).
func CreateChatCompletionWithRetry(ctx context.Context, client LLMClient, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	var lastErr error
	backoff := DefaultLLMInitialBackoff
	for attempt := 0; attempt < DefaultLLMMaxRetries; attempt++ {
		resp, err := client.CreateChatCompletion(ctx, request)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetriableError(err) || attempt == DefaultLLMMaxRetries-1 {
			return openai.ChatCompletionResponse{}, err
		}
		select {
		case <-ctx.Done():
			return openai.ChatCompletionResponse{}, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
	return openai.ChatCompletionResponse{}, lastErr
}
