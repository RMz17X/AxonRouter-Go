package db

import (
	"math/rand/v2"
	"time"

	"axonrouter/proxy/internal/providers"
)

// Rotation pacing, fragment 974 mechanism C — jittered re-eligibility.
//
// When an account-scoped cooldown is written, the announced expiry stays
// in its own field (rateLimitedUntil / oauthLockedUntil) untouched: the
// dashboard renders it as the reset instant, and it IS the instant the
// upstream announced. What selection honors is a companion field written
// in the same statement — the instant the account actually becomes
// selectable again, expiry + one roll of the jitter window. Rolling at
// write time is what makes the value stable: every reader of the episode
// sees the same stored instant, across processes and restarts, instead
// of re-rolling per check and flickering the account in and out of the
// candidate set.
//
// The companion only ever stands in for its own park when it is LATER
// than the announced expiry (see parkEffectiveUntil in
// accounts_oauth_lock.go), so a companion left behind by a reset that
// cleared the park — or retired by a later lock written with jitter
// off — is inert.
//
// Scope: antigravity only, per the operator's decision. Other providers
// keep the exact pre-974 behavior regardless of the setting.
const (
	// keyRateLimitEligibleAt / keyOAuthLockEligibleAt are the data-blob
	// companions of rateLimitedUntil / oauthLockedUntil.
	keyRateLimitEligibleAt = "rateLimitedEligibleAt"
	keyOAuthLockEligibleAt = "oauthLockedEligibleAt"

	// reentryJitterDivisor and reentryJitterCap shape the derived
	// default window: 10% of the cooldown the account is about to
	// serve, capped at 5 minutes. The shape follows the two jitter
	// precedents already in the tree — fragment 968's background
	// refresh de-synchronisation and the combo retry jitter — scaled
	// to cooldown magnitudes instead of attempt magnitudes.
	reentryJitterDivisor = 10
	reentryJitterCap     = 5 * time.Minute
)

// reentryJitterWindow resolves the jitter window for one lock episode:
// the provider strategy's CooldownJitterSec when set (0 = off, positive
// = fixed window), otherwise the derived default for antigravity.
func (r *Repo) reentryJitterWindow(provider string, cooldown time.Duration) (time.Duration, bool) {
	settings, err := r.GetSettings()
	if err != nil || settings == nil {
		return 0, false
	}
	var strat ProviderStrategy
	found := false
	if settings.ProviderStrategies != nil {
		if s, ok := settings.ProviderStrategies[provider]; ok {
			strat, found = s, true
		} else if s, ok := settings.ProviderStrategies[providers.ResolveAlias(provider)]; ok {
			strat, found = s, true
		}
	}
	if found && strat.CooldownJitterSec != nil {
		if *strat.CooldownJitterSec <= 0 {
			return 0, false
		}
		return time.Duration(*strat.CooldownJitterSec) * time.Second, true
	}
	if providers.ResolveAlias(provider) != "antigravity" {
		return 0, false
	}
	window := cooldown / reentryJitterDivisor
	if window > reentryJitterCap {
		window = reentryJitterCap
	}
	if window <= 0 {
		return 0, false
	}
	return window, true
}

// reentryEligibleAt rolls the re-eligibility instant for a lock being
// written on connID: until + rand(0, window). It reports false when no
// jitter applies — a provider outside the pacing scope, the setting
// turned off, or a lock with no time left to serve — and the caller
// then stores a null companion, retiring any roll an older episode
// left behind.
//
// The provider read is one primary-key lookup on the lock-write path.
// Lock writes are failure-path events, never per-selection, so the
// hot path stays exactly as read-light as it was (AGENTS.md §4.A).
func (r *Repo) reentryEligibleAt(connID string, lockedAt, until time.Time) (time.Time, bool) {
	cooldown := until.Sub(lockedAt)
	if cooldown <= 0 {
		return time.Time{}, false
	}
	var provider string
	if err := r.QueryRow("SELECT provider FROM providerConnections WHERE id = ?", connID).Scan(&provider); err != nil || provider == "" {
		return time.Time{}, false
	}
	window, ok := r.reentryJitterWindow(provider, cooldown)
	if !ok {
		return time.Time{}, false
	}
	return until.Add(time.Duration(rand.Int64N(int64(window)))), true
}
