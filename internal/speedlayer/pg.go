package speedlayer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// pg.go is the database backend — the degradation store. It keeps the
// speed layer's state in two small tables inside the application
// database, so when the Redis primary is unreachable the cluster falls
// back to the one store every node already shares, with the same
// durability as the truth it caches. It runs through database/sql on
// the application's own handle — no second pool to size or leak.
type DBStore struct {
	db *sql.DB
}

// NewDBStore creates the backend over an existing handle and ensures
// its tables.
func NewDBStore(handle *sql.DB) (*DBStore, error) {
	s := &DBStore{db: handle}
	if err := s.ensureTables(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DBStore) Name() string { return "database" }

func (s *DBStore) ensureTables() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS speed_kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			expires_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS speed_counters (
			key TEXT PRIMARY KEY,
			value BIGINT NOT NULL,
			expires_at TEXT NOT NULL DEFAULT ''
		)`,
	} {
		if _, err := s.db.Exec(s.bind(stmt)); err != nil {
			return fmt.Errorf("speedlayer: create table: %w", err)
		}
	}
	return nil
}

// bind rewrites '?' placeholders for PostgreSQL. The statements in this
// file are fixed strings authored here and none contains a literal '?',
// so a plain sequential rewrite is exact — the quote-aware machinery in
// internal/db exists for the application's arbitrary SQL, not this.
func (s *DBStore) bind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func expiryString(ttl time.Duration) string {
	if ttl <= 0 {
		return ""
	}
	return time.Now().Add(ttl).UTC().Format(time.RFC3339)
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339) }

func expired(expiresAt string) bool {
	return expiresAt != "" && expiresAt <= nowString()
}

func (s *DBStore) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

func (s *DBStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx, s.bind(
		`INSERT INTO speed_kv (key, value, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at`),
		key, value, expiryString(ttl))
	return err
}

func (s *DBStore) Get(ctx context.Context, key string) (string, bool, error) {
	var value, exp string
	err := s.db.QueryRowContext(ctx, s.bind(
		`SELECT value, expires_at FROM speed_kv WHERE key = ?`), key).Scan(&value, &exp)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if expired(exp) {
		_, _ = s.db.ExecContext(ctx, s.bind(`DELETE FROM speed_kv WHERE key = ?`), key)
		return "", false, nil
	}
	return value, true, nil
}

func (s *DBStore) GetMany(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, k := range keys {
		placeholders[i] = "?"
		args[i] = k
	}
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT key, value, expires_at FROM speed_kv WHERE key IN (`+strings.Join(placeholders, ",")+`)`), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v, exp string
		if err := rows.Scan(&k, &v, &exp); err != nil {
			return nil, err
		}
		if !expired(exp) {
			out[k] = v
		}
	}
	return out, rows.Err()
}

func (s *DBStore) Delete(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM speed_kv WHERE key = ?`), key)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.bind(`DELETE FROM speed_counters WHERE key = ?`), key)
	return err
}

func (s *DBStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	like := prefix + "%"
	res, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM speed_kv WHERE key LIKE ?`), like)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	res, err = s.db.ExecContext(ctx, s.bind(`DELETE FROM speed_counters WHERE key LIKE ?`), like)
	if err != nil {
		return int(n), err
	}
	m, _ := res.RowsAffected()
	return int(n + m), nil
}

func (s *DBStore) AddCounter(ctx context.Context, key string, delta int64) (int64, error) {
	// An expired counter restarts from the delta instead of accumulating
	// onto its stale value — the CASE keeps that decision inside the one
	// atomic statement, so concurrent writers cannot resurrect a count
	// whose TTL already lapsed.
	var value int64
	err := s.db.QueryRowContext(ctx, s.bind(
		`INSERT INTO speed_counters (key, value, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET
			value = CASE WHEN speed_counters.expires_at != '' AND speed_counters.expires_at <= ?
				THEN EXCLUDED.value
				ELSE speed_counters.value + EXCLUDED.value END,
			expires_at = EXCLUDED.expires_at
		 RETURNING value`),
		key, delta, expiryString(counterTTL), nowString()).Scan(&value)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func (s *DBStore) Counters(ctx context.Context, prefix string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(
		`SELECT key, value FROM speed_counters
		 WHERE key LIKE ? AND (expires_at = '' OR expires_at > ?)`),
		prefix+"%", nowString())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int64)
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if v != 0 {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// The recent-request ring is one JSON array in speed_kv, updated under a
// row lock: the ring is capped at a few dozen small entries, so the
// read-modify-write is trivially cheap next to the request that produced
// it, and a list table would buy nothing.
func (s *DBStore) RingPush(ctx context.Context, key, value string, cap int) error {
	if cap <= 0 {
		cap = 1
	}
	ringKey := "ring:" + key
	return s.withLockedRow(ctx, ringKey, func(current string) (string, error) {
		var ring []string
		if current != "" {
			if err := json.Unmarshal([]byte(current), &ring); err != nil {
				ring = nil // a corrupt ring restarts empty; it is a cache
			}
		}
		ring = append([]string{value}, ring...)
		if len(ring) > cap {
			ring = ring[:cap]
		}
		encoded, err := json.Marshal(ring)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	})
}

func (s *DBStore) RingRead(ctx context.Context, key string, limit int) ([]string, error) {
	raw, ok, err := s.Get(ctx, "ring:"+key)
	if err != nil || !ok || raw == "" {
		return []string{}, err
	}
	var ring []string
	if err := json.Unmarshal([]byte(raw), &ring); err != nil {
		return []string{}, nil
	}
	if limit > 0 && len(ring) > limit {
		ring = ring[:limit]
	}
	return ring, nil
}

// withLockedRow runs fn against the current value of a speed_kv row
// under a write lock and stores what fn returns, atomically: a dedicated
// connection, BEGIN + SELECT … FOR UPDATE — the same pattern the
// rotation stamps use (db/rotation.go).
func (s *DBStore) withLockedRow(ctx context.Context, key string, fn func(current string) (string, error)) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("speedlayer: acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("speedlayer: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var current string
	query := `SELECT value FROM speed_kv WHERE key = ? FOR UPDATE`
	var scanned sql.NullString
	if err := conn.QueryRowContext(ctx, s.bind(query), key).Scan(&scanned); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("speedlayer: read ring row: %w", err)
	}
	current = scanned.String

	next, err := fn(current)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, s.bind(
		`INSERT INTO speed_kv (key, value, expires_at) VALUES (?, ?, '')
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, expires_at = ''`),
		key, next); err != nil {
		return fmt.Errorf("speedlayer: write ring row: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("speedlayer: commit: %w", err)
	}
	committed = true
	return nil
}
