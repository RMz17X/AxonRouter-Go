package db

import (
	"time"

	"axonrouter/proxy/internal/providers"
)

// Idle generation probe settings, fragment 979.
//
// Fragment 973's ToS-disable detection is traffic-driven: it classifies
// generation responses as they pass through the chat lane. An account
// Google disabled while it sat idle produces no generation responses, so
// nothing classifies it until its next real request — the operator
// learns about the disable at the worst moment, mid-rotation. The idle
// prober (internal/schedule/idleprobe.go) closes that gap by sending one
// tiny generation request through the same lane for accounts that have
// been quiet past a threshold.
//
// The settings live in the provider strategy, next to fragment 974's
// cooldown jitter — one home for per-provider behavior knobs. Resolution
// follows the same derived-default shape: unset fields select defaults
// that apply to antigravity only, because the gap this closes is the
// Antigravity ToS-disable blindness; other providers keep today's
// behavior unless an operator explicitly enables the probe for them.
const (
	// IdleProbeDefaultThreshold is the derived idle threshold: an
	// account with no generation traffic for 6 hours is verified.
	IdleProbeDefaultThreshold = 6 * time.Hour
	// IdleProbeDefaultInterval is the derived per-account minimum probe
	// interval: however the traffic pattern moves, one account is never
	// probed more often than hourly.
	IdleProbeDefaultInterval = time.Hour
)

// IdleProbeConfig is the resolved idle-probe configuration for one
// provider: whether the prober runs for it at all, how long an account
// may stay quiet before it is verified, and the minimum interval
// between two probes of the same account.
type IdleProbeConfig struct {
	Enabled   bool
	Threshold time.Duration
	Interval  time.Duration
}

// IdleProbeConfigFor resolves the idle-probe configuration for a
// provider from the settings row. A settings read failure resolves to
// disabled: an unreadable settings row must not start probes an operator
// may have turned off.
func (r *Repo) IdleProbeConfigFor(provider string) IdleProbeConfig {
	cfg := IdleProbeConfig{
		Threshold: IdleProbeDefaultThreshold,
		Interval:  IdleProbeDefaultInterval,
	}
	settings, err := r.GetSettings()
	if err != nil || settings == nil {
		return cfg
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
	if found {
		if strat.IdleProbeThresholdSec != nil && *strat.IdleProbeThresholdSec > 0 {
			cfg.Threshold = time.Duration(*strat.IdleProbeThresholdSec) * time.Second
		}
		if strat.IdleProbeIntervalSec != nil && *strat.IdleProbeIntervalSec > 0 {
			cfg.Interval = time.Duration(*strat.IdleProbeIntervalSec) * time.Second
		}
		if strat.IdleProbeEnabled != nil {
			cfg.Enabled = *strat.IdleProbeEnabled
			return cfg
		}
	}
	// Derived default: enabled for antigravity only.
	cfg.Enabled = providers.ResolveAlias(provider) == "antigravity"
	return cfg
}
