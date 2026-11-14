package chat

import (
	"errors"
	"sync"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/translator"
)

// provider_breaker.go — a circuit breaker per (provider, model) pair.
//
// The per-account model locks cannot protect the request path when a
// provider has tens of thousands of accounts: locking one account just
// makes the selector draw the next fresh one, so a provider whose whole
// pool is dead stays "available" forever (IsProviderAvailable only goes
// false once EVERY account is locked). Measured live on tokenharbor
// (2026-11-14): 32,708 accounts, 12,207 failed attempts in one hour,
// zero successes — and every mimo-v2.6-flash:free request still routed
// there first, burning hedging and fallback budget fleet-wide.
//
// The breaker is fed from tryForwardWithConnection, the one chokepoint
// every attempt lane (sequential fallback, hedged waves, combo members)
// passes through. Within a rolling window it counts upstream failures
// and successes per (provider, model); when failures reach
// providerBreakerFailureThreshold while successes stay at or below a
// tenth of the failures — a pool that is down, not merely unlucky —
// the circuit opens for providerBreakerCooldown. While open, the pair
// is treated exactly like an unhealthy provider (the fallback loop
// refuses it up front, combos skip the member), so requests move on
// immediately instead of chewing through accounts. A success closes
// the circuit at once; once the cooldown lapses, fresh attempts
// re-test the provider and re-trip the breaker if it is still dead.
//
// State is in-memory only, like the dahl concurrency block: a restart
// forgets the verdict, and a still-dead provider re-trips it within
// seconds at any real traffic volume.
const (
	providerBreakerFailureThreshold = 30
	providerBreakerWindow           = 5 * time.Minute
	providerBreakerCooldown         = 10 * time.Minute
)

type providerBreakerState struct {
	failures     int
	successes    int
	windowStart  time.Time
	blockedUntil time.Time
}

var (
	providerBreakerMu sync.Mutex
	providerBreaker   = make(map[string]*providerBreakerState)
)

func providerBreakerKey(provider, model string) string {
	lockKey := canonicalLockModel(provider, model)
	if lockKey == "" {
		lockKey = model
	}
	return provider + "\x00" + lockKey
}

// providerBreakerShouldCount reports whether an attempt failure is
// evidence about the provider (vs about the request): retryable
// upstream statuses, 5xx, and transport/executor errors count;
// request-level refusals and model-behaviour mismatches do not.
func providerBreakerShouldCount(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, translator.ErrDecoyOnlyResponse) {
		return false
	}
	var ue *upstreamError
	if errors.As(err, &ue) {
		return providers.RetryableStatusCodes[ue.StatusCode] || ue.StatusCode >= 500
	}
	return true
}

func providerBreakerStateFor(key string, now time.Time) *providerBreakerState {
	st, ok := providerBreaker[key]
	if !ok {
		st = &providerBreakerState{windowStart: now}
		providerBreaker[key] = st
	}
	if now.Sub(st.windowStart) > providerBreakerWindow && !now.Before(st.blockedUntil) {
		st.failures = 0
		st.successes = 0
		st.windowStart = now
	}
	return st
}

// RecordProviderModelFailure feeds one failed upstream attempt into
// the breaker for its (provider, model) pair.
func RecordProviderModelFailure(provider, model string) {
	if provider == "" || model == "" {
		return
	}
	now := time.Now()
	key := providerBreakerKey(provider, model)
	tripped := false
	providerBreakerMu.Lock()
	st := providerBreakerStateFor(key, now)
	if now.Before(st.blockedUntil) {
		// Still failing while open (a pinned lane reached the upstream
		// anyway): push the verdict out instead of letting it lapse
		// mid-outage.
		st.blockedUntil = now.Add(providerBreakerCooldown)
	} else {
		st.failures++
		if st.failures >= providerBreakerFailureThreshold && st.successes*10 <= st.failures {
			st.blockedUntil = now.Add(providerBreakerCooldown)
			st.failures = 0
			st.successes = 0
			st.windowStart = now
			tripped = true
		}
	}
	providerBreakerMu.Unlock()
	if tripped {
		log.Warn("breaker", "provider model circuit opened",
			"provider", provider, "model", model,
			"cooldown", providerBreakerCooldown.String())
	}
}

// RecordProviderModelSuccess feeds one successful upstream attempt:
// it closes an open circuit at once and otherwise counts toward the
// window's success share that keeps an unlucky-but-alive provider
// from tripping the breaker.
func RecordProviderModelSuccess(provider, model string) {
	if provider == "" || model == "" {
		return
	}
	now := time.Now()
	key := providerBreakerKey(provider, model)
	closed := false
	providerBreakerMu.Lock()
	if st, ok := providerBreaker[key]; ok && now.Before(st.blockedUntil) {
		delete(providerBreaker, key)
		closed = true
	} else {
		providerBreakerStateFor(key, now).successes++
	}
	providerBreakerMu.Unlock()
	if closed {
		log.Info("breaker", "provider model circuit closed by a success",
			"provider", provider, "model", model)
	}
}

// IsProviderModelBroken reports whether the pair's circuit is open.
func IsProviderModelBroken(provider, model string) bool {
	if provider == "" || model == "" {
		return false
	}
	key := providerBreakerKey(provider, model)
	providerBreakerMu.Lock()
	defer providerBreakerMu.Unlock()
	st, ok := providerBreaker[key]
	return ok && time.Now().Before(st.blockedUntil)
}

// ResetProviderBreaker clears all breaker state (used in tests).
func ResetProviderBreaker() {
	providerBreakerMu.Lock()
	defer providerBreakerMu.Unlock()
	providerBreaker = make(map[string]*providerBreakerState)
}
