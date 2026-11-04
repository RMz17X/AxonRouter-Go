package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// WorkerProxy-facing internal endpoints (batch 7, AxonRouter parity):
// POST /api/internal/proxy/resolve and POST /api/internal/proxy/report
// (the report handler lives in batch7_internal_report.go).
//
// Auth model, ported faithfully: each route checks the x-internal-auth
// header against its own per-purpose token. The tokens live in the
// settings row (internalProxyResolveToken / internalProxyReportToken)
// and are auto-generated on first read, exactly like
// src/lib/internalProxyTokens.ts — so the routes are NOT additionally
// session-gated: their intended caller is the separate worker-proxy
// process, which holds only these tokens. See .changes/918 for the
// resulting auth-surface analysis.

// internalProxyTokens holds the two per-purpose internal tokens.
type internalProxyTokens struct {
	resolveToken string
	reportToken  string
}

// getInternalProxyTokens reads the tokens from settings, generating and
// persisting any that are missing (upstream's get-or-create behaviour).
func getInternalProxyTokens(repo *db.Repo) internalProxyTokens {
	settings, _ := repo.GetSettingsRaw()
	resolveToken := handlerutil.GetString(settings, "internalProxyResolveToken")
	reportToken := handlerutil.GetString(settings, "internalProxyReportToken")
	updates := map[string]any{}
	if resolveToken == "" {
		resolveToken = generateProxyToken()
		updates["internalProxyResolveToken"] = resolveToken
	}
	if reportToken == "" {
		reportToken = generateProxyToken()
		updates["internalProxyReportToken"] = reportToken
	}
	if len(updates) > 0 {
		_ = repo.UpdateSettingsRaw(updates)
	}
	return internalProxyTokens{resolveToken: resolveToken, reportToken: reportToken}
}

// generateProxyToken returns 32 random bytes hex-encoded, like upstream.
func generateProxyToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// hasValidInternalAuth checks the x-internal-auth header against the
// route's token. An empty expected token never validates.
func hasValidInternalAuth(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	provided := r.Header.Get("x-internal-auth")
	return provided != "" && provided == expected
}

// flattenProviderConnection builds the single-record view of a stored
// connection (data blob spread, columns authoritative), the same shape
// the credentials exporter uses.
func flattenProviderConnection(c *models.ProviderConnection) map[string]any {
	flat := map[string]any{}
	if strings.TrimSpace(c.Data) != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(c.Data), &data); err == nil && data != nil {
			flat = data
		}
	}
	flat["id"] = c.ID
	flat["provider"] = c.Provider
	flat["authType"] = c.AuthType
	flat["isActive"] = c.IsActive == 1
	if c.Name != nil {
		flat["name"] = *c.Name
	}
	if c.Email != nil {
		flat["email"] = *c.Email
	}
	if c.Priority != nil {
		flat["priority"] = *c.Priority
	}
	if c.LastUsedAt != nil {
		flat["lastUsedAt"] = *c.LastUsedAt
	}
	if c.ConsecutiveUseCount != nil {
		flat["consecutiveUseCount"] = *c.ConsecutiveUseCount
	}
	return flat
}

// --- connection status details (src/lib/connectionStatus.ts) ------------

type modelLock struct {
	Key   string `json:"key"`
	Model string `json:"model"`
	Until string `json:"until"`
}

type connectionStatusDetails struct {
	Status             string
	Source             string
	HasActiveModelLock bool
	CooldownUntil      *string
	ActiveModelLocks   []modelLock
}

// futureTimestamp parses a stored timestamp (ISO string or epoch
// milliseconds) and returns its ISO form when it lies in the future.
func futureTimestamp(v any) *string {
	var t time.Time
	switch value := v.(type) {
	case string:
		if value == "" {
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil
		}
		t = parsed
	case float64:
		t = time.UnixMilli(int64(value)).UTC()
	default:
		return nil
	}
	if !t.After(time.Now()) {
		return nil
	}
	iso := t.UTC().Format("2006-01-02T15:04:05.000Z")
	return &iso
}

// connectionActiveModelLocks ports getConnectionActiveModelLocks.
func connectionActiveModelLocks(flat map[string]any) []modelLock {
	locks := []modelLock{}
	keys := make([]string, 0, len(flat))
	for key := range flat {
		if strings.HasPrefix(key, "modelLock_") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		until := futureTimestamp(flat[key])
		if until == nil {
			continue
		}
		model := strings.TrimPrefix(key, "modelLock_")
		if model == "" {
			model = "__all"
		}
		locks = append(locks, modelLock{Key: key, Model: model, Until: *until})
	}
	return locks
}

// connectionCooldownUntil ports getConnectionCooldownUntil.
func connectionCooldownUntil(flat map[string]any) *string {
	candidates := []string{}
	for _, key := range []string{"nextRetryAt", "resetAt"} {
		if ts := futureTimestamp(flat[key]); ts != nil {
			candidates = append(candidates, *ts)
		}
	}
	for _, lock := range connectionActiveModelLocks(flat) {
		candidates = append(candidates, lock.Until)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Strings(candidates)
	return &candidates[0]
}

var usageSupportedProviders = map[string]bool{
	"claude": true, "antigravity": true, "kiro": true,
	"amazon-q": true, "github": true, "codex": true,
}

// getConnectionStatusDetails ports getConnectionStatusDetails.
func getConnectionStatusDetails(flat map[string]any) connectionStatusDetails {
	if flat == nil {
		return connectionStatusDetails{Status: "unknown", Source: "missing", ActiveModelLocks: []modelLock{}}
	}
	if active, ok := flat["isActive"].(bool); ok && !active {
		return connectionStatusDetails{Status: "disabled", Source: "isActive", ActiveModelLocks: []modelLock{}}
	}

	locks := connectionActiveModelLocks(flat)
	cooldown := connectionCooldownUntil(flat)
	details := connectionStatusDetails{
		HasActiveModelLock: len(locks) > 0,
		CooldownUntil:      cooldown,
		ActiveModelLocks:   locks,
	}

	if status, source, found := centralizedConnectionStatus(flat); found {
		details.Status = status
		details.Source = source
		return details
	}
	details.Status = "unknown"
	details.Source = "unknown"
	return details
}

// centralizedConnectionStatus ports getCentralizedStatus.
func centralizedConnectionStatus(flat map[string]any) (string, string, bool) {
	if recStrValue(flat, "reasonCode") == "reauthorization_required" {
		return "disabled", "reasonCode", true
	}

	needsSnapshot := recStrValue(flat, "authType") == "oauth" && usageSupportedProviders[recStrValue(flat, "provider")]
	snapshotValue, snapshotPresent := flat["usageSnapshot"]
	hasSnapshot := snapshotPresent && snapshotValue != nil

	switch recStrValue(flat, "authState") {
	case "invalid", "revoked":
		return "disabled", "authState", true
	case "expired":
		return "blocked", "authState", true
	}

	switch recStrValue(flat, "healthStatus") {
	case "error", "failed", "unhealthy", "down":
		return "blocked", "healthStatus", true
	}

	switch recStrValue(flat, "quotaState") {
	case "exhausted", "blocked":
		return "exhausted", "quotaState", true
	case "ok":
		if recStrValue(flat, "authState") == "ok" && recStrValue(flat, "healthStatus") == "healthy" {
			if needsSnapshot && !hasSnapshot {
				return "unknown", "missingUsageSnapshot", true
			}
			return "eligible", "quotaState", true
		}
	}

	switch recStrValue(flat, "routingStatus") {
	case "eligible":
		if needsSnapshot && !hasSnapshot {
			return "unknown", "missingUsageSnapshot", true
		}
		return "eligible", "routingStatus", true
	case "exhausted", "blocked", "unknown", "disabled":
		return recStrValue(flat, "routingStatus"), "routingStatus", true
	}

	return "", "", false
}

// recStrValue reads a string field from a flattened record.
func recStrValue(record map[string]any, key string) string {
	if s, ok := record[key].(string); ok {
		return s
	}
	return ""
}

// --- POST /api/internal/proxy/resolve -------------------------------------

var allowedProtocolPaths = map[string]map[string]bool{
	"openai": {
		"/v1/chat/completions": true, "/v1/responses": true,
		"/v1/embeddings": true, "/v1/audio/speech": true,
		"/v1/images/generations": true,
	},
	"anthropic": {
		"/v1/messages": true,
	},
}

// HandleInternalProxyResolve serves POST /api/internal/proxy/resolve:
// pick the connection a worker proxy should use for a provider/model/
// protocol triple, with the fallback chain in priority order.
func HandleInternalProxyResolve(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokens := getInternalProxyTokens(repo)
		if !hasValidInternalAuth(r, tokens.resolveToken) {
			writeInternalError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeInternalError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
			writeInternalError(w, http.StatusBadRequest, "invalid_request")
			return
		}

		providerInput := recStrValue(payload, "provider")
		model := recStrValue(payload, "model")
		protocolFamily := recStrValue(payload, "protocolFamily")
		publicPath := recStrValue(payload, "publicPath")
		if providerInput == "" || model == "" || protocolFamily == "" || publicPath == "" {
			writeInternalError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if !validateRouteContract(protocolFamily, publicPath) {
			writeInternalError(w, http.StatusBadRequest, "invalid_route_contract")
			return
		}

		provider := providers.ResolveAlias(providerInput)

		connections, err := repo.GetProviderConnections(provider, true)
		if err != nil {
			writeInternalError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		settings, _ := repo.GetSettingsRaw()

		// Upstream first tries the hot-state eligibility snapshot and
		// falls back to computing status per connection. In this
		// codebase the status fields live on the connection record
		// itself (the data blob), so the two paths coincide: the pool
		// is the active connections whose computed status is eligible.
		pool := []map[string]any{}
		for _, conn := range connections {
			flat := flattenProviderConnection(conn)
			if getConnectionStatusDetails(flat).Status == "eligible" {
				pool = append(pool, flat)
			}
		}
		if len(pool) == 0 {
			handlerutil.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok": false, "error": "no_routable_connection", "owner": "axonrouter",
			})
			return
		}

		strategy := resolveRoutingStrategy(settings, provider)
		chosen, fallbackChain := pickConnections(pool, strategy)

		resolution := map[string]any{
			"provider":         provider,
			"model":            model,
			"protocolFamily":   protocolFamily,
			"publicPath":       publicPath,
			"ttlSeconds":       normalizeResolveTTLSeconds(),
			"chosenConnection": sanitizeResolvedConnection(chosen),
		}
		if len(fallbackChain) > 0 {
			chain := make([]any, len(fallbackChain))
			for i, conn := range fallbackChain {
				chain[i] = sanitizeResolvedConnection(conn)
			}
			resolution["fallbackChain"] = chain
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": true, "owner": "axonrouter", "resolution": resolution,
		})
	}
}

// writeInternalError writes the internal routes' { ok: false, error }
// shape.
func writeInternalError(w http.ResponseWriter, status int, code string) {
	handlerutil.WriteJSON(w, status, map[string]any{"ok": false, "error": code})
}

// validateRouteContract ports validateRouteContract.
func validateRouteContract(protocolFamily, publicPath string) bool {
	allowed, ok := allowedProtocolPaths[protocolFamily]
	return ok && allowed[publicPath]
}

// normalizeResolveTTLSeconds ports normalizeTtlSeconds: the
// GO_PROXY_RESOLVE_CACHE_TTL_SECONDS env override, default 7, clamped
// to [5, 10].
func normalizeResolveTTLSeconds() int {
	raw := os.Getenv("GO_PROXY_RESOLVE_CACHE_TTL_SECONDS")
	value := 7
	if raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			value = parsed
		}
	}
	if value < 5 {
		value = 5
	}
	if value > 10 {
		value = 10
	}
	return value
}

// resolveRoutingStrategy ports the strategy resolution chain:
// per-provider override (routing.providerStrategies, then top-level
// providerStrategies) → routing.strategy → fallbackStrategy →
// "round-robin".
func resolveRoutingStrategy(settings map[string]any, provider string) string {
	routing, _ := settings["routing"].(map[string]any)
	var providerStrategies map[string]any
	if routing != nil {
		providerStrategies, _ = routing["providerStrategies"].(map[string]any)
	}
	if providerStrategies == nil {
		providerStrategies, _ = settings["providerStrategies"].(map[string]any)
	}
	var override map[string]any
	if providerStrategies != nil {
		override, _ = providerStrategies[provider].(map[string]any)
	}
	if s := recStrValue(override, "strategy"); s != "" {
		return s
	}
	if s := recStrValue(override, "fallbackStrategy"); s != "" {
		return s
	}
	if s := recStrValue(routing, "strategy"); s != "" {
		return s
	}
	if s := recStrValue(settings, "fallbackStrategy"); s != "" {
		return s
	}
	return "round-robin"
}

// sanitizeResolvedConnection ports sanitizeConnection.
func sanitizeResolvedConnection(flat map[string]any) map[string]any {
	details := getConnectionStatusDetails(flat)
	out := map[string]any{
		"connectionId":       recStrValue(flat, "id"),
		"provider":           recStrValue(flat, "provider"),
		"status":             details.Status,
		"statusSource":       details.Source,
		"hasActiveModelLock": details.HasActiveModelLock,
	}
	if details.CooldownUntil != nil {
		out["cooldownUntil"] = *details.CooldownUntil
	} else {
		out["cooldownUntil"] = nil
	}
	return out
}

// connectionPriority reads a record's priority the way upstream's
// (priority || 999) does: missing or zero counts as 999.
func connectionPriority(flat map[string]any) float64 {
	if p, ok := flat["priority"].(float64); ok && p != 0 {
		return p
	}
	if p, ok := flat["priority"].(int); ok && p != 0 {
		return float64(p)
	}
	return 999
}

// connectionLastUsedAt reads a record's lastUsedAt timestamp.
func connectionLastUsedAt(flat map[string]any) (time.Time, bool) {
	s, ok := flat["lastUsedAt"].(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// pickConnections ports pickConnections: round-robin keeps the most
// recently used connection sticky for up to 3 consecutive uses, then
// rotates to the least recently used; any other strategy takes the
// pool in priority order.
func pickConnections(pool []map[string]any, strategy string) (map[string]any, []map[string]any) {
	byPriority := sortConnections(pool, func(a, b map[string]any) bool {
		return connectionPriority(a) < connectionPriority(b)
	})
	if len(byPriority) == 0 {
		return nil, []map[string]any{}
	}

	if strategy != "round-robin" {
		return byPriority[0], byPriority[1:]
	}

	byRecencyDesc := sortConnections(pool, func(a, b map[string]any) bool {
		at, aOK := connectionLastUsedAt(a)
		bt, bOK := connectionLastUsedAt(b)
		if !aOK && !bOK {
			return connectionPriority(a) < connectionPriority(b)
		}
		if !aOK {
			return false
		}
		if !bOK {
			return true
		}
		return at.After(bt)
	})
	current := byRecencyDesc[0]
	_, currentUsed := connectionLastUsedAt(current)
	currentCount := 0
	if c, ok := current["consecutiveUseCount"].(float64); ok {
		currentCount = int(c)
	} else if c, ok := current["consecutiveUseCount"].(int); ok {
		currentCount = c
	}

	chosen := current
	if !currentUsed || currentCount >= 3 {
		byRecencyAsc := sortConnections(pool, func(a, b map[string]any) bool {
			at, aOK := connectionLastUsedAt(a)
			bt, bOK := connectionLastUsedAt(b)
			if !aOK && !bOK {
				return connectionPriority(a) < connectionPriority(b)
			}
			if !aOK {
				return true
			}
			if !bOK {
				return false
			}
			return at.Before(bt)
		})
		chosen = byRecencyAsc[0]
	}

	fallbackChain := []map[string]any{}
	for _, conn := range byPriority {
		if recStrValue(conn, "id") != recStrValue(chosen, "id") {
			fallbackChain = append(fallbackChain, conn)
		}
	}
	return chosen, fallbackChain
}

// sortConnections returns a stably sorted copy of the pool.
func sortConnections(pool []map[string]any, less func(a, b map[string]any) bool) []map[string]any {
	out := make([]map[string]any, len(pool))
	copy(out, pool)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}
