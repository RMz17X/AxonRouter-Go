//go:build sqlite_legacy && ignore

package smart_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/smart"
	_ "modernc.org/sqlite"
)

func setupCanaryTestDB(t *testing.T) (*db.Repo, *sql.DB) {
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

func TestCanaryWorker_ProbeAndAutoDisable(t *testing.T) {
	repo, rawDB := setupCanaryTestDB(t)
	defer rawDB.Close()

	// Setup mock relay server
	probeCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCount++
		// simulate failure: 500 error
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Insert edge relay pool pointing to mock server
	p, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "flaky-relay",
		ProxyURL: srv.URL,
		Type:     "cloudflare",
	})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}
	poolID := p["id"].(string)

	tracker := smart.NewFitnessTracker()
	worker := smart.NewCanaryWorker(repo, tracker)

	// Run ProbeAll 9 times: pool should still be active
	for i := 1; i <= 9; i++ {
		worker.ProbeAll(context.Background())
		fails := tracker.GetProbeFailures(poolID)
		if fails != uint64(i) {
			t.Fatalf("expected %d fails, got %d", i, fails)
		}
		pool, _ := repo.GetProxyPool(poolID)
		if !pool.IsActive {
			t.Fatalf("pool should still be active at %d failures", i)
		}
	}

	// 10th failure: MaxProbeFailures reached -> pool should be disabled!
	worker.ProbeAll(context.Background())
	fails := tracker.GetProbeFailures(poolID)
	if fails != 10 {
		t.Fatalf("expected 10 fails, got %d", fails)
	}

	pool, _ := repo.GetProxyPool(poolID)
	if pool.IsActive {
		t.Fatalf("pool should be disabled after 10 consecutive failures!")
	}
}

func TestCanaryWorker_ProbeSuccessClearsFailures(t *testing.T) {
	repo, rawDB := setupCanaryTestDB(t)
	defer rawDB.Close()

	// Mock server answering cloudflare trace format
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fl=123f45\nh=cloudflare.com\nip=1.2.3.4\n"))
	}))
	defer srv.Close()

	p, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "good-relay",
		ProxyURL: srv.URL,
		Type:     "cloudflare",
	})
	if err != nil {
		t.Fatalf("insert pool: %v", err)
	}
	poolID := p["id"].(string)

	tracker := smart.NewFitnessTracker()
	worker := smart.NewCanaryWorker(repo, tracker)

	// Set 5 prior failures manually
	tracker.RecordProbe(poolID, false, 100*time.Millisecond)
	tracker.RecordProbe(poolID, false, 100*time.Millisecond)

	// Run probe -> should succeed and clear failure counter to 0
	worker.ProbeAll(context.Background())
	if fails := tracker.GetProbeFailures(poolID); fails != 0 {
		t.Fatalf("expected failure counter reset to 0, got %d", fails)
	}

	pool, _ := repo.GetProxyPool(poolID)
	if !pool.IsActive {
		t.Fatalf("pool should be active")
	}
}
