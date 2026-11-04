package dashboard

// Batch 6 — the /api/morph family, ported from AxonRouter
// src/app/api/morph/** (route.ts wins: the .ts files are the live
// implementations; the .js twins are older copies).
//
// test-key validates one key from the settings.morph pool against the
// Morph API and persists the resulting status patch, mirroring
// buildMorphKeyStatusPatch: 2xx → active; 429 → cooldown with a 14-day
// nextRetryAt; exhausted on 402, or on 403 whose body talks about
// credits/quota; inactive on invalid-key wording at 400/401/403;
// unknown otherwise. The answer is {ok, email, status, isExhausted,
// lastError} at 200 when active, else 409.
//
// usage/stats + usage/requests read the gateway's usage ledger
// (usageHistory rows with provider='morph' — Axon filters its
// usage_events on source='morph'). Axon's DB query layer hard-codes
// credits to 0 and applies a 7-day window for every period except 24h;
// both quirks are mirrored exactly. Capability is derived by
// reverse-mapping the model through Axon's capability default models
// (its category column has no Go counterpart), else "unknown" —
// Axon's own fallback. byApiKey groups on "Unknown email", Axon's
// fallback label: Go's ledger stores the masked upstream credential,
// not the key's email label.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

const morphCooldown = 14 * 24 * time.Hour

var morphExhaustedMarkers = []string{
	"credit", "quota", "exhaust", "rate limit", "too many requests",
	"insufficient", "payment required", "billing", "monthly limit",
}

var morphInvalidKeyMarkers = []string{
	"invalid api key", "invalid_api_key", "api key is invalid",
	"invalid key", "unauthorized", "authentication", "invalid bearer",
	"bad credentials", "token is invalid", "token invalid",
}

func morphISO(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func morphContainsAny(text string, markers []string) bool {
	lower := strings.ToLower(text)
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// buildMorphKeyStatusPatch mirrors test-key/shared.ts.
func buildMorphKeyStatusPatch(status int, responseText string, now time.Time) map[string]any {
	if status >= 200 && status < 300 {
		return map[string]any{
			"status":        "active",
			"isExhausted":   false,
			"lastCheckedAt": morphISO(now),
			"lastError":     "",
			"nextRetryAt":   nil,
		}
	}
	errorText := responseText
	if errorText == "" {
		errorText = fmt.Sprintf("HTTP %d", status)
	}
	patch := map[string]any{
		"lastCheckedAt": morphISO(now),
		"lastError":     errorText,
		"nextRetryAt":   nil,
	}
	switch {
	case status == http.StatusTooManyRequests:
		patch["status"] = "cooldown"
		patch["isExhausted"] = false
		patch["nextRetryAt"] = morphISO(now.Add(morphCooldown))
	case status == http.StatusPaymentRequired ||
		(status == http.StatusForbidden && morphContainsAny(responseText, morphExhaustedMarkers)):
		patch["status"] = "exhausted"
		patch["isExhausted"] = true
	case (status == 400 || status == 401 || status == 403) &&
		morphContainsAny(responseText, morphInvalidKeyMarkers):
		patch["status"] = "inactive"
		patch["isExhausted"] = false
	default:
		patch["status"] = "unknown"
		patch["isExhausted"] = false
	}
	return patch
}

// HandleMorphTestKey serves POST /api/morph/test-key {email}.
func HandleMorphTestKey(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int, message string) {
			handlerutil.WriteJSON(w, status, map[string]any{"error": message})
		}
		var reqBody struct {
			Email string `json:"email"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		email := strings.ToLower(strings.TrimSpace(reqBody.Email))
		if email == "" {
			fail(http.StatusBadRequest, "Email is required")
			return
		}

		settings, err := repo.GetSettingsRaw()
		if err != nil {
			fail(http.StatusInternalServerError, "Failed to test Morph API key")
			return
		}
		morph, _ := settings["morph"].(map[string]any)
		baseURL, _ := morph["baseUrl"].(string)
		apiKeys, _ := morph["apiKeys"].([]any)

		entryIndex := -1
		var entry map[string]any
		for i, item := range apiKeys {
			candidate, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if stored, _ := candidate["email"].(string); stored == email {
				entryIndex, entry = i, candidate
				break
			}
		}
		key := ""
		if entry != nil {
			key, _ = entry["key"].(string)
		}
		if entry == nil || key == "" {
			fail(http.StatusNotFound, "Morph API key not found")
			return
		}

		// Axon: new URL("/v1/chat/completions", baseUrl + "/") — the
		// base's path is replaced by the chat path.
		base, err := url.Parse(strings.TrimSuffix(baseURL, "/") + "/")
		if err != nil || base.Host == "" {
			fail(http.StatusInternalServerError, "Failed to test Morph API key")
			return
		}
		base.Path = "/v1/chat/completions"
		base.RawQuery = ""

		probeBody, _ := json.Marshal(map[string]any{
			"model": "morph-v3-fast",
			"messages": []any{
				map[string]any{
					"role":    "user",
					"content": "<instruction>Reply with exactly OK</instruction>",
				},
			},
		})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base.String(),
			strings.NewReader(string(probeBody)))
		if err != nil {
			fail(http.StatusInternalServerError, "Failed to test Morph API key")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			fail(http.StatusInternalServerError, "Failed to test Morph API key")
			return
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		patch := buildMorphKeyStatusPatch(resp.StatusCode, string(respBody), time.Now())
		for k, v := range patch {
			entry[k] = v
		}
		apiKeys[entryIndex] = entry
		morph["apiKeys"] = apiKeys
		if err := repo.UpdateSettingsRaw(map[string]any{"morph": morph}); err != nil {
			fail(http.StatusInternalServerError, "Failed to test Morph API key")
			return
		}

		status, _ := patch["status"].(string)
		isExhausted, _ := patch["isExhausted"].(bool)
		lastError, _ := patch["lastError"].(string)
		httpStatus := http.StatusConflict
		if status == "active" {
			httpStatus = http.StatusOK
		}
		handlerutil.WriteJSON(w, httpStatus, map[string]any{
			"ok":          status == "active",
			"email":       email,
			"status":      status,
			"isExhausted": isExhausted,
			"lastError":   lastError,
		})
	}
}

// morphCapabilityByModel reverse-maps a Morph model id to Axon's
// capability names via MORPH_CORE_INTERNAL_MODELS.
var morphCapabilityByModel = map[string]string{
	"morph-v3-large":       "apply",
	"morph-warp-grep-v2.1": "warpgrep",
	"morph-compactor":      "compact",
}

func morphCapability(model string) string {
	if capability, ok := morphCapabilityByModel[model]; ok {
		return capability
	}
	return "unknown"
}

type morphUsageRow struct {
	timestamp     string
	model         string
	endpoint      string
	promptTokens  int
	completionTok int
	status        string
}

func queryMorphUsageRows(repo *db.Repo, since string, limit int) ([]morphUsageRow, error) {
	query := `SELECT timestamp, model, endpoint, promptTokens, completionTokens, status
		FROM usageHistory WHERE provider = 'morph'`
	args := []any{}
	if since != "" {
		query += ` AND timestamp >= ?`
		args = append(args, since)
	}
	query += ` ORDER BY timestamp DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := repo.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]morphUsageRow, 0)
	for rows.Next() {
		var row morphUsageRow
		var endpoint, status sql.NullString
		if err := rows.Scan(&row.timestamp, &row.model, &endpoint, &row.promptTokens, &row.completionTok, &status); err != nil {
			return nil, err
		}
		row.endpoint = endpoint.String
		row.status = status.String
		out = append(out, row)
	}
	return out, rows.Err()
}

type morphGroup struct {
	Requests     int    `json:"requests"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	Credits      int    `json:"credits"`
	KeyField     string `json:"-"`
	KeyValue     string `json:"-"`
}

// morphGroupJSON renders a group with its discriminator field, like
// Axon's buildGroup ({...totals, [keyField]: key}).
func morphGroupJSON(g *morphGroup) map[string]any {
	return map[string]any{
		"requests":     g.Requests,
		"inputTokens":  g.InputTokens,
		"outputTokens": g.OutputTokens,
		"credits":      0,
		g.KeyField:     g.KeyValue,
	}
}

func morphRecentRequest(row morphUsageRow) map[string]any {
	status := row.status
	if status == "" {
		status = "ok"
	}
	entrypoint := row.endpoint
	if entrypoint == "" {
		entrypoint = "unknown"
	}
	return map[string]any{
		"timestamp":      row.timestamp,
		"capability":     morphCapability(row.model),
		"entrypoint":     entrypoint,
		"source":         "morph",
		"method":         "POST",
		"model":          row.model,
		"resolvedModel":  row.model,
		"requestedModel": row.model,
		"apiKeyLabel":    "Unknown email",
		"inputTokens":    row.promptTokens,
		"outputTokens":   row.completionTok,
		"credits":        0,
		"status":         status,
		"upstreamStatus": nil,
		"error":          nil,
	}
}

// HandleMorphUsageStats serves GET /api/morph/usage/stats?period=.
func HandleMorphUsageStats(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int, message string) {
			handlerutil.WriteJSON(w, status, map[string]any{"error": message})
		}
		period := r.URL.Query().Get("period")
		if period == "" {
			period = "7d"
		}
		switch period {
		case "24h", "7d", "30d", "60d", "all":
		default:
			fail(http.StatusBadRequest, "Invalid period")
			return
		}
		// Axon's DB layer: only 24h gets a 24h window; every other
		// period aggregates 7 days. Lifetime counts all rows.
		window := 7 * 24 * time.Hour
		if period == "24h" {
			window = 24 * time.Hour
		}
		since := time.Now().Add(-window).UTC().Format(time.RFC3339)

		rows, err := queryMorphUsageRows(repo, since, 0)
		if err != nil {
			fail(http.StatusInternalServerError, "Failed to fetch Morph usage stats")
			return
		}
		var lifetime int
		if err := repo.QueryRow(
			`SELECT COUNT(*) FROM usageHistory WHERE provider = 'morph'`).Scan(&lifetime); err != nil {
			fail(http.StatusInternalServerError, "Failed to fetch Morph usage stats")
			return
		}

		byCapability := map[string]*morphGroup{}
		byModel := map[string]*morphGroup{}
		byAPIKey := map[string]*morphGroup{}
		byEntrypoint := map[string]*morphGroup{}
		addTo := func(groups map[string]*morphGroup, keyField, key string, row morphUsageRow) {
			g, ok := groups[key]
			if !ok {
				g = &morphGroup{KeyField: keyField, KeyValue: key}
				groups[key] = g
			}
			g.Requests++
			g.InputTokens += row.promptTokens
			g.OutputTokens += row.completionTok
		}
		totalInput, totalOutput := 0, 0
		for _, row := range rows {
			totalInput += row.promptTokens
			totalOutput += row.completionTok
			model := row.model
			if model == "" {
				model = "unknown"
			}
			entrypoint := row.endpoint
			if entrypoint == "" {
				entrypoint = "unknown"
			}
			addTo(byCapability, "capability", morphCapability(row.model), row)
			addTo(byModel, "model", model, row)
			addTo(byAPIKey, "apiKeyLabel", "Unknown email", row)
			addTo(byEntrypoint, "entrypoint", entrypoint, row)
		}
		render := func(groups map[string]*morphGroup) map[string]any {
			out := map[string]any{}
			for key, g := range groups {
				out[key] = morphGroupJSON(g)
			}
			return out
		}
		recent := make([]any, 0, 20)
		for i, row := range rows {
			if i >= 20 {
				break
			}
			recent = append(recent, morphRecentRequest(row))
		}

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"totalRequests":         len(rows),
			"totalInputTokens":      totalInput,
			"totalOutputTokens":     totalOutput,
			"totalCredits":          0,
			"totalRequestsLifetime": lifetime,
			"byCapability":          render(byCapability),
			"byModel":               render(byModel),
			"byApiKey":              render(byAPIKey),
			"byEntrypoint":          render(byEntrypoint),
			"recentRequests":        recent,
		})
	}
}

// HandleMorphUsageRequests serves GET /api/morph/usage/requests?limit=.
func HandleMorphUsageRequests(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int, message string) {
			handlerutil.WriteJSON(w, status, map[string]any{"error": message})
		}
		limit := 200
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		if limit > 500 {
			limit = 500
		}
		rows, err := queryMorphUsageRows(repo, "", limit)
		if err != nil {
			fail(http.StatusInternalServerError, "Failed to fetch Morph request logs")
			return
		}
		out := make([]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, morphRecentRequest(row))
		}
		handlerutil.WriteJSON(w, http.StatusOK, out)
	}
}
