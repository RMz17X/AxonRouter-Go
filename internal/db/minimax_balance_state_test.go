package db

import (
	"testing"
	"time"

	"axonrouter/proxy/internal/db/dbtest"
)

func TestMiniMaxBalanceState_Lifecycle(t *testing.T) {
	conn, _, cleanup, err := dbtest.NewSchemaHandle("minimax_balance")
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	defer cleanup()

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema failed: %v", err)
	}
	if err := EnsureMiniMaxBalanceStateTable(conn); err != nil {
		t.Fatalf("EnsureMiniMaxBalanceStateTable failed: %v", err)
	}
	repo := NewRepo(conn)

	now := time.Now().UTC().Format(time.RFC3339)
	for _, row := range []struct {
		id       string
		provider string
		active   int
	}{
		{"conn-rich", "minimax-code-global", 1},
		{"conn-poor", "minimax-code-global", 1},
		{"conn-dead", "minimax-code-global", 0},
	} {
		if _, err := repo.Exec(`INSERT INTO providerConnections (id, provider, authType, data, isActive, createdAt, updatedAt)
			VALUES (?, ?, 'bearer', '{}', ?, ?, ?)`, row.id, row.provider, row.active, now, now); err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}

	if err := repo.UpdateMiniMaxBalance("conn-poor", "minimax-code-global", 100, "Token Plan"); err != nil {
		t.Fatalf("UpdateMiniMaxBalance poor: %v", err)
	}
	if err := repo.UpdateMiniMaxBalance("conn-rich", "minimax-code-global", 900, "M Plan"); err != nil {
		t.Fatalf("UpdateMiniMaxBalance rich: %v", err)
	}
	if err := repo.UpdateMiniMaxBalance("conn-dead", "minimax-code-global", 500, "M Plan"); err != nil {
		t.Fatalf("UpdateMiniMaxBalance dead: %v", err)
	}

	snaps, err := repo.MiniMaxBalanceSnapshots("minimax-code-global", time.Hour)
	if err != nil {
		t.Fatalf("MiniMaxBalanceSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2 active rows", len(snaps))
	}
	if snaps[0].ConnectionID != "conn-rich" || snaps[0].Balance != 900 {
		t.Fatalf("first snapshot = %+v, want conn-rich/900", snaps[0])
	}
	if snaps[1].ConnectionID != "conn-poor" || snaps[1].Balance != 100 {
		t.Fatalf("second snapshot = %+v, want conn-poor/100", snaps[1])
	}

	// A zero balance is stored, not dropped: an empty account is still a
	// signal the video lane can use to avoid trying it first.
	if err := repo.UpdateMiniMaxBalance("conn-poor", "minimax-code-global", 0, "Token Plan"); err != nil {
		t.Fatalf("UpdateMiniMaxBalance zero: %v", err)
	}
	snaps, err = repo.MiniMaxBalanceSnapshots("minimax-code-global", time.Hour)
	if err != nil {
		t.Fatalf("MiniMaxBalanceSnapshots after zero: %v", err)
	}
	if len(snaps) != 2 || snaps[1].ConnectionID != "conn-poor" || snaps[1].Balance != 0 {
		t.Fatalf("zero snapshot not ranked last: %+v", snaps)
	}

	// Stale snapshots are ignored when a freshness window is supplied.
	if _, err := repo.Exec(`UPDATE minimaxBalanceState SET updatedAt = ? WHERE connectionId = 'conn-rich'`,
		time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("age snapshot: %v", err)
	}
	snaps, err = repo.MiniMaxBalanceSnapshots("minimax-code-global", 24*time.Hour)
	if err != nil {
		t.Fatalf("MiniMaxBalanceSnapshots fresh: %v", err)
	}
	if len(snaps) != 1 || snaps[0].ConnectionID != "conn-poor" {
		t.Fatalf("stale snapshot not filtered: %+v", snaps)
	}
}
