package db

import "fmt"

// DatabaseSizePretty returns pg_size_pretty(pg_database_size(...)) — the
// GET /api/system/memory dbSize on PostgreSQL installs (batch 7,
// AxonRouter parity with settingsRepo.getDatabaseSize). Returns "" on a
// non-PostgreSQL repo so the caller can fall back.
func (r *Repo) DatabaseSizePretty() (string, error) {
	if !r.IsPostgres() {
		return "", nil
	}
	var size string
	if err := r.db.QueryRow(`SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&size); err != nil {
		return "", fmt.Errorf("database size: %w", err)
	}
	return size, nil
}
