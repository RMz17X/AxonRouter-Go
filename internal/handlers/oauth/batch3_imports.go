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
)

// codexJWTConnectionData extracts the stored payload for a pasted ChatGPT
// access token: identity claims from the JWT (profile email, ChatGPT account
// id, plan, expiry) under an access_token providerSpecificData, mirroring
// Axon's codex/import-token route and the generic route's codex JWT branch.
// Returns the data blob plus the extracted email ("" when not a JWT).
func codexJWTConnectionData(token string) (map[string]any, string) {
	psd := map[string]any{"authMethod": "access_token"}
	email := ""
	if payload := decodeJWTPayload(token); payload != nil {
		auth, _ := payload["https://api.openai.com/auth"].(map[string]any)
		profile, _ := payload["https://api.openai.com/profile"].(map[string]any)
		email = firstNonEmpty(stringClaim(profile, "email"), stringClaim(payload, "email"), stringClaim(payload, "preferred_username"))
		if v := stringClaim(auth, "chatgpt_account_id"); v != "" {
			psd["chatgptAccountId"] = v
		}
		if v := stringClaim(auth, "chatgpt_plan_type"); v != "" {
			psd["chatgptPlanType"] = v
		}
		if exp, ok := payload["exp"].(float64); ok {
			psd["jwtExp"] = int64(exp)
		}
	}
	if email == "" {
		// Fall back to the id_token-style claim extraction (root-level
		// account_id / plan_type) like Axon's extractCodexAccountInfo.
		info := codexAccountInfo(token)
		email = info.Email
		if _, has := psd["chatgptAccountId"]; !has && info.AccountID != "" {
			psd["chatgptAccountId"] = info.AccountID
		}
		if _, has := psd["chatgptPlanType"]; !has && info.PlanType != "" {
			psd["chatgptPlanType"] = info.PlanType
		}
	}
	dataMap := map[string]any{
		"apiKey":               token,
		"accessToken":          token,
		"providerSpecificData": psd,
		"testStatus":           "active",
	}
	if email != "" {
		dataMap["email"] = email
	}
	return dataMap, email
}

// HandleCodexImportToken imports a pasted ChatGPT access token as a Codex
// connection, bypassing the OAuth refresh flow.
// POST /api/oauth/codex/import-token {accessToken, name?}
// Contract: AxonRouter src/app/api/oauth/codex/import-token/route.js.
func (h *OAuthHandler) HandleCodexImportToken(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	accessToken, _ := body["accessToken"].(string)
	if strings.TrimSpace(accessToken) == "" {
		axonError(w, http.StatusBadRequest, "Access token is required")
		return
	}
	token := strings.TrimSpace(accessToken)
	dataMap, email := codexJWTConnectionData(token)
	name := jsonStringField(body, "name")
	if name == "" {
		name = firstNonEmpty(email, "ChatGPT Access Token")
	}
	connID := "cx-" + shortHash(token)
	psd, _ := dataMap["providerSpecificData"].(map[string]any)
	if err := h.saveAxonConnection(connID, "codex", "access_token", name, email, nil, dataMap); err != nil {
		log.Error("oauth", "codex import-token save failed", "error", err)
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":        connID,
			"provider":  "codex",
			"email":     nullOr(email),
			"name":      name,
			"workspace": psdString(psd, "chatgptAccountId"),
			"plan":      psdString(psd, "chatgptPlanType"),
		},
	})
}

// supergrok token endpoint + client id (Axon SUPERGROK_CONFIG; the client id
// is the same public xAI OAuth client the pkce config carries).
const (
	supergrokTokenURL  = "https://auth.x.ai/oauth2/token"
	supergrokModelsURL = "https://api.x.ai/v1/models"
	supergrokClientID  = "b1a00492-073a-47ea-816f-4c329264a828"
)

// HandleSupergrokImport imports SuperGrok OAuth credentials: a refresh token
// is exchanged for a fresh access token, an access token is validated against
// the xAI models endpoint, and the pair is stored as a supergrok connection.
// POST /api/oauth/supergrok/import {refreshToken?, accessToken?, expiresIn?}
// Contract: AxonRouter src/app/api/oauth/supergrok/import/route.ts.
func (h *OAuthHandler) HandleSupergrokImport(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if body == nil {
		body = map[string]any{}
	}
	if detail := supergrokValidationDetail(body); detail != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "Validation failed", "details": []any{detail}},
		})
		return
	}
	refreshToken := jsonStringField(body, "refreshToken")
	accessToken := jsonStringField(body, "accessToken")
	expiresIn := 3600
	if v, ok := body["expiresIn"].(float64); ok && v > 0 {
		expiresIn = int(v)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if refreshToken != "" && accessToken == "" {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {refreshToken},
			"client_id":     {supergrokClientID},
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, supergrokTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			axonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			axonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			axonError(w, http.StatusUnauthorized, fmt.Sprintf("Token refresh failed (HTTP %d)", resp.StatusCode))
			return
		}
		var tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    *int   `json:"expires_in"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = json.Unmarshal(raw, &tokens)
		accessToken = tokens.AccessToken
		if tokens.RefreshToken != "" {
			refreshToken = tokens.RefreshToken
		}
		if tokens.ExpiresIn != nil {
			expiresIn = *tokens.ExpiresIn
		} else {
			expiresIn = 3600
		}
	}

	if accessToken != "" {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, supergrokModelsURL, nil)
		if err != nil {
			axonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		resp, err := client.Do(req)
		if err != nil {
			axonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			axonError(w, http.StatusForbidden, "OAuth connected but API access denied - SuperGrok subscription may be required")
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			axonError(w, http.StatusUnauthorized, fmt.Sprintf("Token validation failed with status %d", resp.StatusCode))
			return
		}
	}

	email := ""
	if accessToken != "" {
		if payload := decodeJWTPayload(accessToken); payload != nil {
			email = firstNonEmpty(stringClaim(payload, "email"), stringClaim(payload, "preferred_username"), stringClaim(payload, "sub"))
		}
	}
	ephemeral := refreshToken == ""
	psd := map[string]any{"authMethod": "supergrok_oauth", "provider": "SuperGrok Import"}
	if ephemeral {
		psd["ephemeral"] = true
	}
	nowISO := axonNowISO()
	dataMap := map[string]any{
		"providerSpecificData": psd,
		"expiresAt":            axonExpiresISO(time.Duration(expiresIn) * time.Second),
		"routingStatus":        "eligible",
		"quotaState":           "ok",
		"authState":            "ok",
		"healthStatus":         "healthy",
		"reasonCode":           nil,
		"reasonDetail":         nil,
		"nextRetryAt":          nil,
		"resetAt":              nil,
		"lastCheckedAt":        nowISO,
		"testStatus":           "active",
	}
	if accessToken != "" {
		dataMap["apiKey"] = accessToken
		dataMap["accessToken"] = accessToken
	}
	if refreshToken != "" {
		dataMap["refreshToken"] = refreshToken
	}
	if email != "" {
		dataMap["email"] = email
	}
	connID := "supergrok-" + shortHash(firstNonEmpty(accessToken, refreshToken, randomString(16)))
	if err := h.saveAxonConnection(connID, "supergrok", "supergrok_oauth", email, email, nil, dataMap); err != nil {
		log.Error("oauth", "supergrok import save failed", "error", err)
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respBody := map[string]any{
		"success": true,
		"connection": map[string]any{
			"id":            connID,
			"provider":      "supergrok",
			"email":         nullOr(email),
			"displayName":   nullOr(email),
			"routingStatus": "eligible",
			"healthStatus":  "healthy",
			"quotaState":    "ok",
			"authState":     "ok",
			"reasonCode":    nil,
			"reasonDetail":  nil,
			"lastCheckedAt": nowISO,
		},
	}
	if ephemeral {
		respBody["warning"] = "No refresh token provided - connection will expire and require re-import"
	}
	handlerutil.WriteJSON(w, http.StatusOK, respBody)
}

// supergrokValidationDetail mirrors the zod schema on Axon's route: string
// tokens of length ≥ 1, a positive expiresIn, and at least one token present.
// Returns the first issue as {field, message}, or nil when valid.
func supergrokValidationDetail(body map[string]any) map[string]any {
	for _, key := range []string{"refreshToken", "accessToken"} {
		v, present := body[key]
		if !present || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return map[string]any{"field": key, "message": "Expected string, received " + zodTypeName(v)}
		}
		if len(s) < 1 {
			return map[string]any{"field": key, "message": "String must contain at least 1 character(s)"}
		}
	}
	if v, present := body["expiresIn"]; present && v != nil {
		n, ok := v.(float64)
		if !ok {
			return map[string]any{"field": "expiresIn", "message": "Expected number, received " + zodTypeName(v)}
		}
		if n <= 0 {
			return map[string]any{"field": "expiresIn", "message": "Number must be greater than 0"}
		}
	}
	if jsonStringField(body, "refreshToken") == "" && jsonStringField(body, "accessToken") == "" {
		return map[string]any{"field": "", "message": "Either refreshToken or accessToken is required"}
	}
	return nil
}

// zodTypeName names a decoded JSON value the way zod reports it in issues.
func zodTypeName(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "undefined"
	}
	return "unknown"
}

// nullOr maps "" to nil so optional response fields serialize as JSON null.
func nullOr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// psdString reads a string claim from a providerSpecificData map (nil-safe).
func psdString(psd map[string]any, key string) any {
	if psd == nil {
		return nil
	}
	if s, _ := psd[key].(string); s != "" {
		return s
	}
	return nil
}
