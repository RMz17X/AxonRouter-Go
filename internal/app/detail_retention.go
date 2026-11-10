package app

import (
	"context"
	"log"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/detailstore"
)

// The request-details store keeps its own retention, on the same
// cadence and the same settings the table prune used: an initial
// pass a minute after boot, then every six hours. It lives here
// rather than in the db retention loop because the store is files,
// not a table — the loop over there owns only the usage ledger now.
const (
	detailRetentionInitialDelay = 60 * time.Second
	detailRetentionInterval     = 6 * time.Hour
)

// startDetailRetention prunes the file-backed request-details store
// on a timer until ctx is cancelled. Fail-open, like every retention
// pass in this codebase: a failed prune is logged and the next tick
// retries.
func startDetailRetention(ctx context.Context, repo *db.Repo) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(detailRetentionInitialDelay):
		}
		ticker := time.NewTicker(detailRetentionInterval)
		defer ticker.Stop()
		for {
			runDetailRetentionPass(ctx, repo)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func runDetailRetentionPass(ctx context.Context, repo *db.Repo) {
	retention := db.DefaultRequestDetailRetention
	maxRows := 0
	if repo != nil {
		if s, err := repo.GetSettings(); err == nil && s != nil {
			if s.RetentionDays > 0 {
				retention = time.Duration(s.RetentionDays) * 24 * time.Hour
			}
			if s.MaxRequestRecords > 0 {
				maxRows = s.MaxRequestRecords
			}
		}
	}
	byAge, bySize, err := detailstore.Prune(retention, maxRows)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[RETENTION] request detail prune failed: %v", err)
		}
		return
	}
	if byAge+bySize > 0 {
		log.Printf("[RETENTION] pruned %d request detail records (by age: %d, by size: %d)",
			byAge+bySize, byAge, bySize)
	}
}
