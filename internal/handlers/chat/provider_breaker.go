package chat

import (
	"errors"
	"sync"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
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
// This breaker watches the fleet-wide outcome per (provider, model):
// providerBreakerThreshold consecutive upstream failures with no
// intervening success open the circuit for providerBreakerCooldown.
// While open, the pair is treated exactly like an unhealthy provider
// (the fallback loop refuses it up front, combos skip the member), so
// requests move on immediately instead of chewing through accounts.
// A single success closes the circuit again, and once the cooldown
// lapses the next attempts re-test the provider naturally.
//
// State is in-memory only, like the dahl concurrency block: a restart
// forgets the verdict, and a still-dead provider re-trips it within
// seconds at any real traffic volume.
const (
	providerBreakerThreshold = 20
	providerBreakerWindow    = 10 * time.Minute
	providerBreakerCooldown  = 10 * time.Minute
)

type providerBreakerState struct {
	consecutiveFailures int
	windowStart         time.Time
	blockedUntil        time.Time
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
// request-level refusals (400-class) do not.
func providerBreakerShouldCount(err error) bool {
	if err == nil {
		return false
	}
	var ue *upstreamError
	if errors.As(err, &ue) {
		return providers.RetryableStatusCodes[ue.StatusCode] || ue.StatusCode >= 500
	}
	return true
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
	st, ok := providerBreaker[key]
	if !ok {
		st = &providerBreakerState{windowStart: now}
		providerBreaker[key] = st
	}
	if now.Before(st.blockedUntil) {
		// Still failing while open (a pinned or probe lane reached the
		// upstream anyway): push the verdict out instead of letting it
		// lapse mid-outage.
		st.blockedUntil = now.Add(providerBreakerCooldown)
	} else {
		if now.Sub(st.windowStart) > providerBreakerWindow {
			st.consecutiveFailures = 0
			st.windowStart = now
		}
		st.consecutiveFailures++
		if st.consecutiveFailures >= providerBreakerThreshold {
			st.blockedUntil = now.Add(providerBreakerCooldown)
			st.consecutiveFailures = 0
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

// RecordProviderModelSuccess closes the pair's circuit: one success
// proves the provider serves this model again, whatever came before.
func RecordProviderModelSuccess(provider, model string) {
	if provider == "" || model == "" {
		return
	}
	key := providerBreakerKey(provider, model)
	providerBreakerMu.Lock()
	delete(providerBreaker, key)
	providerBreakerMu.Unlock()
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
