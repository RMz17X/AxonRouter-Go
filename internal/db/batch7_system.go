package db

import "fmt"

// DatabaseSizePretty returns pg_size_pretty(pg_database_size(...)) — the
// GET /api/system/memory dbSize (batch 7, AxonRouter parity with
// settingsRepo.getDatabaseSize).
func (r *Repo) DatabaseSizePretty() (string, error) {
	var size string
	if err := r.db.QueryRow(`SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&size); err != nil {
		return "", fmt.Errorf("database size: %w", err)
	}
	return size, nil
}
