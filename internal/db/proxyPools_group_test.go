//go:build sqlite_legacy && ignore

package db_test

import (
	"database/sql"
	"testing"

	"axonrouter/proxy/internal/db"
	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) (*db.Repo, *sql.DB) {
	t.Helper()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	return db.NewRepo(d), d
}

func TestProxyPool_GroupAndTargetFiltering(t *testing.T) {
	repo, rawDB := setupTestDB(t)
	defer rawDB.Close()

	// 1. Insert pool with Group and Type
	p1, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "cf-pool-1",
		ProxyURL: "https://cf1.example.com",
		Type:     "cloudflare",
		Group:    "premium",
	})
	if err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	id1 := p1["id"].(string)

	p2, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "cf-pool-2",
		ProxyURL: "https://cf2.example.com",
		Type:     "cloudflare",
		Group:    "standard",
	})
	if err != nil {
		t.Fatalf("insert p2: %v", err)
	}
	id2 := p2["id"].(string)

	p3, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "http-pool-1",
		ProxyURL: "http://1.2.3.4:8080",
		Type:     "http",
		Group:    "premium",
	})
	if err != nil {
		t.Fatalf("insert p3: %v", err)
	}
	_ = p3["id"]

	// Verify Group retrieval in GetProxyPool
	read1, err := repo.GetProxyPool(id1)
	if err != nil {
		t.Fatalf("get p1: %v", err)
	}
	if read1.Group != "premium" {
		t.Errorf("expected group 'premium', got %q", read1.Group)
	}

	// 2. ActivePoolsByTarget with type:cloudflare
	cfPools := repo.ActivePoolsByTarget("type:cloudflare")
	if len(cfPools) != 2 {
		t.Fatalf("expected 2 cloudflare pools, got %d", len(cfPools))
	}

	// ActivePoolsByTarget with shorthand "cloudflare"
	cfShort := repo.ActivePoolsByTarget("cloudflare")
	if len(cfShort) != 2 {
		t.Fatalf("expected 2 cloudflare pools via shorthand, got %d", len(cfShort))
	}

	// 3. ActivePoolsByTarget with group:premium
	premPools := repo.ActivePoolsByTarget("group:premium")
	if len(premPools) != 2 {
		t.Fatalf("expected 2 premium pools (id1 and id3), got %d", len(premPools))
	}

	// ActivePoolsByTarget with custom group name directly
	premDirect := repo.ActivePoolsByTarget("premium")
	if len(premDirect) != 2 {
		t.Fatalf("expected 2 premium pools via direct name, got %d", len(premDirect))
	}

	// 4. Exact pool ID
	singlePool := repo.ActivePoolsByTarget(id2)
	if len(singlePool) != 1 || singlePool[0].ID != id2 {
		t.Fatalf("expected exact pool id2 match")
	}

	// 5. Test DisableProxyPool
	if err := repo.DisableProxyPool(id1); err != nil {
		t.Fatalf("disable p1: %v", err)
	}
	readDisabled, _ := repo.GetProxyPool(id1)
	if readDisabled.IsActive {
		t.Errorf("expected id1 to be inactive after DisableProxyPool")
	}

	// Now type:cloudflare should only return 1 active pool (id2)
	cfAfterDisable := repo.ActivePoolsByTarget("type:cloudflare")
	if len(cfAfterDisable) != 1 || cfAfterDisable[0].ID != id2 {
		t.Fatalf("expected only 1 active cloudflare pool after disabling id1")
	}
}
