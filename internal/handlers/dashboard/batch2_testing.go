package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

// The connection test routes of batch 2: {id}/test-model and
// {id}/test-models (ports of AxonRouter's ping-based routes under
// providers/[id]) and providers/test-batch (a port of the sequential
// batch tester that reuses the single-connection probe machinery Axon
// runs against /api/providers/{id}/test).

// pingFullModel resolves the provider alias and prefixes the model the way
// the AxonRouter routes do: unless the model already starts with the
// alias prefix, the full id is "<alias>/<model>".
func pingFullModel(providerID, model string) (alias, fullModel string) {
	alias = providers.GetProviderAlias(providerID)
	if strings.HasPrefix(model, alias+"/") {
		return alias, model
	}
	return alias, alias + "/" + model
}

type testModelResponse struct {
	ConnectionID string `json:"connectionId"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	FullModel    string `json:"fullModel"`
	pingResult
}

// POST /api/providers/{id}/test-model — pings one model on one connection.
func (h *DashboardHandler) HandleTestConnectionModel(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Test failed"})
		return
	}
	if conn == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Connection not found"})
		return
	}
	// request.json().catch(() => ({})): an unreadable body is an empty one.
	var body struct {
		Model string `json:"model"`
		Kind  string `json:"kind"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	if body.Model == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "model is required in request body"})
		return
	}
	kind := body.Kind
	if kind == "" {
		kind = "llm"
	}
	_, fullModel := pingFullModel(conn.Provider, body.Model)
	result, pingErr := h.pingModelByKind(r.Context(), handlerutil.RequestOrigin(r), fullModel, kind, id)
	if pingErr != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Test failed"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, testModelResponse{
		ConnectionID: id,
		Provider:     conn.Provider,
		Model:        body.Model,
		FullModel:    fullModel,
		pingResult:   result,
	})
}

type registryProbeModel struct {
	ID   string
	Name string
	Kind string
}

// registryProbeModels lists the registry models of a provider as
// {id, name, kind} probe targets. The Go registry stores model ids and a
// separate kind map, so the display name falls back to the id (the
// AxonRouter route itself falls back to name || id downstream).
func registryProbeModels(alias string) []registryProbeModel {
	ids := providers.GetProviderModels(alias)
	out := make([]registryProbeModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, registryProbeModel{ID: id, Name: id, Kind: providers.GetProviderModelKind(alias, id)})
	}
	return out
}

type testModelsResult struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
	pingResult
}

// POST /api/providers/{id}/test-models — pings every model of the
// connection's provider (first one as a warm-up, the rest in parallel).
func (h *DashboardHandler) HandleTestConnectionModels(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Test failed"})
		return
	}
	if conn == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Connection not found"})
		return
	}
	providerID := conn.Provider
	alias := providers.GetProviderAlias(providerID)
	modelsList := registryProbeModels(alias)

	// Compatible providers list their models live when the registry has
	// none — AxonRouter round-trips its own /api/providers/{id}/models;
	// here the same handler is captured in-process.
	if providers.IsCustomCompatible(providerID) && len(modelsList) == 0 {
		rec := newModelsCapture()
		h.writeConnectionModels(rec, r, id)
		var payload struct {
			Models []map[string]any `json:"models"`
		}
		if rec.status == http.StatusOK && json.Unmarshal(rec.body.Bytes(), &payload) == nil {
			for _, m := range payload.Models {
				modelID := dataStr(m, "id")
				if modelID == "" {
					modelID = dataStr(m, "name")
				}
				if modelID == "" {
					continue
				}
				name := dataStr(m, "name")
				if name == "" {
					name = modelID
				}
				kind := dataStr(m, "kind")
				if kind == "" {
					kind = dataStr(m, "type")
				}
				modelsList = append(modelsList, registryProbeModel{ID: modelID, Name: name, Kind: kind})
			}
		}
	}
	if len(modelsList) == 0 {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "No models configured for this provider"})
		return
	}

	origin := handlerutil.RequestOrigin(r)
	pingOne := func(m registryProbeModel) (testModelsResult, error) {
		kind := m.Kind
		if kind == "" {
			kind = "llm"
		}
		_, fullModel := pingFullModel(providerID, m.ID)
		res, pingErr := h.pingModelByKind(r.Context(), origin, fullModel, kind, id)
		if pingErr != nil {
			return testModelsResult{}, pingErr
		}
		return testModelsResult{ModelID: m.ID, Name: m.Name, pingResult: res}, nil
	}

	results := make([]testModelsResult, len(modelsList))
	first, pingErr := pingOne(modelsList[0])
	if pingErr != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Test failed"})
		return
	}
	results[0] = first
	var wg sync.WaitGroup
	errs := make([]error, len(modelsList))
	for i := 1; i < len(modelsList); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, pingErr := pingOne(modelsList[i])
			if pingErr != nil {
				errs[i] = pingErr
				return
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	for _, pingErr := range errs {
		if pingErr != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Test failed"})
			return
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"provider":     providerID,
		"connectionId": id,
		"results":      results,
	})
}

// ---------------------------------------------------------------------------
// POST /api/providers/test-batch
// ---------------------------------------------------------------------------

// getAuthGroup classifies a connection the way AxonRouter's test-batch
// route does, with the registry-derived sets standing in for its
// FREE_PROVIDERS / OAUTH_PROVIDERS / APIKEY_PROVIDERS constants: a provider
// is "free" when its registry entry is in the free category, "oauth" /
// "apikey" by its registry auth type.
func getAuthGroup(c *models.ProviderConnection) string {
	canonical := providers.ResolveAlias(c.Provider)
	meta, hasMeta := providers.ProviderRegistry[canonical]
	isFree := hasMeta && (meta.Category == providers.CategoryFree || meta.NoAuth)
	if c.AuthType != "" {
		if c.AuthType == "oauth" {
			if isFree {
				return "free"
			}
			return "oauth"
		}
		return c.AuthType
	}
	switch {
	case isFree:
		return "free"
	case hasMeta && meta.AuthType == "oauth":
		return "oauth"
	case hasMeta && meta.AuthType == "apikey":
		return "apikey"
	case providers.IsCustomCompatible(canonical):
		return "compatible"
	default:
		return "apikey"
	}
}

func (h *DashboardHandler) HandleTestBatch(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var body struct {
		Mode       string `json:"mode"`
		ProviderID string `json:"providerId"`
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || json.Unmarshal(raw, &body) != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Batch test failed"})
		return
	}
	if body.Mode == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "mode is required"})
		return
	}

	var conns []*models.ProviderConnection
	switch {
	case body.Mode == "provider" && body.ProviderID != "":
		// AxonRouter queries connections by the provider id as given.
		conns, err = h.Repo.GetProviderConnections(body.ProviderID, true)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Batch test failed"})
			return
		}
	case body.Mode == "oauth" || body.Mode == "free" || body.Mode == "apikey" ||
		body.Mode == "compatible" || body.Mode == "all":
		all, listErr := h.Repo.GetProviderConnections("", true)
		if listErr != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Batch test failed"})
			return
		}
		for _, c := range all {
			if body.Mode == "all" {
				conns = append(conns, c)
			} else if body.Mode == "compatible" {
				if providers.IsCustomCompatible(c.Provider) {
					conns = append(conns, c)
				}
			} else if getAuthGroup(c) == body.Mode {
				conns = append(conns, c)
			}
		}
	default:
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Invalid mode. Use: provider, oauth, free, apikey, compatible, all",
		})
		return
	}

	type batchResult struct {
		Provider       string  `json:"provider"`
		ConnectionID   string  `json:"connectionId"`
		ConnectionName string  `json:"connectionName"`
		AuthType       string  `json:"authType"`
		Valid          bool    `json:"valid"`
		LatencyMs      int64   `json:"latencyMs"`
		Error          *string `json:"error"`
		Diagnosis      any     `json:"diagnosis"`
		StatusCode     any     `json:"statusCode"`
		TestedAt       string  `json:"testedAt"`
	}
	results := make([]batchResult, 0, len(conns))
	passed := 0
	for _, conn := range conns {
		rawData, _ := decodeConnectionData(conn.Data)
		probeData := parseConnectionProbeData(rawData)
		start := time.Now()
		outcome := h.testSingleConnection(r.Context(), conn, probeData, rawData)
		latency := time.Since(start).Milliseconds()
		// The persisted probe verdict is AxonRouter's saveTestResult step.
		h.persistProbeResult(conn, rawData, outcome)
		authType := conn.AuthType
		if authType == "" {
			authType = getAuthGroup(conn)
		}
		item := batchResult{
			Provider:       conn.Provider,
			ConnectionID:   conn.ID,
			ConnectionName: displayNameOr(conn, conn.Provider),
			AuthType:       authType,
			Valid:          outcome.valid,
			LatencyMs:      latency,
			TestedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}
		// diagnosis / statusCode stay null: the Go probe outcome carries
		// neither (AxonRouter's are null on this path too — its diagnosis
		// enrichment only runs for a wrapped error shape the Go probe does
		// not produce).
		if outcome.message != "" {
			item.Error = &outcome.message
		}
		if outcome.valid {
			passed++
		}
		results = append(results, item)
	}

	var providerIDOut any
	if body.ProviderID != "" {
		providerIDOut = body.ProviderID
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"mode":       body.Mode,
		"providerId": providerIDOut,
		"results":    results,
		"testedAt":   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"summary": map[string]any{
			"total":  len(results),
			"passed": passed,
			"failed": len(results) - passed,
		},
	})
}
