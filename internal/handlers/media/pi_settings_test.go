package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piTestHome points the home directory at a scratch directory and empties the
// PATH for the whole test, so neither the real ~/.pi nor a real `pi` binary
// can leak into the assertions.
func piTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	return home
}

func piWriteConfig(t *testing.T, relPath, content string) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

func piBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response %s: %v", rec.Body.String(), err)
	}
	return body
}

func TestPiSettingsGet(t *testing.T) {
	t.Run("without an install answers installed false", func(t *testing.T) {
		piTestHome(t)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/cli-tools/pi-settings", nil))

		body := piBody(t, rec)
		if rec.Code != http.StatusOK || body["installed"] != false {
			t.Fatalf("expected installed=false, got %d %v", rec.Code, body)
		}
		if body["config"] != nil {
			t.Errorf("expected config null, got %v", body["config"])
		}
	})

	t.Run("an agent-scoped models.json is reported with its axonrouter state", func(t *testing.T) {
		piTestHome(t)
		piWriteConfig(t, filepath.Join(".pi", "agent", "models.json"),
			`{"providers":{"axonrouter":{"baseUrl":"http://localhost:20130/v1","models":[{"id":"gpt-5"}]}},"other":1}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/cli-tools/pi-settings", nil))

		body := piBody(t, rec)
		if body["installed"] != true || body["hasAxonRouter"] != true {
			t.Fatalf("expected installed+hasAxonRouter, got %v", body)
		}
		pi, _ := body["pi"].(map[string]any)
		if pi["baseURL"] != "http://localhost:20130/v1" {
			t.Errorf("expected pi.baseURL relayed, got %v", pi)
		}
		config, _ := body["config"].(map[string]any)
		if _, ok := config["other"]; !ok {
			t.Errorf("expected the whole config relayed, got %v", config)
		}
		if !strings.HasSuffix(body["configPath"].(string), filepath.Join(".pi", "agent", "models.json")) {
			t.Errorf("expected the agent path, got %v", body["configPath"])
		}
	})

	t.Run("a foreign provider block does not count as axonrouter", func(t *testing.T) {
		piTestHome(t)
		piWriteConfig(t, filepath.Join(".pi", "models.json"),
			`{"providers":{"custom":{"baseUrl":"http://127.0.0.1:20128/v1"}}}`)
		rec := httptest.NewRecorder()
		NewPiHandler(nil).HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/cli-tools/pi-settings", nil))

		if body := piBody(t, rec); body["hasAxonRouter"] != false {
			t.Fatalf("expected hasAxonRouter=false under the Axon rule (providers.axonrouter presence), got %v", body)
		}
	})
}
