package speedlayer

import (
	"context"
	"sync"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/shutdown"
)

// failover.go implements the degradation rule the cluster design locks:
// when the Redis primary fails, the speed layer degrades to the DATABASE
// backend — never to per-node memory. Per-node memory would let each
// node's counters, cursors, and locks walk apart silently; the database
// is slower and always shared, which is the correct trade for state
// whose entire purpose is cross-node agreement.
//
// A circuit breaker decides which backend serves: after failThreshold
// primary failures inside failWindow the breaker opens (traffic moves
// to the database, one log line names the transition), and a background prober
// pings the primary every probeInterval; the first success closes the
// breaker (second log line) and the primary resumes. Both transitions
// are also visible through Status() in the dashboard's system payload.

const (
	failThreshold  = 3
	probeInterval  = 5 * time.Second
	breakerTripLog = "speed layer degraded: redis primary unreachable, serving from the database backend"
	breakerHealLog = "speed layer recovered: redis primary reachable again, resuming"
)

// failWindow is the sliding window the breaker's failure count is
// taken over. The count used to be CONSECUTIVE failures, reset by any
// success — so a primary flapping at ~50% success never tripped and
// silently dropped about half of the tracker's updates forever
// (review m3). A windowed count trips on failure DENSITY instead:
// failThreshold failures inside failWindow open the breaker no matter
// how successes interleave, while a healthy primary's isolated blips
// age out of the window before they can accumulate.
const failWindow = 10 * time.Second

type failoverStore struct {
	primary  Store
	fallback Store

	mu       sync.Mutex
	degraded bool
	failures []time.Time // primary failures inside failWindow, oldest first
}

func newFailoverStore(primary, fallback Store) *failoverStore {
	f := &failoverStore{primary: primary, fallback: fallback}
	go f.probeLoop()
	return f
}

// startDegraded opens the breaker from boot: the builder's eager Ping
// already proved the primary unreachable, so traffic starts on the
// database backend immediately instead of discovering it over the
// first probe cycles. The prober heals it the same way as a mid-run
// trip.
func (f *failoverStore) startDegraded() {
	f.mu.Lock()
	f.degraded = true
	f.mu.Unlock()
	log.Warn("speedlayer", breakerTripLog, "primary", f.primary.Name())
}

func (f *failoverStore) Name() string {
	if f.isDegraded() {
		return f.fallback.Name() + "(degraded)"
	}
	return f.primary.Name()
}

// PrimaryName is the configured primary's name regardless of breaker
// state (the status payload reports both fields).
func (f *failoverStore) PrimaryName() string { return f.primary.Name() }

// Degraded reports whether the breaker is open.
func (f *failoverStore) Degraded() bool { return f.isDegraded() }

func (f *failoverStore) isDegraded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.degraded
}

// pruneFailuresLocked drops failures older than failWindow. Caller
// holds f.mu.
func (f *failoverStore) pruneFailuresLocked(now time.Time) {
	cutoff := now.Add(-failWindow)
	i := 0
	for i < len(f.failures) && f.failures[i].Before(cutoff) {
		i++
	}
	f.failures = f.failures[i:]
}

func (f *failoverStore) recordPrimaryFailure() {
	now := time.Now()
	f.mu.Lock()
	f.pruneFailuresLocked(now)
	f.failures = append(f.failures, now)
	trip := !f.degraded && len(f.failures) >= failThreshold
	if trip {
		f.degraded = true
	}
	f.mu.Unlock()
	if trip {
		log.Warn("speedlayer", breakerTripLog, "primary", f.primary.Name())
	}
}

func (f *failoverStore) recordPrimarySuccess() {
	// A success no longer resets the failure count (see failWindow):
	// it only lets old failures age out on schedule.
	f.mu.Lock()
	f.pruneFailuresLocked(time.Now())
	f.mu.Unlock()
}

func (f *failoverStore) probeLoop() {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-shutdown.Done():
			return
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), respCommandTO)
		err := f.primary.Ping(ctx)
		cancel()
		if err != nil {
			f.recordPrimaryFailure()
			continue
		}
		f.mu.Lock()
		healed := f.degraded
		f.degraded = false
		f.failures = nil
		f.mu.Unlock()
		if healed {
			log.Info("speedlayer", breakerHealLog, "primary", f.primary.Name())
		}
	}
}

// route picks the backend for one operation and runs fn against it. A
// primary failure is recorded (tripping the breaker at the threshold)
// and the operation is served by the fallback — the caller never sees a
// primary error while the fallback can serve.
func route[T any](f *failoverStore, ctx context.Context, fn func(context.Context, Store) (T, error)) (T, error) {
	if !f.isDegraded() {
		v, err := fn(ctx, f.primary)
		if err == nil {
			f.recordPrimarySuccess()
			return v, nil
		}
		f.recordPrimaryFailure()
		if !f.isDegraded() {
			// Below the trip threshold, surface the error rather than
			// silently double-writing: the caller (tracker, cursor) is
			// built to drop a best-effort update, and a hard split of
			// writes across two backends below the threshold would be
			// harder to reason about than a dropped hint.
			var zero T
			return zero, err
		}
	}
	return fn(ctx, f.fallback)
}

func (f *failoverStore) Ping(ctx context.Context) error {
	_, err := route(f, ctx, func(c context.Context, s Store) (struct{}, error) {
		return struct{}{}, s.Ping(c)
	})
	return err
}

func (f *failoverStore) AddCounter(ctx context.Context, key string, delta int64) (int64, error) {
	return route(f, ctx, func(c context.Context, s Store) (int64, error) {
		return s.AddCounter(c, key, delta)
	})
}

func (f *failoverStore) Counters(ctx context.Context, prefix string) (map[string]int64, error) {
	return route(f, ctx, func(c context.Context, s Store) (map[string]int64, error) {
		return s.Counters(c, prefix)
	})
}

func (f *failoverStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	_, err := route(f, ctx, func(c context.Context, s Store) (struct{}, error) {
		return struct{}{}, s.Set(c, key, value, ttl)
	})
	return err
}

func (f *failoverStore) Get(ctx context.Context, key string) (string, bool, error) {
	type res struct {
		v  string
		ok bool
	}
	r, err := route(f, ctx, func(c context.Context, s Store) (res, error) {
		v, ok, err := s.Get(c, key)
		return res{v, ok}, err
	})
	return r.v, r.ok, err
}

func (f *failoverStore) GetMany(ctx context.Context, keys []string) (map[string]string, error) {
	return route(f, ctx, func(c context.Context, s Store) (map[string]string, error) {
		return s.GetMany(c, keys)
	})
}

func (f *failoverStore) Delete(ctx context.Context, key string) error {
	_, err := route(f, ctx, func(c context.Context, s Store) (struct{}, error) {
		return struct{}{}, s.Delete(c, key)
	})
	return err
}

func (f *failoverStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	return route(f, ctx, func(c context.Context, s Store) (int, error) {
		return s.DeletePrefix(c, prefix)
	})
}

func (f *failoverStore) RingPush(ctx context.Context, key, value string, cap int) error {
	_, err := route(f, ctx, func(c context.Context, s Store) (struct{}, error) {
		return struct{}{}, s.RingPush(c, key, value, cap)
	})
	return err
}

func (f *failoverStore) RingRead(ctx context.Context, key string, limit int) ([]string, error) {
	return route(f, ctx, func(c context.Context, s Store) ([]string, error) {
		return s.RingRead(c, key, limit)
	})
}
