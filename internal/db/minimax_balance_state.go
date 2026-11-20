package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MiniMaxBalanceState is one connection's last observed credits balance.
// It is a snapshot, not a ledger: the dashboard quota read refreshes it, and
// the video lane uses it only to order candidates before the upstream says
// which account can actually pay.
type MiniMaxBalanceState struct {
	ConnectionID string
	Provider     string
	Balance      int64
	Plan         string
	UpdatedAt    time.Time
}

// EnsureMiniMaxBalanceStateTable creates the snapshot table when absent.
// Safe to call on every startup; the index is created idempotently too.
func EnsureMiniMaxBalanceStateTable(conn *sql.DB) error {
	if conn == nil {
		return fmt.Errorf("minimax balance state: nil db")
	}
	_, err := conn.Exec(`CREATE TABLE IF NOT EXISTS minimaxBalanceState (
		connectionId TEXT PRIMARY KEY,
		provider     TEXT NOT NULL,
		balance      BIGINT NOT NULL DEFAULT 0,
		plan         TEXT NOT NULL DEFAULT '',
		updatedAt    TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("minimax balance state: create table: %w", err)
	}
	_, err = conn.Exec(`CREATE INDEX IF NOT EXISTS idx_mbs_provider_balance
		ON minimaxBalanceState(provider, balance DESC)`)
	if err != nil {
		return fmt.Errorf("minimax balance state: create index: %w", err)
	}
	return nil
}

// EnsureMiniMaxBalanceStateTable on Repo delegates to the connection helper.
func (r *Repo) EnsureMiniMaxBalanceStateTable() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("minimax balance state: nil repo")
	}
	return EnsureMiniMaxBalanceStateTable(r.db)
}

// UpdateMiniMaxBalance records the latest observed balance for a connection.
// A zero balance is stored too: an account known to be empty is exactly the
// one the video lane should not try first.
func (r *Repo) UpdateMiniMaxBalance(connectionID, provider string, balance int64, plan string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("update minimax balance: nil repo")
	}
	if strings.TrimSpace(connectionID) == "" {
		return fmt.Errorf("update minimax balance: empty connection id")
	}
	if strings.TrimSpace(provider) == "" {
		return fmt.Errorf("update minimax balance: empty provider")
	}
	if err := r.EnsureMiniMaxBalanceStateTable(); err != nil {
		return err
	}
	_, err := r.Exec(`INSERT INTO minimaxBalanceState (connectionId, provider, balance, plan, updatedAt)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(connectionId) DO UPDATE SET
			provider = excluded.provider,
			balance = excluded.balance,
			plan = excluded.plan,
			updatedAt = excluded.updatedAt`,
		connectionID, provider, balance, plan, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("update minimax balance %s: %w", connectionID, err)
	}
	return nil
}

// MiniMaxBalanceSnapshots returns active connections' balance snapshots for
// one provider, richest first. maxAge <= 0 disables the freshness filter.
// Only connections that still exist and are active are returned, so a
// snapshot for a deleted row cannot send the video lane to a dead pin.
func (r *Repo) MiniMaxBalanceSnapshots(provider string, maxAge time.Duration) ([]MiniMaxBalanceState, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("minimax balance snapshots: nil repo")
	}
	if strings.TrimSpace(provider) == "" {
		return nil, fmt.Errorf("minimax balance snapshots: empty provider")
	}
	if err := r.EnsureMiniMaxBalanceStateTable(); err != nil {
		return nil, err
	}
	query := `SELECT b.connectionId, b.provider, b.balance, b.plan, b.updatedAt
		FROM minimaxBalanceState b
		JOIN providerConnections pc ON pc.id = b.connectionId
		WHERE b.provider = ? AND pc.isActive = 1`
	args := []any{provider}
	if maxAge > 0 {
		query += ` AND b.updatedAt >= ?`
		args = append(args, time.Now().UTC().Add(-maxAge).Format(time.RFC3339))
	}
	query += ` ORDER BY b.balance DESC, b.updatedAt DESC`
	rows, err := r.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("minimax balance snapshots: %w", err)
	}
	defer rows.Close()
	out := make([]MiniMaxBalanceState, 0, 8)
	for rows.Next() {
		var s MiniMaxBalanceState
		var updated string
		if err := rows.Scan(&s.ConnectionID, &s.Provider, &s.Balance, &s.Plan, &updated); err != nil {
			return nil, fmt.Errorf("minimax balance snapshots: scan: %w", err)
		}
		if t, err := time.Parse(time.RFC3339, updated); err == nil {
			s.UpdatedAt = t
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("minimax balance snapshots: %w", err)
	}
	return out, nil
}
