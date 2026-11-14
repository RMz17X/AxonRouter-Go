package db

import (
	"database/sql"
	"fmt"
	"time"
)

// minimaxClaimState remembers, per provider connection, the last day a
// daily check-in reward was successfully claimed through this gateway
// (manual button, bulk claim, or the background scheduler).
//
// It exists because the only other source of "already claimed today" is
// a live upstream read of MiniMax's signin/status endpoint. That read
// fails often enough (expired token, upstream hiccup, rate limit) that
// the quota page kept falling back to "unknown" and rendering a Claim
// button on accounts that had in fact already claimed — the exact
// complaint this table fixes. A claim recorded here outlives upstream
// flakiness, so the status endpoint can answer "claimed" from one
// indexed row without touching the network.
//
// claimDate is stored as YYYY-MM-DD in WIB (UTC+7): MiniMax's own
// client protocol signs requests with timezone_offset -25200s, i.e.
// the check-in day rolls over on Jakarta midnight, so matching the
// day boundary to WIB is what keeps "claimed today" from being
// computed against the wrong day half the time.

// minimaxClaimDateLayout is the storage format of the claimDate column.
const minimaxClaimDateLayout = "2006-01-02"

// minimaxWIBLoc is the day-boundary zone for check-in dates. Fixed
// offset zone rather than time.LoadLocation("Asia/Jakarta"): the
// gateway binary must not depend on the host tzdata being installed.
var minimaxWIBLoc = time.FixedZone("WIB", 7*3600)

// MinimaxClaimDateToday returns today's check-in date string in WIB.
func MinimaxClaimDateToday() string {
	return time.Now().In(minimaxWIBLoc).Format(minimaxClaimDateLayout)
}

// EnsureMinimaxClaimStateTable creates the ledger when absent. Safe to
// call on every startup, same IF NOT EXISTS contract as the rest of
// the bootstrap. Kept out of pgCoreSchema() so the declarative defs
// file stays a mirror of the upstream schema; this table is an
// AxonRouter-only addition.
func EnsureMinimaxClaimStateTable(conn *sql.DB) error {
	if conn == nil {
		return fmt.Errorf("minimax claim state: nil db")
	}
	_, err := conn.Exec(`CREATE TABLE IF NOT EXISTS minimaxClaimState (
		connectionId TEXT PRIMARY KEY,
		claimDate    TEXT NOT NULL,
		dayNo        INTEGER DEFAULT 0,
		points       INTEGER DEFAULT 0,
		updatedAt    TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("minimax claim state: create table: %w", err)
	}
	return nil
}

// EnsureMinimaxClaimStateTable on Repo delegates to the connection helper.
func (r *Repo) EnsureMinimaxClaimStateTable() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("minimax claim state: nil repo")
	}
	return EnsureMinimaxClaimStateTable(r.db)
}

// RecordMiniMaxClaimed marks a connection as having claimed its daily
// reward. An upsert on purpose: a re-claim within the same day (or a
// correction with better streak data) replaces the earlier row rather
// than fighting a unique constraint.
func (r *Repo) RecordMiniMaxClaimed(connectionID string, dayNo, points int) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("record minimax claim: nil repo")
	}
	if connectionID == "" {
		return fmt.Errorf("record minimax claim: empty connection id")
	}
	date := MinimaxClaimDateToday()
	_, err := r.Exec(`INSERT INTO minimaxClaimState (connectionId, claimDate, dayNo, points, updatedAt)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (connectionId) DO UPDATE SET
			claimDate = EXCLUDED.claimDate,
			dayNo = EXCLUDED.dayNo,
			points = EXCLUDED.points,
			updatedAt = EXCLUDED.updatedAt`,
		connectionID, date, dayNo, points, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("record minimax claim %s: %w", connectionID, err)
	}
	return nil
}

// MinimaxClaimedToday reports whether the connection has a ledger row
// dated today (WIB). sql.ErrNoRows and any read failure both answer
// false — the caller then falls through to the live upstream read,
// which is the correct fail-open: the ledger may only ever skip an
// upstream call it is sure about, never claim one happened that did not.
func (r *Repo) MinimaxClaimedToday(connectionID string) (bool, int) {
	if r == nil || r.db == nil || connectionID == "" {
		return false, 0
	}
	var date string
	var dayNo int
	err := r.QueryRow(
		`SELECT claimDate, dayNo FROM minimaxClaimState WHERE connectionId = ?`,
		connectionID).Scan(&date, &dayNo)
	if err != nil {
		return false, 0
	}
	if date != MinimaxClaimDateToday() {
		return false, 0
	}
	return true, dayNo
}
