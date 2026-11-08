package chat

import (
	"sync"
	"time"
)

var (
	dahlConcurrencyMu     sync.RWMutex
	dahlModelBlockedUntil = make(map[string]time.Time)
)

// RecordDahlModelConcurrencyBlocked records that a Dahl model is saturated across
// the provider cluster, cooling it down for the specified duration (default 10s).
func RecordDahlModelConcurrencyBlocked(model string, duration time.Duration) {
	if model == "" {
		return
	}
	if duration <= 0 {
		duration = 10 * time.Second
	}
	canonical := canonicalLockModel("dahl", model)
	until := time.Now().Add(duration)

	dahlConcurrencyMu.Lock()
	defer dahlConcurrencyMu.Unlock()
	dahlModelBlockedUntil[canonical] = until
	if canonical != model {
		dahlModelBlockedUntil[model] = until
	}
}

// IsDahlModelConcurrencyBlocked reports whether a Dahl model is currently
// blocked due to cluster-wide concurrency saturation on free tier.
func IsDahlModelConcurrencyBlocked(model string) bool {
	if model == "" {
		return false
	}
	canonical := canonicalLockModel("dahl", model)

	dahlConcurrencyMu.RLock()
	defer dahlConcurrencyMu.RUnlock()

	if until, ok := dahlModelBlockedUntil[canonical]; ok {
		if time.Now().Before(until) {
			return true
		}
	}
	if canonical != model {
		if until, ok := dahlModelBlockedUntil[model]; ok {
			if time.Now().Before(until) {
				return true
			}
		}
	}
	return false
}

// ResetDahlModelConcurrency clears the Dahl model concurrency cache (used in tests).
func ResetDahlModelConcurrency() {
	dahlConcurrencyMu.Lock()
	defer dahlConcurrencyMu.Unlock()
	dahlModelBlockedUntil = make(map[string]time.Time)
}
