package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// thoughtSignatureTables is the Go-only table backing the persistent thought
// signature store (G26). It is a table rather than a kv scope because rows are
// pruned by expiry and read by key on every tool turn; a kv scan would decode
// every JSON value to answer either.
var thoughtSignatureTables = []tableDef{
	{
		name: "thoughtSignatures",
		columns: [][2]string{
			{"key", "TEXT PRIMARY KEY"},
			{"signature", "TEXT NOT NULL"},
			{"family", "TEXT NOT NULL DEFAULT ''"},
			{"expiresAt", "TEXT NOT NULL"},
			{"updatedAt", "TEXT NOT NULL"},
		},
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_ts_expires ON thoughtSignatures(expiresAt)",
		},
	},
}

// ThoughtSignatureRow is one persisted signature.
type ThoughtSignatureRow struct {
	Key       string
	Signature string
	Family    string
	ExpiresAt time.Time
}

// SaveThoughtSignature upserts one signature; the newest write wins.
func (r *Repo) SaveThoughtSignature(ctx context.Context, key, signature, family string, expiresAt time.Time) error {
	if key == "" || signature == "" {
		return fmt.Errorf("save thought signature: key and signature are required")
	}
	_, err := r.ExecContext(
		ctx,
		`INSERT INTO thoughtSignatures(key, signature, family, expiresAt, updatedAt) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET signature = excluded.signature, family = excluded.family,
		     expiresAt = excluded.expiresAt, updatedAt = excluded.updatedAt`,
		key, signature, family, expiresAt.UTC().Format(time.RFC3339), nowUTC(),
	)
	if err != nil {
		return fmt.Errorf("save thought signature: %w", err)
	}
	return nil
}

// GetThoughtSignature returns one unexpired row, or nil when absent or expired.
func (r *Repo) GetThoughtSignature(ctx context.Context, key string) (*ThoughtSignatureRow, error) {
	var (
		row     ThoughtSignatureRow
		expires string
	)
	err := r.QueryRowContext(
		ctx,
		`SELECT key, signature, family, expiresAt FROM thoughtSignatures
		 WHERE key = ? AND expiresAt > ? LIMIT 1`,
		key, nowUTC(),
	).Scan(&row.Key, &row.Signature, &row.Family, &expires)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get thought signature: %w", err)
	}
	row.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return nil, fmt.Errorf("get thought signature: parse expiry: %w", err)
	}
	return &row, nil
}

// thoughtSignatureOrder is the newest-first order the load and the trim
// share. updatedAt leads; the tiebreak is the row's identity — the
// primary key itself (PostgreSQL has no rowid). The tiebreak only
// decides between rows written inside the same second (updatedAt is
// second-resolution), so it is a determinism device for the trim and
// the hydration cap, not part of either's contract.
func (r *Repo) thoughtSignatureOrder() string {
	return `updatedAt DESC, key DESC`
}

// LoadThoughtSignatures returns up to limit unexpired rows, newest write first.
// Used once at boot to hydrate the in-memory hot path.
func (r *Repo) LoadThoughtSignatures(ctx context.Context, limit int) ([]ThoughtSignatureRow, error) {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := r.QueryContext(
		ctx,
		`SELECT key, signature, family, expiresAt FROM thoughtSignatures
		 WHERE expiresAt > ? ORDER BY `+r.thoughtSignatureOrder()+` LIMIT ?`,
		nowUTC(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("load thought signatures: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ThoughtSignatureRow, 0, 64)
	for rows.Next() {
		var (
			row     ThoughtSignatureRow
			expires string
		)
		if err := rows.Scan(&row.Key, &row.Signature, &row.Family, &expires); err != nil {
			return nil, fmt.Errorf("load thought signatures: %w", err)
		}
		row.ExpiresAt, err = time.Parse(time.RFC3339, expires)
		if err != nil {
			return nil, fmt.Errorf("load thought signatures: parse expiry: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load thought signatures: %w", err)
	}
	return out, nil
}

// TrimThoughtSignatures deletes the oldest rows beyond keep, and reports how
// many went. Pruning by expiry alone leaves the table unbounded on a gateway
// that keeps serving: a tool-heavy client writes rows faster than an hour-long
// TTL retires them, so the disk tier needs a size ceiling as well as a clock.
//
// The order is (updatedAt, row identity): updatedAt is stored at second
// resolution, so a burst writes many rows that share one timestamp and the
// tiebreak decides which survive. Without it the order is whatever the scan
// produced, and the trim can delete the rows written last. The
// unbounded window is spelled LIMIT ALL — PostgreSQL rejects a
// negative limit outright.
func (r *Repo) TrimThoughtSignatures(ctx context.Context, keep int) (int64, error) {
	if keep < 0 {
		keep = 0
	}
	res, err := r.ExecContext(
		ctx,
		`DELETE FROM thoughtSignatures WHERE key IN (
			SELECT key FROM thoughtSignatures
			ORDER BY `+r.thoughtSignatureOrder()+`
			LIMIT ALL OFFSET ?
		)`, keep,
	)
	if err != nil {
		return 0, fmt.Errorf("trim thought signatures: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("trim thought signatures: %w", err)
	}
	return n, nil
}

// PruneThoughtSignatures deletes expired rows and reports how many went.
func (r *Repo) PruneThoughtSignatures(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.ExecContext(
		ctx,
		`DELETE FROM thoughtSignatures WHERE expiresAt <= ?`,
		now.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("prune thought signatures: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune thought signatures: %w", err)
	}
	return n, nil
}
