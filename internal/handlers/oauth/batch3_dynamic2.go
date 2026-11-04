package oauth

import (
	"bytes"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// The POST exchange action of the generic dispatch route (the GET side is
// batch3_dynamic.go, poll is batch3_dynamic_poll.go, bulk-jwt is
// batch3_bulkjwt.go). Contract: AxonRouter
// src/app/api/oauth/[provider]/[action]/route.js.

// delegateExchangeJSON runs a dedicated exchange handler against a
// synthesized request carrying payload as its JSON body and captures the
// response, following batch3_proxy.go's runExchange pattern.
func (h *OAuthHandler) delegateExchangeJSON(r *http.Request, provider string, payload map[string]any, fn func(http.ResponseWriter, *http.Request)) (int, map[string]any) {
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"http://127.0.0.1/api/oauth/"+provider+"/exchange", bytes.NewReader(raw))
	if err != nil {
		return http.StatusInternalServerError, nil
	}
	req.Header.Set("Content-Type", "application/json")
	return runCaptured(func(w http.ResponseWriter) { fn(w, req) })
}

// dynExchangeSuccess reshapes a captured exchange success body into Axon's
// {success:true, connection:{id, provider, email, displayName}} envelope.
// The display name falls back to the stored row when the handler's own
// success shape carries none.
func (h *OAuthHandler) dynExchangeSuccess(w http.ResponseWriter, provider string, captured map[string]any) {
	id, email := capturedConnection(captured)
	name, _ := captured["name"].(string)
	if conn, ok := captured["connection"].(map[string]any); ok {
		if dn, _ := conn["displayName"].(string); dn != "" {
			name = dn
		}
	}
	if name == "" && id != "" && h.Repo != nil && h.Repo.RawDB() != nil {
		var dbName sql.NullString
		if err := h.Repo.RawDB().QueryRow("SELECT name FROM providerConnections WHERE id = ?", id).Scan(&dbName); err == nil && dbName.Valid {
			name = dbName.String
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id": id, "provider": provider,
			"email": nullOr(email), "displayName": nullOr(name),
		},
	})
}

// dynExchangeFailure maps a captured exchange failure to Axon's flat 500:
// the generic route's outer catch answers {error: err.message}.
func dynExchangeFailure(w http.ResponseWriter, captured map[string]any) {
	msg := capturedErrorMessage(captured)
	if msg == "" {
		msg = "Token exchange failed"
	}
	axonError(w, http.StatusInternalServerError, msg)
}

// dynExchange is Axon's POST exchange action: per-provider special cases
// first (mimo session result, trae/windsurf pasted token, codex raw JWT),
// then the required-fields gate, then the generic token exchange delegated
// to the provider's dedicated handler.
func (h *OAuthHandler) dynExchange(w http.ResponseWriter, r *http.Request, provider string, body map[string]any) {
	code := rawStringField(body, "code")
	redirectURI := rawStringField(body, "redirectUri")
	codeVerifier := rawStringField(body, "codeVerifier")
	state := rawStringField(body, "state")
	systemID := rawStringField(body, "systemId")
	meta, _ := body["meta"].(map[string]any)

	if provider == "xiaomi-mimo" {
		h.dynMimoExchange(w, state)
		return
	}

	if provider == "trae" || provider == "windsurf" {
		token := strings.TrimSpace(code)
		if token == "" {
			axonError(w, http.StatusBadRequest, "Missing token or callback URL")
			return
		}
		payload := map[string]any{"code": token}
		var fn func(http.ResponseWriter, *http.Request)
		if provider == "windsurf" {
			payload["state"] = state
			fn = h.HandleWindsurfExchange
		} else {
			fn = h.HandleTraeExchange
		}
		status, captured := h.delegateExchangeJSON(r, provider, payload, fn)
		if status != http.StatusOK {
			dynExchangeFailure(w, captured)
			return
		}
		h.dynExchangeSuccess(w, provider, captured)
		return
	}

	// A pasted raw JWT is a ChatGPT access token, never an auth code; only
	// codex may store it.
	if code != "" && strings.HasPrefix(code, "eyJ") && strings.Contains(code, ".") {
		if provider != "codex" {
			axonError(w, http.StatusBadRequest,
				fmt.Sprintf("Raw JWT exchange is only supported for codex, not %s", provider))
			return
		}
		h.dynCodexJWTExchange(w, code)
		return
	}

	noPkce := provider == "cline" || provider == "cline-free" || provider == "clinepass" || provider == "kimchi"
	if code == "" || redirectURI == "" || (codeVerifier == "" && !noPkce) {
		axonError(w, http.StatusBadRequest, "Missing required fields")
		return
	}

	var fn func(http.ResponseWriter, *http.Request)
	var payload map[string]any
	switch provider {
	case "claude", "codex", "xai", "gitlab":
		fn = h.HandlePKCEExchange
		payload = map[string]any{
			"provider": provider, "code": code, "codeVerifier": codeVerifier,
			"redirectUri": redirectURI, "state": state,
		}
		// GitLab's self-hosted params ride in meta (baseUrl + client creds).
		for _, k := range []string{"baseUrl", "clientId", "clientSecret"} {
			if v, ok := meta[k]; ok {
				payload[k] = v
			}
		}
	case "antigravity":
		fn = h.HandleAntigravityExchange
		payload = map[string]any{"code": code, "redirectUri": redirectURI}
	case "gemini-cli", "iflow":
		fn = h.HandleAuthCodeExchange
		payload = map[string]any{
			"provider": provider, "code": code,
			"redirectUri": redirectURI, "state": state,
		}
	case "cline", "clinepass", "cline-free":
		fn = h.HandleClineExchange
		stored := provider
		if stored == "cline-free" {
			stored = "cline" // cline-free is cline's free tier in this catalog
		}
		payload = map[string]any{
			"provider": stored, "code": code, "codeVerifier": codeVerifier,
			"redirectUri": redirectURI, "state": state,
		}
	case "zed":
		fn = h.HandleZedExchange
		payload = map[string]any{
			"code": code, "codeVerifier": codeVerifier, "systemId": systemID,
		}
	case "kimchi":
		fn = h.HandleKimchiExchange
		payload = map[string]any{"code": code}
	default:
		if _, known := axonFlow(provider); known {
			axonError(w, http.StatusInternalServerError, "provider.exchangeToken is not a function")
		} else {
			axonError(w, http.StatusInternalServerError, "Unknown provider: "+provider)
		}
		return
	}
	status, captured := h.delegateExchangeJSON(r, provider, payload, fn)
	if status != http.StatusOK {
		dynExchangeFailure(w, captured)
		return
	}
	h.dynExchangeSuccess(w, provider, captured)
}

// dynCodexJWTExchange stores a pasted ChatGPT access token through the same
// payload builder as /api/oauth/codex/import-token.
func (h *OAuthHandler) dynCodexJWTExchange(w http.ResponseWriter, code string) {
	dataMap, email := codexJWTConnectionData(code)
	name := firstNonEmpty(email, "ChatGPT Access Token")
	connID := "cx-" + shortHash(code)
	if err := h.saveAxonConnection(connID, "codex", "access_token", name, email, nil, dataMap); err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id": connID, "provider": "codex",
			"email": nullOr(email), "displayName": name,
		},
	})
}

// dynMimoExchange completes a xiaomi-mimo login from the callback proxy's
// finished session: no token exchange exists — the callback already
// decrypted the sk. The session is cleared and the proxy stopped on both
// success and failure, exactly like Axon.
func (h *OAuthHandler) dynMimoExchange(w http.ResponseWriter, state string) {
	if state == "" {
		axonError(w, http.StatusBadRequest, "Missing state")
		return
	}
	sess, ok := mimoDynProxy.lookup(state)
	if !ok || sess.status != "done" || sess.sk == "" {
		msg := "OAuth session not completed. Please restart the login flow."
		if ok && sess.errMsg != "" {
			msg = sess.errMsg
		}
		axonError(w, http.StatusBadRequest, msg)
		return
	}
	email := ""
	displayName := "Xiaomi MiMo"
	if sess.uid != "" {
		email = sess.uid + "@xiaomi"
		displayName = "Xiaomi " + sess.uid
	}
	baseURL := sess.baseURL
	if baseURL == "" {
		baseURL = "https://api.xiaomimimo.com/v1"
	}
	psd := map[string]any{
		"uid": nullOr(sess.uid), "baseUrl": baseURL, "authMethod": "oauth",
		"mimoPassToken": nil, "mimoUserId": nil, "mimoCUserId": nil,
	}
	if pt := readDesktopPassToken(); pt != nil {
		psd["mimoPassToken"] = pt.passToken
		psd["mimoUserId"] = nullOr(pt.userID)
		psd["mimoCUserId"] = nullOr(pt.cUserID)
	}
	dataMap := map[string]any{
		"apiKey": sess.sk, "accessToken": sess.sk,
		"providerSpecificData": psd,
		"testStatus":           "active",
		"expiresAt":            axonExpiresISO(365 * 24 * time.Hour),
	}
	if email != "" {
		dataMap["email"] = email
	}
	connID := "xiaomi-mimo-" + shortHash(sess.sk)
	err := h.saveAxonConnection(connID, "xiaomi-mimo", "oauth", displayName, email, nil, dataMap)
	mimoDynProxy.clear(state)
	mimoDynProxy.stop()
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id": connID, "provider": "xiaomi-mimo",
			"email": nullOr(email), "displayName": displayName,
		},
	})
}
