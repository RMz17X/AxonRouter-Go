package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/handlerutil"
)

// Chroma sync endpoint for claude-web (docs/CLAUDE_WEB_PROVIDER_SPEC.md
// sections 1 and 9): Chroma extracts the sessionKey from a browser
// profile the user signed in to claude.ai with, and pushes it here.
// The connection lands in the same store, with the same shape, as one
// created from the dashboard — this handler only translates the spec's
// payload into the generic create, so there is exactly one connection
// write path.

// claudeWebSyncRequest is the body Chroma posts.
type claudeWebSyncRequest struct {
	Name             string `json:"name"`
	Cookie           string `json:"cookie"`
	SessionKey       string `json:"sessionKey"`
	OrganizationUUID string `json:"organizationUuid"`
	UserAgent        string `json:"userAgent"`
	ProxyURL         string `json:"proxyUrl"`
	ProxyPoolID      string `json:"proxyPoolId"`
	// Replace refreshes an existing same-name connection in place
	// instead of adding a second row for the same account — the shape
	// a periodic re-sync wants when a sessionKey rotates.
	Replace bool `json:"replace"`
}

// HandleClaudeWebSyncConnection handles
// POST /api/providers/claude-web/connections.
func (h *DashboardHandler) HandleClaudeWebSyncConnection(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req claudeWebSyncRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	// The executor reads the cookie from the connection's apiKey: a
	// full cookie header when Chroma sends one, else the bare key.
	apiKey := strings.TrimSpace(req.Cookie)
	sessionKey := strings.TrimSpace(req.SessionKey)
	if apiKey == "" {
		apiKey = sessionKey
	}
	if apiKey == "" && sessionKey == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing sessionKey (or cookie) — nothing to sync")
		return
	}

	psd := map[string]any{}
	if sessionKey != "" {
		psd["sessionKey"] = sessionKey
	}
	if v := strings.TrimSpace(req.OrganizationUUID); v != "" {
		psd["organizationUuid"] = v
	}
	if v := strings.TrimSpace(req.UserAgent); v != "" {
		psd["userAgent"] = v
	}
	if v := strings.TrimSpace(req.ProxyURL); v != "" {
		psd["proxyUrl"] = v
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Claude Web (Chroma)"
	}

	createBody, err := json.Marshal(map[string]any{
		"provider":             "claude-web",
		"authType":             "cookie",
		"name":                 name,
		"apiKey":               apiKey,
		"providerSpecificData": psd,
		"proxyPoolId":          req.ProxyPoolID,
		"replace":              req.Replace,
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to build connection payload")
		return
	}

	clone := r.Clone(r.Context())
	clone.Body = io.NopCloser(bytes.NewReader(createBody))
	clone.ContentLength = int64(len(createBody))
	clone.Header.Set("Content-Type", "application/json")
	h.HandleCreateConnection(w, clone)
}
