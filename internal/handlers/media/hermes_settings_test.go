package media

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("Unmarshal body %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestHermesGet_InvalidProfileIs400(t *testing.T) {
	hermesTestHome(t)
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-settings?profile=..%2Fsecrets", nil)
	rec := httptest.NewRecorder()
	h.HandleGet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestHermesGet_MissingNamedProfileIs404(t *testing.T) {
	root := hermesTestHome(t)
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-settings?profile=ghost", nil)
	rec := httptest.NewRecorder()
	h.HandleGet(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	// The default profile must stay reachable even though the named one is not:
	// a missing profile is a dashboard-selection problem, not a missing install.
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("telemetry: false\n"), 0o644); err != nil {
		t.Fatalf("seed default config: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-settings", nil)
	rec2 := httptest.NewRecorder()
	h.HandleGet(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("default profile status = %d, want 200 (body %q)", rec2.Code, rec2.Body.String())
	}
}

func TestHermesProfiles_EndpointListsEveryProfile(t *testing.T) {
	root := hermesTestHome(t)
	writeProfile(t, root, "work", "")
	h := NewHermesHandler()

	req := httptest.NewRequest(http.MethodGet, "/api/cli-tools/hermes-profiles", nil)
	rec := httptest.NewRecorder()
	h.HandleProfiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	profiles, _ := decodeJSON(t, rec)["profiles"].([]any)
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	first, _ := profiles[0].(map[string]any)
	if first["name"] != "default" || first["isDefault"] != true {
		t.Errorf("default entry = %+v", first)
	}
}
