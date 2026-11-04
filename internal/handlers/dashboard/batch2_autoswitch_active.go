package dashboard

import (
	"encoding/base64"
	json "encoding/json/v2"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// ---------------------------------------------------------------------------
// Active-account resolution
// ---------------------------------------------------------------------------

// autoSwitchDisplayName is the families' label chain:
// name || email || displayName || "" (rendered as null when empty).
func autoSwitchDisplayName(c *models.ProviderConnection) string {
	if v := displayNameOr(c, ""); v != "" {
		return v
	}
	return dataStr(connDataMap(c), "displayName")
}

func findConnectionByToken(conns []*models.ProviderConnection, token string) *models.ProviderConnection {
	for _, c := range conns {
		accessToken, apiKey, _ := connectionTokenPair(c)
		if accessToken == token || apiKey == token {
			return c
		}
	}
	return nil
}

func findConnectionByID(conns []*models.ProviderConnection, id string) *models.ProviderConnection {
	for _, c := range conns {
		if c.ID == id {
			return c
		}
	}
	return nil
}

var googleUserinfoClient = &http.Client{Timeout: 2 * time.Second}

func fetchGoogleUserEmail(token string) string {
	req, err := http.NewRequest(http.MethodGet, "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := googleUserinfoClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ""
	}
	var payload struct {
		Email string `json:"email"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil || json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return payload.Email
}

func jwtEmailClaim(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	segment := parts[1]
	if decoded, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		var payload struct {
			Email string `json:"email"`
		}
		if json.Unmarshal(decoded, &payload) == nil {
			return payload.Email
		}
	}
	return ""
}

func externalEmail(provider, token string) string {
	if provider == codexProviderID {
		return jwtEmailClaim(token)
	}
	return fetchGoogleUserEmail(token)
}

// quotaRemainingPercent mirrors parseUsageSnapshot's per-window remaining
// percentage: an explicit remainingPercentage wins, else derive from
// used/total, else the window counts as fully remaining (100).
func quotaRemainingPercent(window map[string]any) float64 {
	if pct, ok := window["remainingPercentage"].(float64); ok && pct >= 0 && pct <= 100 {
		return pct
	}
	total, hasTotal := window["total"].(float64)
	used, hasUsed := window["used"].(float64)
	if hasTotal && hasUsed && total > 0 {
		return math.Round((total - used) / total * 100)
	}
	return 100
}

// codexRemainingPercent is the minimum remaining percentage across the
// connection's stored quota windows, or nil when it is 100 / there are no
// windows (the route renders that as null).
func codexRemainingPercent(c *models.ProviderConnection) any {
	quotas := dataMap(connDataMap(c), "quotas")
	if len(quotas) == 0 {
		return nil
	}
	min := 100.0
	for _, raw := range quotas {
		window, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if pct := quotaRemainingPercent(window); pct < min {
			min = pct
		}
	}
	if min >= 100 {
		return nil
	}
	return min
}

// resolveAutoSwitchActive implements the getActive*Account flow shared by
// both families. The returned connection is the resolved one (nil for the
// external / no-active outcomes) so family routes can decorate the
// payload (projectId for antigravity, planType/remainingPercent for
// codex).
func (h *DashboardHandler) resolveAutoSwitchActive(provider string) (map[string]any, *models.ProviderConnection, error) {
	nulls := map[string]any{"connectionId": nil, "connectionName": nil, "email": nil}
	external := func(token string) map[string]any {
		email := externalEmail(provider, token)
		name := "Managed Outside AxonRouter"
		var emailValue any
		if email != "" {
			name = email + " (External)"
			emailValue = email
		}
		return map[string]any{"connectionId": "external", "connectionName": name, "email": emailValue}
	}

	token, hasToken := readAutoSwitchFileToken(provider)
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil {
		return nil, nil, err
	}
	var matched *models.ProviderConnection
	if hasToken {
		matched = findConnectionByToken(conns, token)
	}
	effectiveID := ""
	if hasToken {
		if matched != nil {
			effectiveID = matched.ID
		}
	} else {
		effectiveID = cfgActiveID(h.autoSwitchConfig(provider))
	}
	if effectiveID == "" {
		if hasToken {
			return external(token), nil, nil
		}
		return nulls, nil, nil
	}
	conn := matched
	if conn == nil || conn.ID != effectiveID {
		conn = findConnectionByID(conns, effectiveID)
	}
	if conn == nil {
		if hasToken {
			return external(token), nil, nil
		}
		return nulls, nil, nil
	}
	payload := map[string]any{"connectionId": conn.ID, "connectionName": nil, "email": nil}
	if name := autoSwitchDisplayName(conn); name != "" {
		payload["connectionName"] = name
	}
	if conn.Email != nil && *conn.Email != "" {
		payload["email"] = *conn.Email
	}
	return payload, conn, nil
}

// ---------------------------------------------------------------------------
// Rotation
// ---------------------------------------------------------------------------

var autoSwitchUnhealthyStatuses = map[string]bool{
	"quota_exhausted": true, // AxonRouter: "exhausted"
	"unavailable":     true,
	"error":           true,
	"expired":         true, // AxonRouter: "blocked"
	"disabled":        true,
	"rate_limited":    true,
}

// findNextHealthyAutoSwitch ports findNextHealthyConnectionId: round-
// robin from the active connection, first healthy sibling wins.
func (h *DashboardHandler) findNextHealthyAutoSwitch(provider, activeID string) (*models.ProviderConnection, error) {
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil {
		return nil, err
	}
	if len(conns) <= 1 {
		return nil, nil
	}
	healthy := func(c *models.ProviderConnection) bool {
		if c.ID == activeID {
			return false
		}
		status, _ := providers.ConnectionStatus(c.IsActive, c.Data)
		return !autoSwitchUnhealthyStatuses[status]
	}
	start := -1
	for i, c := range conns {
		if c.ID == activeID {
			start = i
			break
		}
	}
	for i := 1; i <= len(conns); i++ {
		candidate := conns[(start+i)%len(conns)]
		if healthy(candidate) {
			return candidate, nil
		}
	}
	return nil, nil
}

// codexBelowThreshold reports whether any quota window of the active
// connection is at or below the configured threshold.
func codexBelowThreshold(cfg map[string]any, c *models.ProviderConnection) bool {
	quotas := dataMap(connDataMap(c), "quotas")
	if len(quotas) == 0 {
		return false
	}
	threshold := float64(normalizeCodexThreshold(cfg["thresholdPercent"]))
	for _, raw := range quotas {
		window, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if quotaRemainingPercent(window) <= threshold {
			return true
		}
	}
	return false
}

// checkAndRotateAutoSwitch ports checkAndRotate*: returns the new active
// connection id, or "" when no rotation happened.
func (h *DashboardHandler) checkAndRotateAutoSwitch(provider string) (string, error) {
	cfg := h.autoSwitchConfig(provider)
	if cfg["enabled"] != true {
		return "", nil
	}
	activeID := cfgActiveID(cfg)
	if activeID == "" {
		next, err := h.findNextHealthyAutoSwitch(provider, "")
		if err != nil {
			return "", err
		}
		if next == nil {
			return "", nil
		}
		// AxonRouter ignores the token-write outcome on this branch.
		_ = writeAutoSwitchTokenFile(provider, next)
		id := next.ID
		h.persistAutoSwitchActive(provider, &id, rotationKeep, nil, nil)
		return id, nil
	}

	if provider == codexProviderID {
		conns, err := h.Repo.GetProviderConnections(provider, true)
		if err != nil {
			return "", err
		}
		active := findConnectionByID(conns, activeID)
		if active == nil || !codexBelowThreshold(cfg, active) {
			return "", nil
		}
	}

	next, err := h.findNextHealthyAutoSwitch(provider, activeID)
	if err != nil {
		return "", err
	}
	if next == nil {
		return "", nil
	}
	if !writeAutoSwitchTokenFile(provider, next) {
		return "", nil
	}
	id := next.ID
	from := activeID
	h.persistAutoSwitchActive(provider, &id, rotationSet, &from, &id)
	return id, nil
}

// setAutoSwitchActive ports setActive*Account: (false, nil) means the
// route answers 404.
func (h *DashboardHandler) setAutoSwitchActive(provider string, connectionID *string) (bool, error) {
	if connectionID == nil {
		h.persistAutoSwitchActive(provider, nil, rotationClear, nil, nil)
		return true, nil
	}
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil {
		return false, err
	}
	conn := findConnectionByID(conns, *connectionID)
	if conn == nil {
		return false, nil
	}
	if !writeAutoSwitchTokenFile(provider, conn) {
		return false, nil
	}
	h.persistAutoSwitchActive(provider, connectionID, rotationClear, nil, nil)
	return true, nil
}
