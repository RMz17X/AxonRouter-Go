package proxy

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DeadlineHandle bounds the wait for an upstream to produce its response.
//
// The bound is a cancellation on the attempt's context, but a bound that
// could not be released would be the wrong tool. An upstream that sends its
// response headers and then streams for minutes must not be cut off halfway:
// 281 requests on 2026-11-18 ran longer than 100 seconds that way and still
// answered 200, and the longest successful run was 220.6 seconds.
//
// So the handle is a cancel-on-timeout that can be released: once the
// upstream's headers arrive, the wait it was bounding is over, and from
// there the stream is bounded by the stall watchdog instead.
//
// The bound still matters for the wait it was written for: the wait for the
// first byte, before the client has seen anything. Measured on 2026-11-18,
// 809 requests ended in a client-side timeout in 36 hours, 622 of them from
// one non-streaming client, and every one of them ended in the gateway
// waiting for an upstream that never answered.
type DeadlineHandle struct {
	timer  *time.Timer
	cancel context.CancelFunc
	fired  atomic.Bool
	once   sync.Once
}

// NewDeadlineHandle derives a context bounded by deadline and returns the
// handle that can release the bound.
//
// A zero deadline disables the bound: the context is returned unchanged and
// the handle is inert. This is the "no deadline configured" case, not an
// error.
func NewDeadlineHandle(ctx context.Context, deadline time.Time) (context.Context, *DeadlineHandle) {
	if deadline.IsZero() {
		return ctx, nil
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	h := &DeadlineHandle{cancel: cancel}
	h.timer = time.AfterFunc(time.Until(deadline), func() {
		h.fired.Store(true)
		cancel()
	})
	return WithDeadlineHandle(attemptCtx, h), h
}

// Release stops the bound without ending the context.
//
// It is called once the upstream's response headers have arrived: the wait
// for the first byte is over, and the response is now the client's business
// to read.
func (h *DeadlineHandle) Release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.timer != nil {
			h.timer.Stop()
		}
	})
}

// Cancel ends the bound and the context it was applied to. It is the
// attempt's own cleanup: the attempt is over, so nothing may keep the
// upstream connection alive.
func (h *DeadlineHandle) Cancel() {
	if h == nil {
		return
	}
	h.Release()
	if h.cancel != nil {
		h.cancel()
	}
}

// Fired reports whether the bound actually expired.
//
// The distinction matters because the transport reports an expired bound as
// a cancellation, which is indistinguishable from a client that hung up. The
// handle remembers what actually happened, so "the client left" and "the
// upstream was too slow" stay apart.
func (h *DeadlineHandle) Fired() bool {
	return h != nil && h.fired.Load()
}

// deadlineHandleKey carries the handle in the context it was applied to.
type deadlineHandleKey struct{}

// WithDeadlineHandle attaches h to ctx.
func WithDeadlineHandle(ctx context.Context, h *DeadlineHandle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deadlineHandleKey{}, h)
}

// DeadlineHandleFrom resolves the handle carried by ctx, or nil.
func DeadlineHandleFrom(ctx context.Context) *DeadlineHandle {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(deadlineHandleKey{}).(*DeadlineHandle)
	return h
}

// ReleaseDeadline releases the bound carried by ctx, if any.
//
// It is called by the request path once the upstream's response headers have
// arrived, so that the rest of the response is not held to a deadline that
// was written for the wait before the response started.
func ReleaseDeadline(ctx context.Context) {
	ReleaseDeadlineHandle(DeadlineHandleFrom(ctx))
}

// ReleaseDeadlineHandle releases h, if it is not nil.
func ReleaseDeadlineHandle(h *DeadlineHandle) {
	if h != nil {
		h.Release()
	}
}
