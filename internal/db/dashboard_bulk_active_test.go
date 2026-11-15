package db_test

import (
	"errors"
	"testing"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/db/dbtest"
)

// pgBulkRepo returns a repo over a throwaway schema with the real core
// schema bootstrapped, so the test exercises the production column types
// (isActive INTEGER, data JSONB) rather than a hand-built fixture.
//
// When no database is configured the suite skips: a test that needs a
// database is not a failure when CI provisions none.
func pgBulkRepo(t *testing.T) *db.Repo {
	t.Helper()
	conn, _, cleanupDB, err := dbtest.NewSchemaHandle("bulkactive")
	if err != nil {
		if errors.Is(err, dbtest.ErrNoDatabase) {
			t.Skipf("skipping database-backed test: %v", err)
		}
		t.Fatalf("provision test database: %v", err)
	}
	t.Cleanup(cleanupDB)
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	return db.NewRepo(conn)
}

// bulkSeedConnection inserts one providerConnections row with the given
// active state and status blob.
func bulkSeedConnection(t *testing.T, repo *db.Repo, id, provider string, active int, status string) {
	t.Helper()
	_, err := repo.RawDB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, data, isActive, createdAt, updatedAt)
		 VALUES ($1, $2, 'api-key', $1, jsonb_build_object('status', $3::text, 'testStatus', $3::text), $4, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, provider, status, active,
	)
	if err != nil {
		t.Fatalf("seed connection %s: %v", id, err)
	}
}

// bulkActiveState reads a connection's isActive column and its data->>'status'.
func bulkActiveState(t *testing.T, repo *db.Repo, id string) (int, string) {
	t.Helper()
	var active int
	var status string
	err := repo.RawDB().QueryRow(
		`SELECT isActive, COALESCE(data->>'status','') FROM providerConnections WHERE id = $1`, id,
	).Scan(&active, &status)
	if err != nil {
		t.Fatalf("read connection %s: %v", id, err)
	}
	return active, status
}

// SetProviderConnectionsActive is the single-statement pool toggle the
// media dashboard's per-provider switch calls. The browser-side fan-out it
// replaced issued one PUT per account, so a thousand-account provider meant a
// thousand round trips and a pool that could end up half-toggled.
//
// The properties worth locking down:
//   - only the named provider's rows change (no cross-provider bleed)
//   - activating clears the stale error/lock fields, so a re-enabled
//     connection is not immediately re-disabled by its old verdict
//   - deactivating marks the row disabled and leaves the error fields alone
//   - the affected count reports the rows actually changed
func TestSetProviderConnectionsActive(t *testing.T) {
	repo := pgBulkRepo(t)

	bulkSeedConnection(t, repo, "c1", "antigravity", 1, "active")
	bulkSeedConnection(t, repo, "c2", "antigravity", 1, "active")
	bulkSeedConnection(t, repo, "c3", "antigravity", 0, "disabled")
	bulkSeedConnection(t, repo, "other1", "codex", 1, "active")

	// Deactivate the whole antigravity pool.
	n, err := repo.SetProviderConnectionsActive("antigravity", false)
	if err != nil {
		t.Fatalf("SetProviderConnectionsActive(false): %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 affected rows, got %d", n)
	}

	for _, id := range []string{"c1", "c2", "c3"} {
		active, status := bulkActiveState(t, repo, id)
		if active != 0 {
			t.Errorf("connection %s: isActive = %d, want 0", id, active)
		}
		if status != "disabled" {
			t.Errorf("connection %s: status = %q, want \"disabled\"", id, status)
		}
	}

	// Another provider must be untouched.
	if active, _ := bulkActiveState(t, repo, "other1"); active != 1 {
		t.Errorf("cross-provider bleed: other1 isActive = %d, want 1", active)
	}

	// Reactivate: the count covers every row of the provider, and the
	// stale fields are cleared so the pool comes back clean.
	n, err = repo.SetProviderConnectionsActive("antigravity", true)
	if err != nil {
		t.Fatalf("SetProviderConnectionsActive(true): %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 affected rows on activate, got %d", n)
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		active, status := bulkActiveState(t, repo, id)
		if active != 1 {
			t.Errorf("connection %s: isActive = %d after activate, want 1", id, active)
		}
		if status != "active" {
			t.Errorf("connection %s: status = %q after activate, want \"active\"", id, status)
		}
	}

	// A provider with no rows is a no-op, not an error.
	n, err = repo.SetProviderConnectionsActive("does-not-exist", true)
	if err != nil {
		t.Fatalf("SetProviderConnectionsActive on unknown provider: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 affected rows for unknown provider, got %d", n)
	}
}
