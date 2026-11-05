//go:build integration

package bootfx

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"axonrouter/proxy/internal/db"
)

// TestRealApplicationBootAndServesTraffic is the end-to-end smoke test: the
// production fx graph started, the schema is on disk, a client key is seeded,
// and a real chat completion travels from the HTTP listener through provider
// dispatch to the fake upstream and back. Every other integration test
// assembles the router by hand, so this is the one that fails if wiring, DI,
// config loading, the database bootstrap or the listener itself breaks.
func TestRealApplicationBootAndServesTraffic(t *testing.T) {
	t.Setenv("ALLOW_PRIVATE_NETWORKING", "true")
	t.Run("the schema was created in the configured database", func(t *testing.T) {
		// A wrong DATABASE_URL would leave the server serving from a
		// different database and every other assertion here would fail
		// confusingly. PostgreSQL folds the unquoted DDL identifier, so
		// the catalog carries the table as "apikeys" in the schema the
		// connection's search_path names.
		var name string
		err := repo.RawDB().QueryRow(
			"SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename = 'apikeys'").Scan(&name)
		if err != nil {
			t.Fatalf("query pg_tables for apikeys: %v", err)
		}
		if name != "apikeys" {
			t.Errorf("apikeys table name = %q, want apikeys", name)
		}
	})

	if err := repo.CreateApiKey("bootfx-key", testAPIKey, "BootFX", "bootfx-machine"); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	priority := 1
	data := fmt.Sprintf(`{"apiKey":"sk-bootfx-upstream","baseUrl":%q}`, upstream.URL+"/chat/completions")
	if err := repo.CreateProviderConnectionFull("bootfx-conn", "deepseek", "apikey", "BootFX DeepSeek", &priority, data); err != nil {
		t.Fatalf("seed provider connection: %v", err)
	}

	body := strings.NewReader(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"Say hello."}]}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("build chat request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read chat response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", resp.StatusCode, respBody)
	}
	if got := lastUpstreamModel(); got != "deepseek-chat" {
		t.Errorf("upstream model = %q, want deepseek-chat", got)
	}
	if !strings.Contains(string(respBody), "booted reply") {
		t.Errorf("response body = %s, want the upstream content", respBody)
	}

	t.Run("the request was recorded in usage history", func(t *testing.T) {
		// The usage ledger is written by the batched async writer
		// (25 ms flush), so the row is awaited rather than read back
		// synchronously; the assertion itself is unchanged.
		var rows []db.UsageHistoryRow
		deadline := time.Now().Add(5 * time.Second)
		for {
			var err error
			rows, err = repo.GetRecentUsageHistory(5)
			if err != nil {
				t.Fatalf("read usage history: %v", err)
			}
			if len(rows) >= 1 || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if len(rows) != 1 {
			t.Fatalf("usageHistory rows = %d, want 1", len(rows))
		}
		if rows[0].Provider != "deepseek" || rows[0].Status != "success" {
			t.Errorf("usage row = %+v, want a successful deepseek request", rows[0])
		}
	})
}

// TestUnauthenticatedRequestIsRejectedOnTheLiveServer repeats the auth
// assertion against the booted process rather than a hand-built router, so a
// middleware stack that lost its API key guard in the real wiring is caught
// even when the unit-level router still has it.
func TestUnauthenticatedRequestIsRejectedOnTheLiveServer(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /v1/models without a key = %d, want 401 (body: %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "invalid_api_key") {
		t.Errorf("401 body = %s, want the invalid_api_key error code", body)
	}
}
