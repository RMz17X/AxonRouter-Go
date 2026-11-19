package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"axonrouter/proxy/internal/log"
	internalproxy "axonrouter/proxy/internal/proxy"
)

// ErrFirstByteTimeout reports that the upstream did not produce its first
// byte within the gateway's first-byte deadline.
//
// It is a distinct error because the fallback layer has to treat it
// differently from a provider failure: the provider did not answer wrongly,
// it answered too late, and the account is not at fault for it. The
// connection is excluded for this turn so the chain moves on, but no lock or
// cooldown is written — a slow provider is not a broken account.
var ErrFirstByteTimeout = errors.New("upstream did not produce a first byte before the deadline")

// DefaultFirstByteDeadline bounds how long the gateway waits for an
// upstream's first byte before it answers the client itself.
//
// The problem it solves: a reverse proxy in front of the gateway (this
// deployment runs behind Cloudflare) closes an idle connection at its own
// fixed edge timeout. When that edge timeout is shorter than the gateway's
// own wait for the upstream, the edge answers the client with 524 while the
// gateway is still waiting — a status the client cannot act on, and a failure
// the gateway never sees, because the upstream had not answered yet.
//
// Measured against the 2026-11-18 incident on axon.hijitoko.com: 809 such
// responses in 36 hours, 622 of them from one non-streaming client, and 562
// requests in the application log ending at exactly 125 s.
//
// The deadline is absolute for the whole request, not a stall window: it
// covers the entire wait for the first byte of the chain, so a fallback
// chain that rotates through several candidates cannot add their waits
// together past the edge timeout.
//
// It is deliberately below the edge timeout rather than equal to it. The
// gateway has to answer, flush, and have the edge read that answer before the
// edge's own clock runs out, so the deadline leaves room for the round trip
// instead of racing it.
//
// A value of 0 disables the deadline, which restores the previous behaviour
// (wait indefinitely and let the edge cut the connection). That is a
// deliberate operator choice for a gateway whose edge has a longer idle
// timeout than the default.
const DefaultFirstByteDeadline = 90 * time.Second

// firstByteDeadlineEnv names the environment variable that overrides the
// default.
const firstByteDeadlineEnv = "FIRST_BYTE_DEADLINE_MS"

// ResolveFirstByteDeadline returns the bound on the wait for the upstream's
// first byte.
//
// The environment variable wins over the default when it holds a positive
// number of milliseconds. A value of 0 disables the deadline entirely, which
// is a deliberate operator choice and not a parse failure.
//
// Anything unparseable falls back to the default rather than disabling the
// deadline: a typo in a deployment variable must not silently reintroduce the
// 524 the deadline exists to prevent.
func ResolveFirstByteDeadline() time.Duration {
	raw, ok := os.LookupEnv(firstByteDeadlineEnv)
	if !ok {
		return DefaultFirstByteDeadline
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultFirstByteDeadline
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		log.Warn("firstbyte", "ignoring unparseable deadline", "env", firstByteDeadlineEnv, "value", raw)
		return DefaultFirstByteDeadline
	}
	if ms == 0 {
		return 0
	}
	if ms < 0 {
		log.Warn("firstbyte", "ignoring negative deadline", "env", firstByteDeadlineEnv, "value", raw)
		return DefaultFirstByteDeadline
	}
	return time.Duration(ms) * time.Millisecond
}

// firstByteBudget is the request's first-byte budget: the instant the wait
// started and the instant it runs out.
//
// It travels as a context value rather than as a parameter because the wait
// happens deep inside the serving paths — the transport, the peek, the body
// read — and each of them already receives the request context. A parameter
// would have to be threaded through every layer of the forward path.
//
// The budget belongs to the request, not to an attempt. A fallback chain that
// rotates through candidates shares one budget instead of spending a fresh
// one per candidate, which is what keeps the total silent gap under the
// edge's own idle timeout.
type firstByteBudget struct {
	started  time.Time
	deadline time.Time
}

// remaining reports how much of the budget is left. A zero budget — the
// deadline disabled — reports an unlimited wait.
func (b firstByteBudget) remaining() time.Duration {
	if b.deadline.IsZero() {
		return 0
	}
	return time.Until(b.deadline)
}

// spent reports whether the budget is gone.
func (b firstByteBudget) spent() bool {
	if b.deadline.IsZero() {
		return false
	}
	return !time.Now().Before(b.deadline)
}

// waited reports how long the request has been waiting for its first byte.
func (b firstByteBudget) waited() time.Duration {
	if b.started.IsZero() {
		return 0
	}
	return time.Since(b.started)
}

// firstByteDeadlineKey carries the request's first-byte budget in its context.
type firstByteDeadlineKey struct{}

// WithFirstByteDeadline returns a context whose first-byte budget is the
// given duration from now.
//
// A duration of 0 disables the deadline and leaves the context unchanged.
func WithFirstByteDeadline(ctx context.Context, d time.Duration) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if d <= 0 {
		return ctx
	}
	now := time.Now()
	deadline := now.Add(d)
	ctx = context.WithValue(ctx, firstByteDeadlineKey{}, firstByteBudget{
		started:  now,
		deadline: deadline,
	})
	// The proxy package reads the same instant, so that the body reader
	// bounds the wait by the request's budget rather than by a deadline of
	// its own.
	return internalproxy.WithFirstByteDeadlineAt(ctx, deadline)
}

// firstByteBudgetFromContext resolves the budget carried by ctx, or the zero
// budget when the context carries none.
func firstByteBudgetFromContext(ctx context.Context) firstByteBudget {
	if ctx == nil {
		return firstByteBudget{}
	}
	b, _ := ctx.Value(firstByteDeadlineKey{}).(firstByteBudget)
	return b
}

// firstByteDeadlineError is the error a timed-out attempt reports. It wraps
// ErrFirstByteTimeout so callers can match it with errors.Is.
type firstByteDeadlineError struct {
	waited time.Duration
}

func (e *firstByteDeadlineError) Error() string {
	return fmt.Sprintf("%v after %s", ErrFirstByteTimeout, e.waited.Round(time.Millisecond))
}

func (e *firstByteDeadlineError) Unwrap() error { return ErrFirstByteTimeout }

// firstByteTimeoutErrorFor builds the error a timed-out attempt reports.
//
// It is deliberately NOT an *upstreamError. The upstream error type carries
// an HTTP status, and every status in providers.RetryableStatusCodes leads
// the fallback layer to cool the account down and to count the failure
// against the provider breaker. A timeout is evidence of neither: the
// upstream did not answer, so it said nothing about the credential and
// nothing about the provider's health. A separate type keeps the account
// out of the lock path while still letting the client see a 504.
//
// The client-facing status is attached to the error so the terminal writer
// can use it without re-deriving the classification.
func firstByteTimeoutErrorFor(waited time.Duration) *firstByteDeadlineError {
	return &firstByteDeadlineError{waited: waited}
}

// firstByteAttemptContext derives the context for one upstream attempt from
// the request's first-byte deadline.
//
// The bound is a cancel-on-timeout that can be released, not a plain context
// deadline: the upstream's response headers end the wait this bound was
// written for, and from there the response may stream for minutes. A plain
// deadline would cut a long generation in half. 281 requests on 2026-11-18
// ran longer than 100 seconds and still answered 200, and the longest
// successful run was 220.6 seconds.
//
// Cancelling the attempt context also closes the upstream connection, which
// is what keeps a timed-out attempt from holding a socket and a goroutine
// for the rest of the chain.
//
// The returned cancel function must be called when the attempt ends.
func firstByteAttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	b := firstByteBudgetFromContext(ctx)
	if b.deadline.IsZero() {
		return ctx, func() {}
	}
	attemptCtx, handle := internalproxy.NewDeadlineHandle(ctx, b.deadline)
	return attemptCtx, handle.Cancel
}

// firstByteBudgetSpent reports whether the request's first-byte budget is
// already gone.
//
// A chain that reaches this point has nothing left to wait for: the next
// attempt would start against an expired deadline and fail immediately, so
// the caller stops instead of walking the remaining candidates.
func firstByteBudgetSpent(ctx context.Context) (bool, time.Duration) {
	b := firstByteBudgetFromContext(ctx)
	if b.deadline.IsZero() {
		return false, 0
	}
	return b.spent(), b.waited()
}

// firstByteTimeoutError classifies a failed attempt: it reports whether the
// attempt died because the first-byte deadline fired.
//
// The deadline fires as a context deadline on the attempt, and the upstream
// error surfaces as a transport failure. The distinction matters to the
// fallback layer: a deadline is not a provider fault, so it must exclude the
// connection for this turn without locking it, and it must report a timeout
// to the client rather than a generic upstream error.
func firstByteTimeoutError(ctx context.Context, err error) (bool, time.Duration) {
	if err == nil {
		return false, 0
	}
	if errors.Is(err, ErrFirstByteTimeout) {
		var de *firstByteDeadlineError
		if errors.As(err, &de) {
			return true, de.waited
		}
		return true, 0
	}
	// The attempt context carries the deadline, so a deadline that fired
	// surfaces as a context deadline error on the read. The request context
	// is the one that holds the budget, so the check reads it from there.
	//
	// The body read reports the miss as the proxy package's own sentinel:
	// Go's transport does not interrupt an in-flight body read on context
	// cancellation, so the reader is closed instead, and the read fails with
	// that sentinel rather than with context.DeadlineExceeded.
	if errors.Is(err, internalproxy.ErrDeadlineExceeded) {
		b := firstByteBudgetFromContext(ctx)
		return true, b.waited()
	}
	// The handle knows whether the bound actually expired, which the
	// transport's cancellation error alone cannot say: a client that hung up
	// produces the same error.
	if h := internalproxy.DeadlineHandleFrom(ctx); h != nil && h.Fired() {
		b := firstByteBudgetFromContext(ctx)
		return true, b.waited()
	}
	b := firstByteBudgetFromContext(ctx)
	if b.deadline.IsZero() {
		return false, 0
	}
	if errors.Is(err, context.DeadlineExceeded) && b.spent() {
		return true, b.waited()
	}
	return false, 0
}

// firstByteTimeoutUpstreamError builds the client-facing error for a chain
// that ran out of time before any upstream produced a byte.
//
// It is a 504: the gateway waited and the upstream did not answer in time.
// The status is deliberately not 502, because 502 says "the upstream answered
// and the answer was bad", and no upstream answered at all.
//
// The header carries how long the request waited, so a client that sees the
// timeout can tell a slow upstream from a stuck one, and so the operator can
// line the number up against the gateway's own budget.
func firstByteTimeoutUpstreamError(waited time.Duration) *firstByteDeadlineError {
	return &firstByteDeadlineError{waited: waited}
}
