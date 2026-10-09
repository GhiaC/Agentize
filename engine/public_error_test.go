package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestPublicTurnErrorHidesClientTimeout(t *testing.T) {
	raw := errors.New("LLM request failed: net/http: request canceled (Client.Timeout or context cancellation while reading body)")
	if got := PublicTurnError(raw); got != PublicModelTimeout {
		t.Fatalf("timeout detail = %q", got)
	}
	kept := "The last step did not finish. Retry continues from there."
	if got := PublicTurnErrorText(kept); got != kept {
		t.Fatalf("kept sentence = %q", got)
	}
	if got := PublicTurnErrorText("LLM request failed: error, status code: 402, message: Insufficient credits"); !strings.Contains(strings.ToLower(got), "insufficient credits") {
		t.Fatalf("credit detail = %q", got)
	}
	if got := PublicTurnErrorText("context canceled"); got != PublicTurnStopped {
		t.Fatalf("cancel detail = %q", got)
	}
	if got := PublicTurnErrorText("dial tcp 10.0.0.1:443: connect: connection refused"); got != PublicTurnFailed {
		t.Fatalf("internal detail = %q", got)
	}
}
