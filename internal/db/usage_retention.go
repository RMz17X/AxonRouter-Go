package db

import (
	"context"
	"fmt"
	"time"
)

// Retention bounds for the diagnostic tables.
//
// requestDetails is pruned unconditionally: it only ever feeds the "Recent
// Request Details" tab, which shows the newest page and nothing older. Nothing
// bills from it. At the stored payload size — up to MaxLoggedMessages ×
// MaxMessageContentLen of request text plus MaxResponseContentLen of response
// text, measured at ~20 KB per row — 85k rows is 1.7 GB of diagnostics for a tab
// that renders 20 of them.
//
// usageHistory is the billing ledger and is pruned only when an operator asks
// for it: see usage_retention_ledger.go. It is opt-in because the `all` usage
// period and the analytics sections read it directly, so trimming it changes
// what those pages report — and because the ledger is what a per-key tokenQuota
// is measured from, which is why the prune has to carry the spend of the rows it
// removes rather than let them vanish.
const (
	// DefaultRequestDetailRetention is how long a diagnostic row is kept. A
	// month covers the "why did that request three weeks ago fail" question the
	// tab exists to answer, and bounds the table at roughly what a busy gateway
	// produces in that month.
	DefaultRequestDetailRetention = 30 * 24 * time.Hour
)

// retentionChunkSize bounds a single DELETE. The statement holds a write lock
// for its whole run, and a half-million-row delete would block the request path
// long enough to stall live traffic. Every pass chunks, so none of them ever
// holds the lock for the length of a full-table delete.
//
// It is a variable rather than a constant so the chunk loops can be exercised
// against a few hundred rows instead of five thousand (see the ledger tests).
var retentionChunkSize = 5000

// DatabaseStats holds metrics for database disk usage and key table volumes.
type DatabaseStats struct {
	DatabaseSizeBytes   int64 `json:"databaseSizeBytes"`
	ReclaimableBytes    int64 `json:"reclaimableBytes"`
	RequestDetailsCount int   `json:"requestDetailsCount"`
	UsageHistoryCount   int   `json:"usageHistoryCount"`
	// UsageHistoryOldestTimestamp is the timestamp of the oldest retained ledger
	// row, or empty when there are none. It is what tells an operator how far back
	// the usage pages actually reach, and it is the number a ledger prune moves —
	// without it, "did that prune do anything" is unanswerable from the dashboard.
	//
	// Both bounds ride idx_uh_ts as covering-index seeks (MIN/MAX over the indexed
	// column), so this stays O(log n) on a ledger of any size.
	UsageHistoryOldestTimestamp string `json:"usageHistoryOldestTimestamp"`
	UsageHistoryNewestTimestamp string `json:"usageHistoryNewestTimestamp"`
}

// GetDatabaseStats reports table row counts and ledger bounds.
//
// The old page-count half was a file report and has no counterpart
// here: the database is not a file this process owns, and its space
// bookkeeping (autovacuum) is the server's. The two size fields
// therefore report zero — deliberately not a pg_database_size
// transliteration, which would answer a different question (the whole
// shared database, indexes included) under the field that used to mean
// "this gateway's file".
func (r *Repo) GetDatabaseStats(ctx context.Context) (DatabaseStats, error) {
	var stats DatabaseStats
	if r == nil || r.db == nil {
		return stats, fmt.Errorf("repo or db is nil")
	}

	// RequestDetailsCount is deliberately not read here: request
	// details live in the file-backed detail store, and the stats
	// handler fills the field from it.
	if err := r.QueryRowContext(ctx, "SELECT COUNT(*) FROM usageHistory;").Scan(&stats.UsageHistoryCount); err != nil {
		return stats, fmt.Errorf("count usageHistory: %w", err)
	}
	if err := r.QueryRowContext(ctx,
		`SELECT COALESCE(MIN(timestamp), ''), COALESCE(MAX(timestamp), '') FROM usageHistory`,
	).Scan(&stats.UsageHistoryOldestTimestamp, &stats.UsageHistoryNewestTimestamp); err != nil {
		return stats, fmt.Errorf("read usageHistory bounds: %w", err)
	}

	return stats, nil
}

// VacuumDatabase is a deliberate no-op, for the same reason
// GetDatabaseStats reports no file size: reclaiming space inside a
// shared server is autovacuum's job, and the VACUUM a dashboard button
// could run would not return storage to the operating system in any
// case (that is VACUUM FULL, a locking operation no maintenance button
// should fire). The handler keeps calling it so the maintenance
// endpoint's shape does not change.
func (r *Repo) VacuumDatabase(ctx context.Context) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("repo or db is nil")
	}
	return nil
}
