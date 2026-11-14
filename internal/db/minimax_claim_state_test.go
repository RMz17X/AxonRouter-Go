package db

import (
	"testing"
	"time"

	"axonrouter/proxy/internal/db/dbtest"
)

func TestMiniMaxClaimState_Lifecycle(t *testing.T) {
	conn, _, cleanup, err := dbtest.NewSchemaHandle("minimax_claim")
	if err != nil {
		t.Skipf("skipping database test (no test postgres): %v", err)
		return
	}
	defer cleanup()

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema failed: %v", err)
	}
	if err := EnsureMinimaxClaimStateTable(conn); err != nil {
		t.Fatalf("EnsureMinimaxClaimStateTable failed: %v", err)
	}

	repo := NewRepo(conn)

	// Initially not claimed
	claimed, dayNo := repo.MinimaxClaimedToday("conn-test-1")
	if claimed || dayNo != 0 {
		t.Fatalf("expected not claimed initially, got claimed=%v, dayNo=%d", claimed, dayNo)
	}

	// Record a claim
	if err := repo.RecordMiniMaxClaimed("conn-test-1", 5, 800); err != nil {
		t.Fatalf("RecordMiniMaxClaimed failed: %v", err)
	}

	// Verify it reports claimed today with correct dayNo
	claimed, dayNo = repo.MinimaxClaimedToday("conn-test-1")
	if !claimed || dayNo != 5 {
		t.Fatalf("expected claimed=true dayNo=5, got claimed=%v, dayNo=%d", claimed, dayNo)
	}

	// Another connection is unaffected
	claimed2, _ := repo.MinimaxClaimedToday("conn-test-2")
	if claimed2 {
		t.Fatalf("expected conn-test-2 to be unclaimed")
	}

	// Verify idempotency / upsert update
	if err := repo.RecordMiniMaxClaimed("conn-test-1", 6, 1000); err != nil {
		t.Fatalf("RecordMiniMaxClaimed update failed: %v", err)
	}
	claimed, dayNo = repo.MinimaxClaimedToday("conn-test-1")
	if !claimed || dayNo != 6 {
		t.Fatalf("expected updated dayNo=6, got %d", dayNo)
	}
}

func TestMiniMaxClaimState_StaleDateNotClaimedToday(t *testing.T) {
	conn, _, cleanup, err := dbtest.NewSchemaHandle("minimax_stale")
	if err != nil {
		t.Skipf("skipping database test (no test postgres): %v", err)
		return
	}
	defer cleanup()

	if err := EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema failed: %v", err)
	}
	if err := EnsureMinimaxClaimStateTable(conn); err != nil {
		t.Fatalf("EnsureMinimaxClaimStateTable failed: %v", err)
	}

	repo := NewRepo(conn)

	// Insert an old date record directly
	_, err = repo.Exec(`INSERT INTO minimaxClaimState (connectionId, claimDate, dayNo, points, updatedAt)
		VALUES (?, ?, ?, ?, ?)`,
		"conn-old", "2020-01-01", 3, 500, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert old date failed: %v", err)
	}

	claimed, _ := repo.MinimaxClaimedToday("conn-old")
	if claimed {
		t.Fatalf("expected stale date to NOT report claimed today")
	}
}
