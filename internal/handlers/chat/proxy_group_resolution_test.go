//go:build sqlite_legacy

package chat

import (
	"context"
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/proxy/smart"
)

func TestGetClientForConnection_ProxyGroupResolution(t *testing.T) {
	repo := setupProxyPools(t)

	// Create 2 cloudflare pools and 1 custom group pool
	cf1, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "CF Relay 1",
		Type:     "cloudflare",
		ProxyURL: "https://cf1.test.workers.dev",
	})
	if err != nil {
		t.Fatalf("create cf1: %v", err)
	}
	id1 := cf1["id"].(string)

	cf2, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "CF Relay 2",
		Type:     "cloudflare",
		ProxyURL: "https://cf2.test.workers.dev",
	})
	if err != nil {
		t.Fatalf("create cf2: %v", err)
	}
	id2 := cf2["id"].(string)

	cust1, err := repo.InsertProxyPool(db.ProxyPoolData{
		Name:     "Custom Pool",
		Type:     "http",
		Group:    "dedicated-asia",
		ProxyURL: "http://1.2.3.4:8080",
	})
	if err != nil {
		t.Fatalf("create cust1: %v", err)
	}
	_ = cust1

	defaultClient := &http.Client{Timeout: 5 * time.Second}
	h := &ChatHandler{
		Repo:   repo,
		Client: defaultClient,
	}

	// 1. Resolve by type:cloudflare
	connData := &ConnectionData{
		Provider:    "openai",
		ProxyPoolID: "type:cloudflare",
	}
	client, err := h.getClientForConnection(connData)
	if err != nil {
		t.Fatalf("unexpected error resolving type:cloudflare: %v", err)
	}
	if client == nil {
		t.Fatalf("expected non-nil client")
	}
	// ResolvedProxyPool should be one of the CF pools
	if connData.ResolvedProxyPool != "CF Relay 1" && connData.ResolvedProxyPool != "CF Relay 2" {
		t.Fatalf("expected resolved name to be CF Relay 1 or 2, got %q", connData.ResolvedProxyPool)
	}

	// 2. Penalize cf1 with failures so cf2 has higher fitness
	for i := 0; i < 5; i++ {
		smart.DefaultTracker.RecordFailure("openai", id1, context.DeadlineExceeded)
	}
	smart.DefaultTracker.RecordSuccess("openai", id2, 50*time.Millisecond)

	connData2 := &ConnectionData{
		Provider:    "openai",
		ProxyPoolID: "cloudflare", // shorthand
	}
	_, err = h.getClientForConnection(connData2)
	if err != nil {
		t.Fatalf("unexpected error resolving shorthand cloudflare: %v", err)
	}
	if connData2.ResolvedProxyPool != "CF Relay 2" {
		t.Fatalf("expected CF Relay 2 with higher fitness, got %q", connData2.ResolvedProxyPool)
	}

	// 3. Resolve by custom group name
	connDataCust := &ConnectionData{
		Provider:    "anthropic",
		ProxyPoolID: "group:dedicated-asia",
	}
	_, err = h.getClientForConnection(connDataCust)
	if err != nil {
		t.Fatalf("unexpected error resolving group:dedicated-asia: %v", err)
	}
	if connDataCust.ResolvedProxyPool != "Custom Pool" {
		t.Fatalf("expected Custom Pool, got %q", connDataCust.ResolvedProxyPool)
	}

	// 4. Fallback to direct when group has no active proxies and strictProxy is false
	connDataEmpty := &ConnectionData{
		Provider:    "openai",
		ProxyPoolID: "group:non-existent",
		StrictProxy: false,
	}
	cliFallback, err := h.getClientForConnection(connDataEmpty)
	if err != nil {
		t.Fatalf("expected fallback to direct without error, got: %v", err)
	}
	if cliFallback != defaultClient {
		t.Fatalf("expected fallback to defaultClient")
	}

	// 5. Fail closed when group has no active proxies and strictProxy is true
	connDataStrict := &ConnectionData{
		Provider:    "openai",
		ProxyPoolID: "group:non-existent",
		StrictProxy: true,
	}
	_, err = h.getClientForConnection(connDataStrict)
	if err == nil {
		t.Fatalf("expected error in strict mode when group has no active proxies, got nil")
	}
}
