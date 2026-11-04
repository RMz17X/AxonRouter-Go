package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/proxy/executor"
)

// The device-code GET action plus the two session POST actions of the
// generic dispatch route (split from batch3_dynamic.go): device-code,
// register-session, manual-code.

// ---- device-code ----

func (h *OAuthHandler) dynDeviceCode(w http.ResponseWriter, r *http.Request, provider string) {
	flow, known := axonFlow(provider)
	if !known {
		axonError(w, http.StatusInternalServerError, "Unknown provider: "+provider)
		return
	}
	if flow != "device_code" {
		axonError(w, http.StatusBadRequest, "Provider does not support device code flow")
		return
	}
	verifier := pkceVerifier()
	if provider == "freebuff" {
		out, err := h.freebuffDeviceStart(r)
		if err != nil {
			axonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out["codeVerifier"] = verifier
		handlerutil.WriteJSON(w, http.StatusOK, out)
		return
	}
	q := r.URL.Query()
	var out map[string]any
	var err error
	if provider == "workbuddy" {
		// WorkBuddy mirrors the CodeBuddy endpoints on its own host; the
		// shared device switch does not carry it, so it starts here.
		out, err = workbuddyStart(r.Context())
	} else {
		out, err = deviceStart(r.Context(), deviceCanonical(provider),
			q.Get("region"), q.Get("start_url"), q.Get("auth_method"))
	}
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make(map[string]any, len(out)+2)
	for k, v := range out {
		resp[k] = v
	}
	session, _ := out["session"].(map[string]any)
	delete(resp, "session")
	switch deviceCanonical(provider) {
	case "qoder", "qoder-cn":
		if v := strVal(session, "verifier"); v != "" {
			resp["codeVerifier"] = v
		}
		resp["_qoderNonce"] = strVal(session, "nonce")
		resp["_qoderMachineId"] = strVal(session, "machineId")
	case "kiro":
		resp["_clientId"] = strVal(session, "clientId")
		resp["_clientSecret"] = strVal(session, "clientSecret")
		resp["_region"] = strVal(session, "region")
		resp["_authMethod"] = strVal(session, "authMethod")
		resp["_startUrl"] = strVal(session, "startUrl")
	}
	if s, _ := resp["codeVerifier"].(string); s == "" {
		resp["codeVerifier"] = verifier
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// freebuffDeviceCodePayload is Axon's device_code value for freebuff: the
// fingerprint triple JSON-encoded (field order as in the JS literal).
type freebuffDeviceCodePayload struct {
	FingerprintID   string `json:"fingerprintId"`
	FingerprintHash string `json:"fingerprintHash"`
	ExpiresAt       int64  `json:"expiresAt"`
}

// freebuffDeviceStart mirrors Axon's freebuff requestDeviceCode: register a
// fingerprint with the CLI code endpoint and package the result in the
// OAuthModal device shape.
func (h *OAuthHandler) freebuffDeviceStart(r *http.Request) (map[string]any, error) {
	fingerprintID := randomUUID()
	payload, _ := json.Marshal(map[string]string{"fingerprintId": fingerprintID})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		freebuffAuthBaseURL+"/api/auth/cli/code", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codebuff-cli/0.0.138")
	resp, err := executor.DoFreebuffHTTP(r.Context(), nil, req)
	if err != nil {
		return nil, fmt.Errorf("Freebuff login code request failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Freebuff login code request failed: %s", truncateForError(raw))
	}
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	loginURL := jsonStringField(data, "loginUrl")
	authCode := ""
	if i := strings.Index(loginURL, "auth_code="); i >= 0 {
		authCode = loginURL[i+len("auth_code="):]
		if j := strings.Index(authCode, "&"); j >= 0 {
			authCode = authCode[:j]
		}
	}
	expiresAt := int64(0)
	if v, ok := data["expiresAt"].(float64); ok {
		expiresAt = int64(v)
	}
	if v := jsonStringField(data, "fingerprintId"); v != "" {
		fingerprintID = v
	}
	fingerprintHash := jsonStringField(data, "fingerprintHash")
	const timeoutSec = 300
	serverMs := timeoutSec * 1000
	if expiresAt > 0 {
		serverMs = int(expiresAt - time.Now().UnixMilli())
	}
	expiresIn := serverMs / 1000
	if expiresIn > timeoutSec {
		expiresIn = timeoutSec
	}
	if expiresIn < 60 {
		expiresIn = 60
	}
	freebuffPendingSessions.Store(fingerprintID, expiresAt)
	codeJSON, _ := json.Marshal(freebuffDeviceCodePayload{
		FingerprintID: fingerprintID, FingerprintHash: fingerprintHash, ExpiresAt: expiresAt,
	})
	return map[string]any{
		"device_code":               string(codeJSON),
		"user_code":                 authCode,
		"verification_uri":          loginURL,
		"verification_uri_complete": loginURL,
		"expires_in":                expiresIn,
		"interval":                  5,
	}, nil
}

// ---- register-session ----

func dynRegisterSession(w http.ResponseWriter, r *http.Request, provider string, body map[string]any) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = rawStringField(body, "state")
	}
	if state == "" {
		axonError(w, http.StatusBadRequest, "Missing state")
		return
	}
	var ok bool
	switch provider {
	case "trae":
		ok = traeDynProxy.register(state, "", "")
	case "windsurf":
		ok = windsurfDynProxy.register(state, "", "")
	case "zed":
		ok = zedDynProxy.register(state, rawStringField(body, "codeVerifier"), rawStringField(body, "systemId"))
	default:
		axonError(w, http.StatusBadRequest, "register-session only supported for trae/windsurf/zed")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": ok})
}

// ---- manual-code (xai) ----

// dynManualCode completes an xAI login from a pasted code + the session the
// start-proxy step registered (Axon completeXaiManualCode).
func (h *OAuthHandler) dynManualCode(w http.ResponseWriter, provider string, body map[string]any) {
	if provider != "xai" {
		axonError(w, http.StatusBadRequest, "Manual code only supported for xai")
		return
	}
	code := strings.TrimSpace(rawStringField(body, "code"))
	state := strings.TrimSpace(rawStringField(body, "state"))
	fail := func(msg string) {
		xaiLoopback.clear(state)
		xaiLoopback.stop()
		axonError(w, http.StatusInternalServerError, msg)
	}
	sess, ok := xaiLoopback.lookup(state)
	if !ok {
		fail("xAI OAuth session not found; restart the login flow and paste the code again")
		return
	}
	if code == "" {
		fail("Missing xAI authorization code")
		return
	}
	ex := &pkceExchange{
		cfg:          pkceProviders["xai"],
		provider:     "xai",
		code:         code,
		codeVerifier: sess.codeVerifier,
		state:        state,
		redirectURI:  sess.redirectURI,
		clientID:     pkceProviders["xai"].clientID,
		tokenURL:     pkceProviders["xai"].tokenURL,
	}
	if disc, err := discoverOIDCEndpoints(pkceProviders["xai"].discoverIssuer); err == nil {
		ex.tokenURL = disc.tokenURL
	}
	res, xerr := h.completePKCEExchange(context.Background(), ex)
	if xerr != nil {
		fail(xerr.message)
		return
	}
	xaiLoopback.clear(state)
	xaiLoopback.stop()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id": res.ConnectionID, "provider": "xai",
			"email": nullOr(res.Email), "displayName": res.Name,
		},
	})
}
