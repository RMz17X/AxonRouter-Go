package middleware

import (
	"sync"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/log"
)

// apikey_usage.go accumulates the F-14 api-key usage accounting
// (usedCount / lastUsedAt) in memory and lands it in batches.
//
// It used to be one UPDATE per authenticated request, synchronously on
// the request goroutine — and because a deployment funnels through a
// handful of keys, every concurrent request serialised on the same
// apiKeys row (review cycle 1, F1: pg_stat_activity showed the pool
// parked in Lock/transactionid behind exactly these writes). Now the
// auth path only increments an in-memory counter; a flusher lands each
// key's accumulated count as one UPDATE per interval.
//
// The two fields are bookkeeping, not ledger: a crash loses at most
// one flush interval of counts — the same loss a single failed write
// already meant under the old best-effort contract (see auth.go). The
// usage LEDGER (usageHistory) is a different path with its own
// never-droppable writer and is not affected by this trade.

// apiKeyUsageFlushInterval is the accounting freshness bound: the
// dashboard's usedCount/lastUsedAt trail live traffic by at most this
// much.
const apiKeyUsageFlushInterval = 10 * time.Second

type apiKeyUsagePending struct {
	count    int
	lastUsed time.Time
}

type apiKeyUsageAccumulator struct {
	mu      sync.Mutex
	repo    *db.Repo
	pending map[string]*apiKeyUsagePending
	started bool
}

var apiKeyUsageAccum = &apiKeyUsageAccumulator{
	pending: make(map[string]*apiKeyUsagePending),
}

// RecordApiKeyUsage notes one authenticated request against keyID. It
// never touches the database on the caller's goroutine.
func RecordApiKeyUsage(repo *db.Repo, keyID string) {
	if repo == nil || keyID == "" {
		return
	}
	now := time.Now().UTC()
	apiKeyUsageAccum.mu.Lock()
	apiKeyUsageAccum.repo = repo
	p := apiKeyUsageAccum.pending[keyID]
	if p == nil {
		p = &apiKeyUsagePending{}
		apiKeyUsageAccum.pending[keyID] = p
	}
	p.count++
	p.lastUsed = now
	if !apiKeyUsageAccum.started {
		apiKeyUsageAccum.started = true
		go apiKeyUsageFlushLoop()
	}
	apiKeyUsageAccum.mu.Unlock()
}

func apiKeyUsageFlushLoop() {
	ticker := time.NewTicker(apiKeyUsageFlushInterval)
	defer ticker.Stop()
	for range ticker.C {
		FlushApiKeyUsage()
	}
}

// FlushApiKeyUsage lands every accumulated count now. The server's
// shutdown hook calls it after the final responses complete, so a
// graceful exit loses nothing; the ticker calls it in steady state.
func FlushApiKeyUsage() {
	apiKeyUsageAccum.mu.Lock()
	pending := apiKeyUsageAccum.pending
	repo := apiKeyUsageAccum.repo
	apiKeyUsageAccum.pending = make(map[string]*apiKeyUsagePending)
	apiKeyUsageAccum.mu.Unlock()
	if len(pending) == 0 || repo == nil {
		return
	}
	for keyID, p := range pending {
		if err := repo.AddApiKeyUsage(keyID, p.count, p.lastUsed); err != nil {
			log.Warn("auth", "api key usage flush failed", "key", keyID, "count", p.count, "error", err)
		}
	}
}
