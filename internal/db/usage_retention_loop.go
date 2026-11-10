package db

import (
	"context"
	"log"
	"sync"
	"time"
)

// Retention runs on a timer rather than per request: pruning is a disk-space
// concern, and a request-path prune would put a write transaction in front of
// every inference call.
const (
	retentionInitialDelay = 60 * time.Second
	retentionInterval     = 6 * time.Hour
)

var (
	retentionMu      sync.Mutex
	retentionStarted bool
)

// StartRetentionLoop prunes the usage ledger on a timer when the
// operator has configured a window for it, until ctx is cancelled.
// Request details used to be pruned here too; they live in the
// file-backed detail store now, which runs its own retention from
// the app layer (internal/app) against the same settings.
//
// Safe to call more than once; the extra calls are no-ops. Fail-open:
// a failed pass is logged and the next tick retries, because a
// pruning failure costs disk and never costs correctness — and a
// gateway that cannot prune must still serve traffic.
func StartRetentionLoop(ctx context.Context, repo *Repo) {
	if repo == nil {
		return
	}

	retentionMu.Lock()
	if retentionStarted {
		retentionMu.Unlock()
		return
	}
	retentionStarted = true
	retentionMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(retentionInitialDelay):
		}

		ticker := time.NewTicker(retentionInterval)
		defer ticker.Stop()

		for {
			runRetentionPass(ctx, repo)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// runRetentionPass runs one tick of the retention loop: the usage
// ledger, only when the operator has asked for it.
//
// Statistics freshness after a pass is the server's autoanalyze job,
// so nothing runs here to refresh it.
func runRetentionPass(ctx context.Context, repo *Repo) {
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	ledgerDays := 0
	if s, err := repo.GetSettings(); err == nil && s != nil {
		ledgerDays = s.UsageHistoryRetentionDays
	}

	pruneUsageLedgerRows(passCtx, ctx, repo, ledgerDays)
	// No statistics refresh ends the pass: the server's autoanalyze
	// owns that job (the file backend's PRAGMA optimize had no server
	// form and was gated off rather than transliterated).
}

// pruneUsageLedgerRows runs the ledger prune when the operator has configured a
// window for it. days <= 0 means the ledger is kept forever, which is the
// default: nothing is read and nothing is deleted.
//
// The count is taken before the delete so the common case — a window that holds
// nothing yet — costs one indexed COUNT and stays silent in the log.
func pruneUsageLedgerRows(passCtx, ctx context.Context, repo *Repo, days int) bool {
	if days <= 0 {
		return false
	}
	retention := time.Duration(days) * 24 * time.Hour
	pending, cutoff, err := repo.CountExpiredUsageHistory(passCtx, retention)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] usage ledger count failed: %v", err)
		}
		return false
	}
	if pending == 0 {
		return false
	}
	result, err := repo.PruneUsageHistory(passCtx, retention)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] usage ledger prune failed: %v", err)
		}
		return false
	}
	log.Printf("[RETENTION] pruned %d usageHistory rows before %s (%d tokens carried into per-key budgets)",
		result.Rows, cutoff, result.CarriedTokens)
	return result.Rows > 0
}
