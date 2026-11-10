package dashboard

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/handlerutil"
)

// Chroma sync endpoint for kimi-web (docs/KIMI_WEB_PROVIDER_SPEC.md
// sections 1 and 9): Chroma extracts the refresh_token from the Local
// Storage of a browser profile the user signed in to kimi.moonshot.cn
// with, and pushes it here. The connection lands in the same store,
// with the same shape, as one created from the dashboard — this
// handler only translates the spec's payload into the generic create,
// so there is exactly one connection write path.

// kimiWebSyncRequest is the body Chroma posts.
type kimiWebSyncRequest struct {
	Name           string `json:"name"`
	RefreshToken   string `json:"refreshToken"`
	AccessToken    string `json:"accessToken"`
	TokenExpiresAt int64  `json:"tokenExpiresAt"`
	UserAgent      string `json:"userAgent"`
	ProxyURL       string `json:"proxyUrl"`
	ProxyPoolID    string `json:"proxyPoolId"`
	// Replace refreshes an existing same-name connection in place
	// instead of adding a second row for the same account — the shape
	// a periodic re-sync wants when a refresh token rotates.
	Replace bool `json:"replace"`
}

// HandleKimiWebSyncConnection handles
// POST /api/providers/kimi-web/connections.
func (h *DashboardHandler) HandleKimiWebSyncConnection(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req kimiWebSyncRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing refreshToken — nothing to sync")
		return
	}

	psd := map[string]any{"refreshToken": refreshToken}
	if v := strings.TrimSpace(req.AccessToken); v != "" {
		psd["accessToken"] = v
	}
	if req.TokenExpiresAt > 0 {
		psd["tokenExpiresAt"] = req.TokenExpiresAt
	}
	if v := strings.TrimSpace(req.UserAgent); v != "" {
		psd["userAgent"] = v
	}
	if v := strings.TrimSpace(req.ProxyURL); v != "" {
		psd["proxyUrl"] = v
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Kimi Web (Chroma)"
	}

	createBody, err := json.Marshal(map[string]any{
		"provider":             "kimi-web",
		"authType":             "token",
		"name":                 name,
		"apiKey":               refreshToken,
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
