package proxy

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDeadlineExceeded reports that a read ran past the deadline carried by
// its context.
//
// It wraps context.DeadlineExceeded so that a caller can match it with
// errors.Is against either sentinel: the deadline fired, and the reader
// noticed it rather than blocking.
var ErrDeadlineExceeded = fmt.Errorf("read deadline exceeded: %w", context.DeadlineExceeded)

// DeadlineReader bounds the wait for an upstream body.
//
// Two modes, because the two lanes need different bounds.
//
// The streaming lane only cares about the wait before the response starts:
// once the first byte is out, the stream's own stall watchdog governs the
// gap between events, and a deadline that kept running would cut a long
// generation in half. 281 requests on 2026-11-18 ran longer than 100
// seconds and still answered 200.
//
// The non-streaming lane reads the whole body before it writes a single
// header, so the client sees nothing for the entire generation; the deadline
// stays armed until the body ends.
//
// The mechanism is a timer that closes the body. Closing is what aborts a
// read that is already blocked inside the transport, so the pending read
// fails instead of waiting for the upstream to give up on its own.
type DeadlineReader struct {
	body     io.ReadCloser
	deadline time.Time
	// detachOnFirstByte releases the deadline once the body has produced
	// its first byte. It is the streaming lane's behaviour.
	detachOnFirstByte bool

	seen bool

	closeOnce sync.Once
	stopTimer sync.Once
	// timer is published through an atomic pointer because the timer
	// callback can run before AfterFunc has returned and assigned the
	// field. The callback stops the timer, and reading the field without
	// the atomic would race with that assignment.
	timer atomic.Pointer[time.Timer]
}

// NewDeadlineReader wraps body so that the deadline stays armed until the
// body ends.
//
// This is the non-streaming lane's reader: the whole body has to arrive
// before the gateway can answer, so the whole wait is bounded.
func NewDeadlineReader(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	return newDeadlineReader(ctx, body, false)
}

// NewFirstByteDeadlineReader wraps body so that the deadline is released
// once the body produces its first byte.
//
// This is the streaming lane's reader. Before the first byte the client has
// nothing at all, and a proxy in front of the gateway closes the connection
// at its own timeout: 809 client-side timeouts in 36 hours on 2026-11-18,
// 622 of them from one non-streaming client. After the first byte the
// response has started, the stall watchdog takes over, and the deadline has
// no business cutting the generation short.
func NewFirstByteDeadlineReader(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	return newDeadlineReader(ctx, body, true)
}

func newDeadlineReader(ctx context.Context, body io.ReadCloser, detachOnFirstByte bool) io.ReadCloser {
	if body == nil {
		return nil
	}
	deadline, ok := deadlineFromContext(ctx)
	if !ok {
		return body
	}
	d := &DeadlineReader{
		body:              body,
		deadline:          deadline,
		detachOnFirstByte: detachOnFirstByte,
	}
	timer := time.AfterFunc(time.Until(deadline), d.closeBody)
	d.timer.Store(timer)
	return d
}

// deadlineFromContext resolves the instant the wait for the upstream's first
// byte runs out.
//
// It comes from the request's first-byte budget rather than from the
// attempt's own context deadline: the attempt's bound is a cancel-on-timeout
// that has no deadline of its own, because it has to be releasable once the
// upstream's headers arrive.
func deadlineFromContext(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	if d, ok := ctx.Value(firstByteDeadlineKey{}).(time.Time); ok && !d.IsZero() {
		return d, true
	}
	if dl, ok := ctx.Deadline(); ok {
		return dl, true
	}
	return time.Time{}, false
}

// firstByteDeadlineKey carries the request's first-byte deadline instant.
//
// It is declared here rather than in the handler package so that the reader
// and the handler agree on one value: the reader reads the same instant the
// handler set.
type firstByteDeadlineKey struct{}

// WithFirstByteDeadlineAt attaches the deadline instant to ctx.
//
// The handler package sets it; the proxy package reads it. The key type is
// unexported on purpose: the value is written through the exported
// function, so nothing outside can forge it.
func WithFirstByteDeadlineAt(ctx context.Context, deadline time.Time) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, firstByteDeadlineKey{}, deadline)
}

// Read implements io.Reader.
func (r *DeadlineReader) Read(p []byte) (int, error) {
	if !r.seen {
		if !time.Now().Before(r.deadline) {
			return 0, ErrDeadlineExceeded
		}
		// The timer closes the body to unblock a read that is already in
		// flight. The read then fails with the transport's own "closed
		// body" error, which says nothing about why; the deadline explains
		// it, so the error is translated here.
		n, err := r.body.Read(p)
		if err != nil && !time.Now().Before(r.deadline) {
			return n, ErrDeadlineExceeded
		}
		if n > 0 {
			r.seen = true
			if r.detachOnFirstByte {
				r.releaseDeadline()
			}
		}
		return n, err
	}
	return r.body.Read(p)
}

// Close implements io.Closer.
func (r *DeadlineReader) Close() error {
	r.closeBody()
	return nil
}

// releaseDeadline stops the timer without closing the body. The body stays
// open because the response has started and the client is reading it.
func (r *DeadlineReader) releaseDeadline() {
	r.stopTimer.Do(func() {
		if t := r.timer.Load(); t != nil {
			t.Stop()
		}
	})
}

// closeBody closes the underlying body once, so that a deadline and the
// serving path's own cleanup cannot both tear the same body down.
func (r *DeadlineReader) closeBody() {
	r.closeOnce.Do(func() {
		r.stopTimer.Do(func() {
			if t := r.timer.Load(); t != nil {
				t.Stop()
			}
		})
		_ = r.body.Close()
	})
}
