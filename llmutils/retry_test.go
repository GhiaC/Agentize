package llmutils

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

func TestIsRetriableNetworkError(t *testing.T) {
	if IsRetriableNetworkError(nil) {
		t.Fatal("nil must not retry")
	}
	if !IsRetriableNetworkError(&net.OpError{Op: "dial", Err: errors.New("connection refused")}) {
		t.Fatal("dial refused must retry")
	}
	if !IsRetriableNetworkError(errors.New("connection reset by peer")) {
		t.Fatal("reset must retry")
	}
	if !IsRetriableNetworkError(&openai.APIError{HTTPStatusCode: 503, Message: "unavailable"}) {
		t.Fatal("503 must retry")
	}
	if !IsRetriableNetworkError(&openai.APIError{HTTPStatusCode: 429, Message: "rate"}) {
		t.Fatal("429 must retry")
	}
	if IsRetriableNetworkError(&openai.APIError{HTTPStatusCode: 401, Message: "unauthorized"}) {
		t.Fatal("401 must not retry")
	}
	if IsRetriableNetworkError(&openai.APIError{HTTPStatusCode: 402, Message: "payment required"}) {
		t.Fatal("402 must not retry")
	}
	if IsRetriableNetworkError(&openai.APIError{HTTPStatusCode: 403, Message: "forbidden"}) {
		t.Fatal("403 must not retry")
	}
	if IsRetriableNetworkError(errors.New("insufficient credit for this model")) {
		t.Fatal("credit errors must not retry")
	}
	if IsRetriableNetworkError(context.Canceled) {
		t.Fatal("canceled must not retry")
	}
}

func TestNetworkRetryDelay(t *testing.T) {
	if NetworkRetryDelay(1) != 30*time.Second {
		t.Fatalf("first retry = %s", NetworkRetryDelay(1))
	}
	if NetworkRetryDelay(2) != 60*time.Second {
		t.Fatalf("second retry = %s", NetworkRetryDelay(2))
	}
	if NetworkRetryDelay(10) != 300*time.Second {
		t.Fatalf("tenth retry = %s", NetworkRetryDelay(10))
	}
	if NetworkRetryDelay(99) != 300*time.Second {
		t.Fatalf("clamped retry = %s", NetworkRetryDelay(99))
	}
}

func TestRetryNetworkErrorHonorsCancel(t *testing.T) {
	prev := afterRetry
	afterRetry = func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time)
		return ch
	}
	t.Cleanup(func() { afterRetry = prev })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RetryNetworkError(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
