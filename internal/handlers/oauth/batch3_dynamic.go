package oauth

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/handlerutil"
)

// The generic AxonRouter OAuth dispatch route:
//   GET  /api/oauth/{provider}/{action}  — authorize, start-proxy,
//          poll-status, stop-proxy, ide-status, device-code
//   POST /api/oauth/{provider}/{action}  — register-session, exchange, poll,
//          manual-code, bulk-jwt
// Contract: AxonRouter src/app/api/oauth/[provider]/[action]/route.js
// (route.js wins over the older route.ts subset). Actions delegate to this
// package's dedicated per-provider handlers and proxy registries wherever
// they exist, reshaping their output to Axon's envelopes.

// axonFlowTypes is Axon's PROVIDERS registry (src/lib/oauth/providers/
// index.js): provider → flowType. kimi-coding resolves to kimi there.
var axonFlowTypes = map[string]string{
	"claude": "authorization_code_pkce", "codex": "authorization_code_pkce",
	"xai": "authorization_code_pkce", "gitlab": "authorization_code_pkce",
	"antigravity": "authorization_code", "cline": "authorization_code",
	"cline-free": "authorization_code", "clinepass": "authorization_code",
	"gemini-cli": "authorization_code", "iflow": "authorization_code",
	"trae": "authorization_code", "windsurf": "authorization_code",
	"zed":   "authorization_code",
	"qoder": "device_code", "qoder-cn": "device_code", "github": "device_code",
	"kiro": "device_code", "kimi": "device_code", "kilocode": "device_code",
	"codebuddy-cn": "device_code", "codebuddy-intl": "device_code",
	"workbuddy": "device_code", "grok-cli": "device_code",
	"freebuff": "device_code", "muse": "device_code", "nous": "device_code",
	"minimax-code": "device_code", "minimax-code-global": "device_code",
	"cursor": "import_token", "kimchi": "browser_token",
}

// axonFlow resolves a provider's Axon flowType (kimi-coding → kimi).
func axonFlow(provider string) (string, bool) {
	if provider == "kimi-coding" {
		provider = "kimi"
	}
	flow, ok := axonFlowTypes[provider]
	return flow, ok
}

// rawStringField reads a body field as a string without trimming ("" when
// absent or not a string), matching JS destructuring of a JSON body.
func rawStringField(body map[string]any, key string) string {
	if body == nil {
		return ""
	}
	s, _ := body[key].(string)
	return s
}

// HandleOAuthDynamicGet serves GET /api/oauth/{provider}/{action}.
func (h *OAuthHandler) HandleOAuthDynamicGet(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	switch action := r.PathValue("action"); action {
	case "authorize":
		h.dynAuthorize(w, r, provider)
	case "start-proxy":
		h.dynStartProxy(w, r, provider)
	case "poll-status":
		h.dynPollStatus(w, r, provider)
	case "stop-proxy":
		dynStopProxy(w, provider)
	case "ide-status":
		dynIDEStatus(w, provider)
	case "device-code":
		h.dynDeviceCode(w, r, provider)
	default:
		axonError(w, http.StatusBadRequest, "Unknown action")
	}
}

// HandleOAuthDynamicPost serves POST /api/oauth/{provider}/{action}. Axon
// parses the JSON body before dispatching any action.
func (h *OAuthHandler) HandleOAuthDynamicPost(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	action := r.PathValue("action")
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var bodyAny any
	if err := json.Unmarshal(raw, &bodyAny); err != nil {
		axonError(w, http.StatusBadRequest, "Invalid or empty request body")
		return
	}
	body, _ := bodyAny.(map[string]any)
	if body == nil {
		body = map[string]any{}
	}
	switch action {
	case "register-session":
		dynRegisterSession(w, r, provider, body)
	case "exchange":
		h.dynExchange(w, r, provider, body)
	case "poll":
		h.dynPoll(w, r, provider, body)
	case "manual-code":
		h.dynManualCode(w, provider, body)
	case "bulk-jwt":
		h.dynBulkJWT(w, provider, bodyAny)
	default:
		axonError(w, http.StatusBadRequest, "Unknown action")
	}
}

// ---- authorize ----

func (h *OAuthHandler) dynAuthorize(w http.ResponseWriter, r *http.Request, provider string) {
	q := r.URL.Query()
	if provider == "xiaomi-mimo" {
		h.dynMimoAuthorize(w, q)
		return
	}
	flow, known := axonFlow(provider)
	if !known {
		axonError(w, http.StatusInternalServerError, "Unknown provider: "+provider)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "http://localhost:8080/callback"
	}
	if flow == "device_code" {
		// Device flows have no auth URL upfront, but generateAuthData still
		// returns the PKCE pair + state.
		verifier := pkceVerifier()
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"authUrl": nil, "state": randomString(32),
			"codeVerifier": verifier, "codeChallenge": sha256Base64(verifier),
			"redirectUri": redirectURI, "flowType": "device_code",
			"callbackPath": "/callback",
		})
		return
	}
	if provider == "cursor" {
		// Cursor's Axon provider module defines no buildAuthUrl (import-token
		// flow only), so generateAuthData throws exactly this.
		axonError(w, http.StatusInternalServerError, "provider.buildAuthUrl is not a function")
		return
	}
	status, captured := h.delegateAuthorize(provider, r)
	if status != http.StatusOK {
		msg := capturedErrorMessage(captured)
		if msg == "" {
			msg = "authorize failed"
		}
		axonError(w, http.StatusInternalServerError, msg)
		return
	}
	authURL, _ := captured["authUrl"].(string)
	if authURL == "" {
		authURL, _ = captured["url"].(string)
	}
	state, _ := captured["state"].(string)
	if state == "" {
		state, _ = captured["loginTraceId"].(string)
	}
	if state == "" {
		state = randomString(32)
	}
	verifier, _ := captured["codeVerifier"].(string)
	if verifier == "" {
		verifier = pkceVerifier()
	}
	challenge, _ := captured["codeChallenge"].(string)
	if challenge == "" {
		challenge = sha256Base64(verifier)
	}
	redirectOut, _ := captured["redirectUri"].(string)
	if redirectOut == "" {
		redirectOut = redirectURI
	}
	out := map[string]any{
		"authUrl": authURL, "state": state,
		"codeVerifier": verifier, "codeChallenge": challenge,
		"redirectUri": redirectOut, "flowType": flow,
		"callbackPath": dynCallbackPath(provider),
	}
	switch provider {
	case "codex":
		out["fixedPort"] = codexProxyPort
	case "xai":
		out["fixedPort"] = xaiProxyPort
	case "zed":
		if sys, _ := captured["systemId"].(string); sys != "" {
			out["systemId"] = sys
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, out)
}

// dynCallbackPath is Axon's per-provider callbackPath (default "/callback").
func dynCallbackPath(provider string) string {
	switch provider {
	case "codex":
		return "/auth/callback"
	case "zed":
		return "/"
	case "windsurf":
		return "/windsurf-auth-callback"
	}
	return "/callback"
}

// delegateAuthorize runs the provider's dedicated authorize handler against
// a cloned request (provider threaded through the query, as those handlers
// expect) and captures its JSON.
func (h *OAuthHandler) delegateAuthorize(provider string, r *http.Request) (int, map[string]any) {
	var fn func(http.ResponseWriter, *http.Request)
	nr := r.Clone(r.Context())
	q := nr.URL.Query()
	switch provider {
	case "claude", "codex", "xai", "gitlab":
		q.Set("provider", provider)
		fn = h.HandlePKCEAuthorize
	case "antigravity":
		fn = h.HandleAntigravityAuthorize
	case "gemini-cli", "iflow":
		q.Set("provider", provider)
		fn = h.HandleAuthCodeAuthorize
	case "cline", "clinepass", "cline-free":
		// cline-free is Cline's free tier in this codebase's catalog (its
		// models live under the cline provider), so the Cline flow runs
		// under the canonical cline id.
		if provider == "cline-free" {
			q.Set("provider", "cline")
		} else {
			q.Set("provider", provider)
		}
		fn = h.HandleClineAuthorize
	case "trae":
		fn = h.HandleTraeAuthorize
	case "windsurf":
		fn = h.HandleWindsurfAuthorize
	case "zed":
		// Axon derives native_app_port from the callback URL's port.
		if raw := q.Get("redirect_uri"); raw != "" {
			if u, err := url.Parse(raw); err == nil && u.Port() != "" {
				q.Set("port", u.Port())
			}
		}
		fn = h.HandleZedAuthorize
	case "kimchi":
		fn = h.HandleKimchiAuthorize
	default:
		return http.StatusInternalServerError, nil
	}
	nr.URL.RawQuery = q.Encode()
	return runCaptured(func(w http.ResponseWriter) { fn(w, nr) })
}

// dynMimoAuthorize is Axon's xiaomi-mimo authorize branch: mint the ECDH
// keypair, start the callback proxy, register the session, return the
// platform authorize URL bound to the proxy's callback URL.
func (h *OAuthHandler) dynMimoAuthorize(w http.ResponseWriter, q url.Values) {
	state := q.Get("state")
	if state == "" {
		state = randomUUID()
	}
	ok, port, callbackURL, reason := mimoDynProxy.start()
	if !ok {
		axonError(w, http.StatusInternalServerError, "Failed to start callback server: "+reason)
		return
	}
	verifier, authURL := mimoAuthorizeURL(callbackURL)
	mimoDynProxy.register(state, verifier)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"state": state, "authorizeUrl": authURL,
		"redirectUri": callbackURL, "port": port,
	})
}

// mimoAuthorizeURL mints the X25519 keypair + platform authorize URL for a
// given callback URL, returning the session verifier (the encoding
// mimoDecryptCallback consumes). Mirrors HandleMimoAuthorize's keygen with
// the proxy callback substituted for the dashboard callback.
func mimoAuthorizeURL(redirectURI string) (verifier, authURL string) {
	keyName := ""
	if hn, err := os.Hostname(); err == nil {
		sum := sha256.Sum256([]byte(runtime.GOOS + "-" + hn))
		keyName = fmt.Sprintf("axonrouter-go-xmd-%x", sum[:4])
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", ""
	}
	pubDer, err := x509.MarshalPKIXPublicKey(priv.PublicKey())
	if err != nil {
		return "", ""
	}
	privDer, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", ""
	}
	verifier = "mimo-x25519:" + base64.RawURLEncoding.EncodeToString(privDer)
	p := url.Values{
		"pk":           {base64.StdEncoding.EncodeToString(pubDer)},
		"redirect_uri": {redirectURI},
		"kn":           {mimoKn},
	}
	if keyName != "" {
		p.Set("key_name", keyName)
	}
	return verifier, mimoPlatformURL + "/authorize?" + p.Encode()
}
