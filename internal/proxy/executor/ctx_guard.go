package executor

import (
	"context"
	"errors"
)

// ctx-cancel guards for executor retry loops.
//
// The executor retry paths (perch 401, freebuff stale-session, etc.) must
// abort cleanly when the caller hangs up. The handler layer already stops
// retrying on ctx cancel (upstream #5639 / commit eb354dc5e), but the
// executor-level replay loops were still burning attempts and mutating
// shared state (ticket cache, session lease) on a cancelled context.
//
// guardRetryAttempt checks the context before an executor retry attempt.
// It returns the context's error when the request is already dead, and
// nil when the attempt may proceed.

// ctxAborted reports whether ctx has ended, treating a nil context as alive.
func ctxAborted(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}

// ctxAbortError returns the context's own error (context.Canceled or
// context.DeadlineExceeded) when ctx has ended, or nil when alive.
func ctxAbortError(ctx context.Context) error {
	if !ctxAborted(ctx) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

// guardRetryAttempt is the single guard for all executor retry/replay paths.
// It returns the abort error when the caller's context has ended,
// and nil when the attempt may go out.
//
// Example usage in a retry loop:
//
//	if err := guardRetryAttempt(ctx); err != nil {
//	    return err
//	}
//
// The return value is the context's error so the abort surfaces as the
// abort (not as an upstream status), preserving the handler-layer contract
// from upstream #5639.
func guardRetryAttempt(ctx context.Context) error {
	return ctxAbortError(ctx)
}

// isClientAbort reports whether err is the caller's own cancellation rather
// than a provider failure. It checks both the context and the error chain.
func isClientAbort(ctx context.Context, err error) bool {
	if ctxAborted(ctx) {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}