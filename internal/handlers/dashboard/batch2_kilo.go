package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"axonrouter/proxy/internal/handlerutil"
)

// GET /api/providers/kilo/free-models — port of AxonRouter's
// kilo/free-models route: the Kilo gateway's free models, behind a
// one-hour in-process cache with stale-on-error fallback.

const kiloModelsURL = "https://api.kilo.ai/api/gateway/models"

var (
	kiloCacheModels []map[string]any
	kiloCacheAt     time.Time
	kiloCacheMu     sync.Mutex
)

var kiloHTTPClient = &http.Client{Timeout: 10 * time.Second}

func fetchKiloFreeModels(ctx context.Context) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kiloModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := kiloHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("Kilo API returned %d", resp.StatusCode)
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	models := make([]map[string]any, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m["isFree"] != true {
			continue
		}
		contextLength := 0
		if v, ok := m["context_length"].(float64); ok {
			contextLength = int(v)
		}
		models = append(models, map[string]any{
			"id":             m["id"],
			"name":           m["name"],
			"isFree":         true,
			"context_length": contextLength,
		})
	}
	return models, nil
}

func (h *DashboardHandler) HandleKiloFreeModels(w http.ResponseWriter, r *http.Request) {
	kiloCacheMu.Lock()
	if kiloCacheModels != nil && time.Since(kiloCacheAt) < time.Hour {
		cached := kiloCacheModels
		kiloCacheMu.Unlock()
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"models": cached, "cached": true})
		return
	}
	kiloCacheMu.Unlock()

	models, err := fetchKiloFreeModels(r.Context())
	if err != nil {
		kiloCacheMu.Lock()
		cached := kiloCacheModels
		kiloCacheMu.Unlock()
		if cached != nil {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"models": cached, "cached": true, "warning": err.Error(),
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"models": []any{}, "error": "Failed to fetch Kilo models: " + err.Error(),
		})
		return
	}

	kiloCacheMu.Lock()
	kiloCacheModels = models
	kiloCacheAt = time.Now()
	kiloCacheMu.Unlock()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"models": models, "cached": false})
}
