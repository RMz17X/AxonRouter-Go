package oauth

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
)

var (
	googleOAuthAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleOAuthTokenURL = "https://oauth2.googleapis.com/token"
)

const defaultAntigravityProjectID = "aicode-consumers"

// defaultAntigravityScopes is agy's own sign-in scope list, in agy's
// order — proven by runtime capture of the preserved v1.5.1 binary's
// printed authorization URL (teardown §7, CERTAIN; fragments 971/972).
// The delta from the router's historical list is exactly `openid` plus
// this ordering; the set is otherwise the same.
var defaultAntigravityScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
	"https://www.googleapis.com/auth/aicode",
	"openid",
}

// getAntigravityOAuthConfig seeds the sign-in client from the one home for
// OAuth client credentials, providers.KnownOAuthConfigs — the literals and
// the ANTIGRAVITY_OAUTH_CLIENT_ID / ANTIGRAVITY_OAUTH_CLIENT_SECRET env
// overrides live there (envOr), not duplicated in this handler.
func getAntigravityOAuthConfig() (clientID, clientSecret, tokenURL string) {
	tokenURL = googleOAuthTokenURL

	if cfg, ok := providers.KnownOAuthConfigs["antigravity"]; ok {
		clientID = cfg.ClientID
		clientSecret = cfg.ClientSecret
		if cfg.TokenURL != "" && googleOAuthTokenURL == "https://oauth2.googleapis.com/token" {
			tokenURL = cfg.TokenURL
		}
	}
	return
}

// HandleAntigravityAuthorize returns the Google OAuth authorization URL for Antigravity.
// GET /api/oauth/antigravity/authorize
//
// The URL mirrors agy's /login exactly (fragment 982): the fixed Google
// callback page as redirect_uri and a PKCE S256 challenge whose verifier
// stays server-side, keyed by state, until the exchange consumes it. A
// client-supplied ?redirect_uri= is not honored for this provider — the
// public Antigravity client registered only the Google page.
func (h *OAuthHandler) HandleAntigravityAuthorize(w http.ResponseWriter, r *http.Request) {
	clientID, _, _ := getAntigravityOAuthConfig()

	scopes := defaultAntigravityScopes
	if customScope := r.URL.Query().Get("scope"); customScope != "" {
		scopes = strings.Split(customScope, " ")
	}

	state := r.URL.Query().Get("state")
	if state == "" {
		state = randomString(32)
	}

	verifier := pkceVerifier()
	antigravityVerifiers.put(state, verifier)

	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {antigravityRedirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(scopes, " ")},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {state},
		"code_challenge":        {sha256Base64(verifier)},
		"code_challenge_method": {"S256"},
	}

	authURL := googleOAuthAuthURL + "?" + params.Encode()

	if r.URL.Query().Get("redirect") == "true" {
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"url":         authURL,
		"redirectUrl": authURL,
		"authUrl":     authURL,
		"state":       state,
		"redirectUri": antigravityRedirectURI,
	})
}

// HandleAntigravityExchange exchanges a Google authorization code for tokens
// and stores the Antigravity connection in the database.
// POST /api/oauth/antigravity/exchange
func (h *OAuthHandler) HandleAntigravityExchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handlerutil.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
		// RedirectURI is accepted so older dashboard builds do not break
		// on an unknown-field basis, but it is never honored: the exchange
		// always repeats the fixed agy redirect (antigravityRedirectURI).
		RedirectURI string `json:"redirectUri"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	code := cleanAuthCode(body.Code)
	if code == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing code parameter")
		return
	}
	state := strings.TrimSpace(body.State)
	if state == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing state parameter")
		return
	}
	// Consume before the token call: the verifier is single-use whether
	// the exchange succeeds or fails, so a replayed state can never ride
	// a second attempt.
	verifier, ok := antigravityVerifiers.consume(state)
	if !ok {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "unknown or expired authorization state")
		return
	}

	clientID, clientSecret, tokenURL := getAntigravityOAuthConfig()

	// PKCE stays (fragment 982), but the exchange also sends the client's
	// embedded secret: the live Google endpoint rejects the no-secret form
	// with 400 (proven 2026-11-07 against v1.9.23; the same falsification
	// hit the JS router and was fixed there in v0.1.33). The secret is the
	// public one shipped in agy itself, carried by the provider config.
	// The refresh path keeps its own configs and is not affected.
	form := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {antigravityRedirectURI},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}

	tokenReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to create token request")
		return
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No custom UA: the handler runs server-side against Google token
	// endpoints, and any router brand here is pure self-identification.
	// Go's default UA ("Go-http-client/2.0") is unbranded and sufficient.

	client := &http.Client{Timeout: 15 * time.Second}
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		log.Error("oauth", "antigravity token exchange failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "antigravity token exchange failed")
		return
	}
	defer tokenResp.Body.Close()

	respBody, err := io.ReadAll(tokenResp.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to read token response")
		return
	}

	if tokenResp.StatusCode != http.StatusOK {
		// Error bodies carry only Google's error/error_description — no
		// tokens — so log them: a bare status made the 2026-11-07 live
		// failure undiagnosable. Success bodies are never logged.
		log.Warn("oauth", "antigravity token exchange non-200", "status", tokenResp.StatusCode, "body", string(respBody))
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("token exchange returned status %d", tokenResp.StatusCode))
		return
	}

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		IDToken      string `json:"id_token"`
	}

	if err := json.Unmarshal(respBody, &tokenData); err != nil {
		log.Error("oauth", "antigravity decode token response failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "failed to decode token response")
		return
	}

	if tokenData.AccessToken == "" {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "missing access_token in token response")
		return
	}

	email := ""
	if tokenData.IDToken != "" {
		email = extractEmailFromJWT(tokenData.IDToken)
	}

	connID := ""
	if email != "" {
		connID = "ag-" + shortHash(email)
	} else if tokenData.RefreshToken != "" {
		connID = "ag-" + shortHash(tokenData.RefreshToken)
	} else {
		connID = "ag-" + randomString(12)
	}

	connName := connectionDisplayName("antigravity", "", email, "Antigravity")

	now := currentTimestamp()
	dataMap := map[string]any{
		"apiKey":      tokenData.AccessToken,
		"accessToken": tokenData.AccessToken,
		"tokenType":   tokenData.TokenType,
		"projectId":   defaultAntigravityProjectID,
	}
	if tokenData.RefreshToken != "" {
		dataMap["refreshToken"] = tokenData.RefreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	if tokenData.IDToken != "" {
		dataMap["idToken"] = tokenData.IDToken
	}
	if tokenData.Scope != "" {
		dataMap["scope"] = tokenData.Scope
	}
	if tokenData.ExpiresIn > 0 {
		dataMap["expiresAt"] = time.Now().Add(time.Duration(tokenData.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	dataBytes, err := json.Marshal(dataMap)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal connection data")
		return
	}

	if h.Repo != nil && h.Repo.RawDB() != nil {
		var existingDataStr string
		err := h.Repo.QueryRow("SELECT data FROM providerConnections WHERE id = ?", connID).Scan(&existingDataStr)
		if err == nil && existingDataStr != "" {
			var existingData map[string]any
			if err := json.Unmarshal([]byte(existingDataStr), &existingData); err == nil {
				if tokenData.RefreshToken == "" {
					if rt, ok := existingData["refreshToken"].(string); ok && rt != "" {
						dataMap["refreshToken"] = rt
					}
				}
				if pid, ok := existingData["projectId"].(string); ok && strings.TrimSpace(pid) != "" {
					dataMap["projectId"] = strings.TrimSpace(pid)
				} else if psd, ok := existingData["providerSpecificData"].(map[string]any); ok {
					if pid, ok := psd["projectId"].(string); ok && strings.TrimSpace(pid) != "" {
						dataMap["projectId"] = strings.TrimSpace(pid)
					}
				}
				// Fragment 972: the identity pin survives re-exchange. An
				// existing pin is carried verbatim — no path may move an
				// established identity — while a connection that predates
				// pinning gets its pin from this OAuth completion, the
				// same moment a fresh connection gets its own.
				psd := map[string]any{}
				if existingPSD, ok := existingData["providerSpecificData"].(map[string]any); ok && providers.AntigravityIdentityPinned(existingPSD) {
					psd[providers.AntigravityIdentityPSDKey] = existingPSD[providers.AntigravityIdentityPSDKey]
				}
				if _, ok := psd[providers.AntigravityIdentityPSDKey]; !ok {
					psd[providers.AntigravityIdentityPSDKey] = providers.AntigravityIdentitySnapshot()
				}
				dataMap["providerSpecificData"] = psd
			}
			dataBytes, _ = json.Marshal(dataMap)
			_, err = h.Repo.Exec(
				"UPDATE providerConnections SET name = ?, data = ?, updatedAt = ? WHERE id = ?",
				connName, string(dataBytes), now, connID,
			)
		} else {
			priority := 1
			if h.Repo != nil {
				priority = h.Repo.ResolveDefaultPriority("antigravity")
			}
			// Fragment 972: the identity is pinned once, here at OAuth
			// completion — the snapshot of the current runtime derivation
			// is stored with the connection and never recomputed.
			dataMap["providerSpecificData"] = map[string]any{
				providers.AntigravityIdentityPSDKey: providers.AntigravityIdentitySnapshot(),
			}
			dataBytes, _ = json.Marshal(dataMap)
			_, err = h.Repo.Exec(
				"INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES (?, 'antigravity', 'oauth', ?, ?, 1, ?, ?, ?)",
				connID, connName, priority, string(dataBytes), now, now,
			)
		}
		if err != nil {
			log.Error("oauth", "save antigravity connection failed", "conn", connID, "error", err)
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to save connection")
			return
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":          connID,
			"provider":    "antigravity",
			"email":       email,
			"displayName": connName,
		},
	})
}

func cleanAuthCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	// If the user pasted a full URL or query string like "https://...?code=..." or "code=..."
	if strings.Contains(code, "code=") {
		if u, err := url.Parse(code); err == nil && u.Query().Get("code") != "" {
			code = u.Query().Get("code")
		} else {
			parts := strings.Split(code, "code=")
			if len(parts) > 1 {
				sub := strings.Split(parts[1], "&")
				code = sub[0]
			}
		}
	}
	// Decode percent encoding until fully unescaped so that "4%2F..." or "4%252F..." becomes "4/..."
	for strings.Contains(code, "%") {
		unescaped, err := url.QueryUnescape(code)
		if err != nil || unescaped == code {
			break
		}
		code = unescaped
	}
	return strings.TrimSpace(code)
}
