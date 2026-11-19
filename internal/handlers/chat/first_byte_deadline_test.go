package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"axonrouter/proxy/internal/proxy"
)

// TestResolveFirstByteDeadline pins the environment override's contract.
//
// A typo in a deployment variable must not silently reintroduce the 524 the
// deadline exists to prevent, so an unparseable value falls back to the
// default rather than disabling the bound.
func TestResolveFirstByteDeadline(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		env  string
		want time.Duration
	}{
		{name: "unset uses the default", set: false, want: DefaultFirstByteDeadline},
		{name: "empty uses the default", set: true, env: "", want: DefaultFirstByteDeadline},
		{name: "positive value is honoured", set: true, env: "45000", want: 45 * time.Second},
		{name: "zero disables the bound", set: true, env: "0", want: 0},
		{name: "negative falls back to the default", set: true, env: "-1", want: DefaultFirstByteDeadline},
		{name: "garbage falls back to the default", set: true, env: "ninety", want: DefaultFirstByteDeadline},
		{name: "surrounding space is tolerated", set: true, env: "  30000  ", want: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(firstByteDeadlineEnv, tt.env)
			}
			if got := ResolveFirstByteDeadline(); got != tt.want {
				t.Errorf("ResolveFirstByteDeadline() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFirstByteBudgetIsSharedAcrossAttempts pins the property the deadline
// exists for: the budget belongs to the request, not to an attempt.
//
// A fallback chain that rotated through candidates must not be able to add
// their waits together past the bound. Measured on 2026-11-18, the edge
// answered 809 requests with a 524 because the gateway was still waiting.
func TestFirstByteBudgetIsSharedAcrossAttempts(t *testing.T) {
	ctx := WithFirstByteDeadline(context.Background(), 90*time.Second)

	first := firstByteBudgetFromContext(ctx)
	if first.deadline.IsZero() {
		t.Fatal("firstByteBudgetFromContext returned a zero budget")
	}

	// A second attempt derives from the same request context, so it gets
	// the same instant rather than a fresh window.
	attemptCtx, cancel := firstByteAttemptContext(ctx)
	defer cancel()

	attempt := firstByteBudgetFromContext(attemptCtx)
	if !attempt.deadline.Equal(first.deadline) {
		t.Errorf("attempt deadline = %v, want %v", attempt.deadline, first.deadline)
	}
}

// TestFirstByteAttemptContextIsBounded proves the attempt context is actually
// bounded: a request that outlives the budget must have its attempt aborted,
// not left waiting on the upstream.
func TestFirstByteAttemptContextIsBounded(t *testing.T) {
	ctx := WithFirstByteDeadline(context.Background(), 50*time.Millisecond)

	attemptCtx, cancel := firstByteAttemptContext(ctx)
	defer cancel()

	select {
	case <-attemptCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("attempt context was not bounded by the first-byte deadline")
	}
}

// TestFirstByteDeadlineIsReleasedAfterFirstByte proves the bound does not cut
// a long generation in half.
//
// The deadline bounds the wait for the upstream's first byte. Once the
// upstream's response headers have arrived, the wait is over and the
// response may stream for as long as it needs: 281 requests on 2026-11-18
// ran longer than 100 seconds and still answered 200.
func TestFirstByteDeadlineIsReleasedAfterFirstByte(t *testing.T) {
	ctx := WithFirstByteDeadline(context.Background(), 50*time.Millisecond)

	attemptCtx, cancel := firstByteAttemptContext(ctx)
	defer cancel()

	handle := proxy.DeadlineHandleFrom(attemptCtx)
	if handle == nil {
		t.Fatal("attempt context carries no deadline handle")
	}

	// The upstream answered: release the bound.
	proxy.ReleaseDeadline(attemptCtx)

	time.Sleep(150 * time.Millisecond)

	if handle.Fired() {
		t.Error("handle reported the bound as expired after it was released")
	}
	if attemptCtx.Err() != nil {
		t.Errorf("attempt context ended with %v after the bound was released", attemptCtx.Err())
	}
}

// TestFirstByteDeadlineFiresWhenUpstreamIsSilent proves the bound fires when
// the upstream does not answer, and that the failure says so.
func TestFirstByteDeadlineFiresWhenUpstreamIsSilent(t *testing.T) {
	ctx := WithFirstByteDeadline(context.Background(), 50*time.Millisecond)

	attemptCtx, cancel := firstByteAttemptContext(ctx)
	defer cancel()

	select {
	case <-attemptCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the bound did not fire against a silent upstream")
	}

	handle := proxy.DeadlineHandleFrom(attemptCtx)
	if handle == nil || !handle.Fired() {
		t.Fatal("the handle did not record that the bound expired")
	}

	// The transport reports an expired bound as a cancellation, which is
	// indistinguishable from a client that hung up. The classifier has to
	// tell them apart, or a slow provider gets recorded as a broken account.
	timedOut, waited := firstByteTimeoutError(attemptCtx, context.Canceled)
	if !timedOut {
		t.Error("firstByteTimeoutError did not recognise the expired bound")
	}
	if waited < 0 {
		t.Errorf("waited = %v, want a non-negative duration", waited)
	}
}

// TestFirstByteTimeoutErrorIsNotAnUpstreamError pins the property that keeps
// a slow provider out of the lock path.
//
// Every status in providers.RetryableStatusCodes makes the fallback layer
// cool the account down and count the failure against the provider breaker.
// A timeout is evidence of neither, so it must not carry an upstream status.
func TestFirstByteTimeoutErrorIsNotAnUpstreamError(t *testing.T) {
	err := firstByteTimeoutUpstreamError(2 * time.Second)

	var ue *upstreamError
	if errors.As(err, &ue) {
		t.Error("the timeout error carries an upstream status, which would lock the account")
	}
	if !errors.Is(err, ErrFirstByteTimeout) {
		t.Error("the timeout error does not match ErrFirstByteTimeout")
	}
}

// TestTerminalFailureClassification pins the classes the structured terminal
// log line reports.
//
// The 2026-11-18 incident left 96 of 245 chat 502s unattributable because
// the terminal log line carried only a status and a duration.
func TestTerminalFailureClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want terminalFailureClass
	}{
		{name: "no connection", err: errors.New("no active connections for provider: clixad"), want: failureClassNoConnection},
		{name: "unhealthy provider", err: errors.New("provider minimax-code-global/MiniMax-M3 is unhealthy"), want: failureClassProviderUnhealthy},
		{name: "pinned ineligible", err: errors.New("pinned connection abc: connection is ineligible"), want: failureClassPinnedIneligible},
		{name: "combo exhausted", err: errors.New("all combo models failed: no valid entries"), want: failureClassComboExhausted},
		{name: "first byte timeout", err: firstByteTimeoutUpstreamError(time.Second), want: failureClassFirstByteTimeout},
		{name: "anything else", err: errors.New("upstream error: something else"), want: failureClassUpstreamFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyTerminalFailure(tt.err); got != tt.want {
				t.Errorf("classifyTerminalFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}
