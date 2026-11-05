package oauth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/handlerutil"
)

// The POST poll action of the generic dispatch route (split from
// batch3_dynamic2.go), plus the WorkBuddy device pair it and the
// device-code action dispatch to.

// ---- poll ----

// dynNoPkcePollProviders is Axon's poll list whose codeVerifier is ignored.
var dynNoPkcePollProviders = map[string]bool{
	"github": true, "kimi": true, "kimi-coding": true, "kilocode": true,
	"codebuddy-cn": true, "codebuddy-intl": true, "workbuddy": true,
	"muse": true, "nous": true,
}

// dynPoll is Axon's POST poll action (device_code flows): validate the
// verifier gate for the provider's class, rebuild the start-time session
// from extraData, run one device poll, and answer in Axon's envelope —
// {success:true, connection:{id, provider}} or HTTP 200
// {success:false, error, errorDescription, pending}.
func (h *OAuthHandler) dynPoll(w http.ResponseWriter, r *http.Request, provider string, body map[string]any) {
	deviceCode := rawStringField(body, "deviceCode")
	codeVerifier := rawStringField(body, "codeVerifier")
	extraData, _ := body["extraData"].(map[string]any)
	if extraData == nil {
		extraData = map[string]any{}
	}
	if deviceCode == "" {
		axonError(w, http.StatusBadRequest, "Missing device code")
		return
	}
	flow, known := axonFlow(provider)
	if !known {
		axonError(w, http.StatusInternalServerError, "Unknown provider: "+provider)
		return
	}
	if flow != "device_code" {
		axonError(w, http.StatusInternalServerError,
			fmt.Sprintf("Provider %s does not support device code flow", provider))
		return
	}
	if !dynNoPkcePollProviders[provider] && provider != "kiro" && codeVerifier == "" {
		axonError(w, http.StatusBadRequest, "Missing code verifier")
		return
	}
	if provider == "freebuff" {
		h.dynFreebuffPoll(w, r, deviceCode)
		return
	}
	if provider == "workbuddy" {
		h.dynWorkbuddyPoll(w, r, deviceCode)
		return
	}
	canonical := deviceCanonical(provider)
	session := map[string]any{}
	switch canonical {
	case "qoder", "qoder-cn":
		session["verifier"] = codeVerifier
		session["machineId"] = strVal(extraData, "_qoderMachineId")
	case "kiro":
		session["clientId"] = strVal(extraData, "_clientId")
		session["clientSecret"] = strVal(extraData, "_clientSecret")
		session["region"] = strVal(extraData, "_region")
		session["authMethod"] = strVal(extraData, "_authMethod")
		session["startUrl"] = strVal(extraData, "_startUrl")
	case "kimi":
		session["deviceId"] = strVal(extraData, "_kimiDeviceId")
	case "minimax-code", "minimax-code-global":
		session["codeVerifier"] = codeVerifier
	}
	tokens, status, errMsg := devicePoll(r.Context(), canonical, deviceCode, session)
	h.dynWritePollResult(w, canonical, tokens, status, errMsg)
}

// dynWritePollResult renders one device poll outcome in Axon's envelope,
// saving the connection through the shared device machinery on success.
func (h *OAuthHandler) dynWritePollResult(w http.ResponseWriter, provider string, tokens deviceTokens, status, errMsg string) {
	if status == "authorized" && strings.TrimSpace(tokens.access) != "" {
		conn := h.saveDeviceConnection(provider, tokens)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"connection": map[string]any{"id": conn.id, "provider": provider},
		})
		return
	}
	errText := errMsg
	pending := status == "pending"
	if pending && errText == "" {
		errText = "authorization_pending"
	}
	if errText == "authorization_pending" || errText == "slow_down" {
		pending = true
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": false, "error": nullOr(errText),
		"errorDescription": nil, "pending": pending,
	})
}

// dynFreebuffPoll drives the dedicated freebuff poll handler with the
// fingerprint triple packed into Axon's device_code JSON.
func (h *OAuthHandler) dynFreebuffPoll(w http.ResponseWriter, r *http.Request, deviceCode string) {
	var dc freebuffDeviceCodePayload
	if err := json.Unmarshal([]byte(deviceCode), &dc); err != nil || dc.FingerprintID == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "error": "invalid_device_code",
			"errorDescription": nil, "pending": false,
		})
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"fingerprintId": dc.FingerprintID, "fingerprintHash": dc.FingerprintHash,
		"expiresAt": dc.ExpiresAt,
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"http://127.0.0.1/api/oauth/freebuff/poll", bytes.NewReader(payload))
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	status, captured := runCaptured(func(cw http.ResponseWriter) { h.HandleFreebuffPoll(cw, req) })
	switch {
	case captured["status"] == "authorized":
		id, _ := captured["connectionId"].(string)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"connection": map[string]any{"id": id, "provider": "freebuff"},
		})
	case captured["status"] == "expired":
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "error": "expired_token",
			"errorDescription": nil, "pending": false,
		})
	case status != http.StatusOK:
		msg := capturedErrorMessage(captured)
		if msg == "" {
			msg = "freebuff poll failed"
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "error": msg,
			"errorDescription": nil, "pending": false,
		})
	default:
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": false, "error": "authorization_pending",
			"errorDescription": nil, "pending": true,
		})
	}
}

// ---- workbuddy device pair ----

const (
	workbuddyBase = "https://www.workbuddy.ai"
	workbuddyUA   = "IDE/2.63.2 WorkBuddy/2.63.2"
)

// workbuddyStart mirrors Axon's workbuddy requestDeviceCode: POST the state
// endpoint on www.workbuddy.ai and package state + authUrl in the device
// shape (codebuddyStart's sibling, on WorkBuddy's host/UA/domain).
func workbuddyStart(ctx context.Context) (map[string]any, error) {
	headers := map[string]string{
		"User-Agent": workbuddyUA, "X-Requested-With": "XMLHttpRequest",
		"X-Domain": "www.workbuddy.ai", "X-No-Authorization": "true",
		"X-No-User-Id": "true", "X-Product": "SaaS",
	}
	data, status, err := postJSON(ctx, workbuddyBase+"/v2/plugin/auth/state?platform=ide",
		map[string]string{}, headers)
	if err != nil || status != http.StatusOK {
		return nil, fmt.Errorf("WorkBuddy state request failed: %v status=%d", err, status)
	}
	var code float64
	if c, ok := data["code"].(float64); ok {
		code = c
	}
	inner, _ := data["data"].(map[string]any)
	state, authURL := strVal(inner, "state"), strVal(inner, "authUrl")
	if code != 0 || state == "" || authURL == "" {
		msg := strVal(data, "msg")
		if msg == "" {
			msg = "missing state/authUrl"
		}
		return nil, fmt.Errorf("WorkBuddy state error: %s", msg)
	}
	return map[string]any{
		"device_code": state, "user_code": "",
		"verification_uri": authURL, "verification_uri_complete": authURL,
		"interval": 5, "session": map[string]any{},
	}, nil
}

// workbuddyPoll mirrors codebuddyPoll against WorkBuddy's token endpoint:
// code 0 + accessToken wins, 11217 is authorization_pending, anything else
// surfaces the upstream message.
func workbuddyPoll(ctx context.Context, state string) (deviceTokens, error) {
	var t deviceTokens
	headers := map[string]string{
		"Accept": "application/json", "User-Agent": workbuddyUA,
		"X-Requested-With": "XMLHttpRequest", "X-Domain": "www.workbuddy.ai",
		"X-No-Authorization": "true", "X-No-User-Id": "true",
		"X-No-Enterprise-Id": "true", "X-No-Department-Info": "true",
		"X-Product": "SaaS",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		workbuddyBase+"/v2/plugin/auth/token?state="+url.QueryEscape(state), nil)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := deviceClient.Do(req)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return t, fmt.Errorf("request_failed")
	}
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data map[string]any
	if json.Unmarshal(out, &data) != nil {
		return t, fmt.Errorf("request_failed")
	}
	var code float64
	if c, ok := data["code"].(float64); ok {
		code = c
	}
	inner, _ := data["data"].(map[string]any)
	if code == 0 && strVal(inner, "accessToken") != "" {
		t.access = strVal(inner, "accessToken")
		t.refresh = strVal(inner, "refreshToken")
		return t, nil
	}
	if code == 11217 {
		return t, errors.New("authorization_pending")
	}
	msg := strVal(data, "msg", "error")
	if msg == "" {
		msg = "unknown_error"
	}
	return t, errors.New(msg)
}

// dynWorkbuddyPoll normalizes workbuddyPoll's outcome the way devicePoll
// normalizes the shared providers (pending wording → pending status).
func (h *OAuthHandler) dynWorkbuddyPoll(w http.ResponseWriter, r *http.Request, state string) {
	tokens, err := workbuddyPoll(r.Context(), state)
	status, errMsg := "authorized", ""
	if err != nil {
		errMsg = err.Error()
		status = "error"
		if strings.Contains(errMsg, "authorization_pending") ||
			strings.Contains(errMsg, "slow_down") || strings.Contains(errMsg, "pending") {
			status = "pending"
		}
	} else if tokens.access == "" {
		status = "pending"
	}
	h.dynWritePollResult(w, "workbuddy", tokens, status, errMsg)
}
