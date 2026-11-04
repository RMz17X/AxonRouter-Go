package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// ---------------------------------------------------------------------------
// POST /api/providers/{id}/sync-models
// ---------------------------------------------------------------------------

type connectionModelsCapture struct {
	Status  int
	Models  []any
	Warning string
	ErrText string
}

// fetchConnectionModels runs the connection models handler in-process, the
// way AxonRouter's sync-models fetches its own /models route over HTTP.
func (h *DashboardHandler) fetchConnectionModels(r *http.Request, connectionID string) connectionModelsCapture {
	rec := newModelsCapture()
	h.writeConnectionModels(rec, r, connectionID)
	capture := connectionModelsCapture{Status: rec.status}
	if capture.Status == 0 {
		capture.Status = http.StatusOK
	}
	body := rec.body.Bytes()
	if len(body) == 0 {
		capture.ErrText = "Empty response body from /models"
		return capture
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		capture.ErrText = "Invalid JSON response from /models"
		return capture
	}
	if models, ok := payload["models"].([]any); ok {
		capture.Models = models
	}
	capture.Warning = dataStr(payload, "warning")
	capture.ErrText = dataStr(payload, "error")
	if capture.ErrText == "" {
		// The Go models handler reports failures in the standard error
		// envelope ({error: {message}}); AxonRouter's inner route uses a
		// flat string, which is what its sync route relays.
		if envelope, ok := payload["error"].(map[string]any); ok {
			capture.ErrText = dataStr(envelope, "message")
		}
	}
	return capture
}

func (h *DashboardHandler) HandleSyncConnectionModels(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to sync models"})
		return
	}
	if conn == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Connection not found"})
		return
	}
	providerID := conn.Provider
	mode := "sync"
	if r.URL.Query().Get("mode") == "import" {
		mode = "merge"
	}

	capture := h.fetchConnectionModels(r, id)
	if capture.ErrText != "" || capture.Models == nil {
		// AxonRouter relays the inner route's failure: its status when the
		// response was not ok, 502 when an ok response was unusable.
		status := capture.Status
		message := capture.ErrText
		if capture.Status >= 200 && capture.Status <= 299 {
			if message == "" {
				message = "Invalid JSON response from /models"
			}
			status = http.StatusBadGateway
		} else if message == "" {
			message = fmt.Sprintf("Failed to fetch models: %d", capture.Status)
		}
		handlerutil.WriteJSON(w, status, map[string]any{"error": message})
		return
	}

	previous := h.loadSyncedModelsFor(providerID)[id]
	discovered := normalizeDiscoveredModels(capture.Models)
	if storeErr := h.storeSyncedModels(providerID, id, discovered); storeErr != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to sync models"})
		return
	}
	stored := h.loadSyncedModelsFor(providerID)[id]
	if stored == nil {
		stored = []map[string]any{}
	}

	// Import change accounting against the previously imported list.
	previousByID := map[string]map[string]any{}
	for _, m := range previous {
		if normalizeManagedSource(m["source"]) != "imported" {
			continue
		}
		comparable := importedComparable(m)
		previousByID[firstModelString(comparable["id"])] = comparable
	}
	changes := map[string]int{"added": 0, "updated": 0, "unchanged": 0}
	for _, m := range discovered {
		comparable := importedComparable(m)
		modelID := firstModelString(comparable["id"])
		prev, seen := previousByID[modelID]
		switch {
		case !seen:
			changes["added"]++
		case jsonDeepEqual(prev, comparable):
			changes["unchanged"]++
		default:
			changes["updated"]++
		}
	}
	changes["total"] = changes["added"] + changes["updated"]

	var warning any
	if capture.Warning != "" {
		warning = capture.Warning
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     providerID,
		"connectionId": id,
		"fetchedCount": len(capture.Models),
		"syncedCount":  len(stored),
		"importedChanges": map[string]any{
			"added":     changes["added"],
			"updated":   changes["updated"],
			"unchanged": changes["unchanged"],
			"total":     changes["total"],
		},
		"models":  stored,
		"mode":    mode,
		"warning": warning,
	})
}

// ---------------------------------------------------------------------------
// POST /api/providers/noauth-sync
// ---------------------------------------------------------------------------

var noauthHTTPClient = &http.Client{Timeout: 10 * time.Second}

func fetchNoauthModels(r *http.Request, fetcherURL string) []any {
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetcherURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := noauthHTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil
	}
	var payload any
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	switch p := payload.(type) {
	case []any:
		return p
	case map[string]any:
		if data, ok := p["data"].([]any); ok {
			return data
		}
		if models, ok := p["models"].([]any); ok {
			return models
		}
	}
	return nil
}

func (h *DashboardHandler) HandleNoauthSync(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var body struct {
		ProviderID string `json:"providerId"`
	}
	raw, _ := io.ReadAll(r.Body)
	target := ""
	if json.Unmarshal(raw, &body) == nil {
		target = strings.TrimSpace(body.ProviderID)
	}

	type syncTarget struct {
		id  string
		url string
		typ string
	}
	targets := make([]syncTarget, 0)
	for id, meta := range providers.ProviderRegistry {
		if !meta.NoAuth || meta.ModelsFetcher == nil || meta.ModelsFetcher.URL == "" {
			continue
		}
		if target != "" && id != target {
			continue
		}
		targets = append(targets, syncTarget{id: id, url: meta.ModelsFetcher.URL, typ: meta.ModelsFetcher.Type})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })

	results := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		func() {
			result := map[string]any{"providerId": t.id, "ok": false, "count": 0}
			defer func() { results = append(results, result) }()
			models := fetchNoauthModels(r, t.url)
			if len(models) == 0 {
				result["error"] = "No models returned"
				return
			}
			if t.typ == "opencode-free" {
				filtered := make([]any, 0, len(models))
				for _, m := range models {
					mm, ok := m.(map[string]any)
					if !ok {
						continue
					}
					id := firstModelString(mm["id"])
					if strings.HasSuffix(id, "-free") || id == "big-pickle" {
						filtered = append(filtered, m)
					}
				}
				models = filtered
				if len(models) == 0 {
					result["error"] = "No free models found after filtering"
					return
				}
			}
			normalized := normalizeDiscoveredModels(models)
			if err := h.storeSyncedModels(t.id, noauthConnectionID, normalized); err != nil {
				result["error"] = err.Error()
				return
			}
			result["ok"] = true
			result["count"] = len(normalized)
		}()
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
}
