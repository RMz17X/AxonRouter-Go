package executor

import (
	"context"
	"sync"
	"time"
)

// CodeBuddy-family request pacing.
//
// Upstream paces every brand on the Tencent gateway at 120 requests per
// minute through a leaky bucket on request starts
// (open-sse/utils/providerRateLimiter.js, PROVIDER_RATE_LIMITS, applied
// by the shared CodeBuddyExecutor base — so codebuddy-cn, codebuddy-intl
// and workbuddy are all paced). The bucket fails open: when the queue is
// full (12 waiting) or the wait would exceed 2s, the request proceeds
// immediately rather than being delayed or rejected — pacing smooths
// bursts, it never becomes an availability risk.
//
// Upstream also has a Valkey-backed distributed variant for its
// multi-process cluster; this gateway is a single process, so the
// in-memory bucket below is the whole mechanism.
const (
	codebuddyPaceInterval = 500 * time.Millisecond // 120 requests/minute
	codebuddyPaceMaxQueue = 12
	codebuddyPaceMaxWait  = 2 * time.Second
)

type codebuddyPacer struct {
	mu      sync.Mutex
	nextAt  time.Time
	waiting int
}

var codebuddyPacers sync.Map // provider id -> *codebuddyPacer

func codebuddyPacerFor(provider string) *codebuddyPacer {
	if v, ok := codebuddyPacers.Load(provider); ok {
		return v.(*codebuddyPacer)
	}
	p := &codebuddyPacer{}
	actual, _ := codebuddyPacers.LoadOrStore(provider, p)
	return actual.(*codebuddyPacer)
}

// acquireCodebuddySlot reserves the provider's next pacing slot and
// sleeps until it starts. Reservation and sleep are separate so a
// cancelled context never strands a slot: the slot time stays consumed
// (the bucket is time-based, not a semaphore), only the waiter count is
// released.
func acquireCodebuddySlot(ctx context.Context, provider string) {
	if provider == "" {
		return
	}
	p := codebuddyPacerFor(provider)

	p.mu.Lock()
	now := time.Now()
	if p.waiting >= codebuddyPaceMaxQueue {
		p.mu.Unlock()
		return
	}
	start := p.nextAt
	if start.Before(now) {
		start = now
	}
	wait := time.Until(start)
	if wait > codebuddyPaceMaxWait {
		p.mu.Unlock()
		return
	}
	p.nextAt = start.Add(codebuddyPaceInterval)
	p.waiting++
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.waiting--
		p.mu.Unlock()
	}()
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
