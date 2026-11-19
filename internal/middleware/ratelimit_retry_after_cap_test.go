package middleware

import (
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"axonrouter/proxy/internal/budget"
	"axonrouter/proxy/internal/handlerutil"
)

// TestRejectRateLimitedPublishesABoundedWait pins that the gateway's own
// rate-limit refusal publishes a wait the client will accept.
//
// The gateway's sliding window is 60 s and the concurrency queue timeout is
// 60 s, and each publishes its bound plus a second — one second past what the
// pi-ai retry helper accepts. A client that reads a wait past its ceiling
// stops retrying, so the refusal that the gateway intends to be transient
// becomes a terminal error on the client.
func TestRejectRateLimitedPublishesABoundedWait(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter int
	}{
		{name: "the window bound plus a second", retryAfter: 61},
		{name: "a long budget window", retryAfter: 1800},
		{name: "a wait at the ceiling", retryAfter: 60},
		{name: "a short wait", retryAfter: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rejectRateLimited(rec, "key-1", Limits{RPM: 10}, tt.retryAfter, "rpm")

			raw := rec.Header().Get("Retry-After")
			if raw == "" {
				t.Fatal("Retry-After header missing")
			}
			secs, err := strconv.Atoi(raw)
			if err != nil {
				t.Fatalf("Retry-After = %q, want an integer number of seconds: %v", raw, err)
			}
			if secs > handlerutil.ClientRetryAfterCapSeconds {
				t.Errorf("Retry-After = %d, want at most %d", secs, handlerutil.ClientRetryAfterCapSeconds)
			}
		})
	}
}

// TestRejectBudgetExhaustedPublishesABoundedWait pins the same bound on the
// per-period budget refusal, whose reset instant can be far out.
func TestRejectBudgetExhaustedPublishesABoundedWait(t *testing.T) {
	rec := httptest.NewRecorder()
	rejectBudgetExhausted(rec, budget.Decision{
		Axis:    "tokens",
		Limits:  budget.Limits{LimitTokens: 1000},
		Spend:   budget.Spend{Tokens: 1000},
		ResetAt: time.Now().Add(2 * time.Hour),
	})

	raw := rec.Header().Get("Retry-After")
	if raw == "" {
		t.Fatal("Retry-After header missing")
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("Retry-After = %q, want an integer number of seconds: %v", raw, err)
	}
	if secs > handlerutil.ClientRetryAfterCapSeconds {
		t.Errorf("Retry-After = %d, want at most %d", secs, handlerutil.ClientRetryAfterCapSeconds)
	}
}
