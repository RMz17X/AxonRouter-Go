package log

import (
	"context"
	"io"
)

// ProgressGate is the slice of a stall watchdog the serving paths need: a
// single "the upstream just showed a sign of life" ping. The combo response
// timeout gate implements it; any serving path can report progress without
// knowing the gate exists, which is what lets one registry serve the chat
// handlers, the shared proxy senders and the provider executors alike.
type ProgressGate interface {
	Progress()
}

// progressGateCtxKey registers the active request's progress gate in its
// context. Unexported: registration and notes go through this file only, so
// there is exactly one registry mechanism.
type progressGateCtxKey struct{}

// RegisterProgressGate returns a context carrying gate. A nil context or a
// nil gate returns the context unchanged.
func RegisterProgressGate(ctx context.Context, gate ProgressGate) context.Context {
	if ctx == nil || gate == nil {
		return ctx
	}
	return context.WithValue(ctx, progressGateCtxKey{}, gate)
}

// progressGateFromContext resolves the gate registered by
// RegisterProgressGate, or nil when the context carries none — the case for
// direct (non-combo) requests and a combo's last candidate.
func progressGateFromContext(ctx context.Context) ProgressGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(progressGateCtxKey{}).(ProgressGate)
	return gate
}

// NoteUpstreamProgress tells the progress gate registered in ctx (if any)
// that the upstream just showed a sign of life, restarting its stall window.
// Contexts without a gate make it a no-op.
func NoteUpstreamProgress(ctx context.Context) {
	if gate := progressGateFromContext(ctx); gate != nil {
		gate.Progress()
	}
}

// ProgressReader wraps an upstream response body so every Read that returns
// bytes notes progress on the gate registered in ctx. Applied at the point a
// serving path receives the response, it covers every downstream read —
// stream scans, single-shot body reads, bridges fed from the body — without
// each consumer having to ping on its own.
//
// When ctx carries no gate the body is returned unwrapped: the bytes and
// the read behaviour are exactly the original's, so direct requests pay
// nothing for the mechanism.
func ProgressReader(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	gate := progressGateFromContext(ctx)
	if gate == nil || body == nil {
		return body
	}
	return &progressReadCloser{gate: gate, body: body}
}

// progressReadCloser is the wrapped body ProgressReader hands out. It is a
// pure pass-through apart from the progress note: same bytes, same errors,
// same Close.
type progressReadCloser struct {
	gate ProgressGate
	body io.ReadCloser
}

func (r *progressReadCloser) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		r.gate.Progress()
	}
	return n, err
}

func (r *progressReadCloser) Close() error { return r.body.Close() }
