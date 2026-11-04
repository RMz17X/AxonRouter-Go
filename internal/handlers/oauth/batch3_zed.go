package oauth

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// Zed imports (AxonRouter src/app/api/oauth/zed/*): validate + save a Zed
// session from the IDE, and read the signed-in IDE session from the OS
// credential store (Axon src/lib/oauth/utils/zedCredentials.js).

// fetchZedAuthenticatedUser mirrors open-sse/shared/zedAuth.js
// fetchZedAuthenticatedUser: GET cloud.zed.dev/client/users/me with the
// "<userId> <accessToken>" authorization header. Non-2xx answers become an
// error carrying the upstream message, like fetchJson.
func fetchZedAuthenticatedUser(ctx context.Context, userID, accessToken, systemID string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zedCloudBase+zedMePath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", userID+" "+accessToken)
	if systemID != "" {
		req.Header.Set(zedSystemHeader, systemID)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := jsonStringField(data, "message")
		if msg == "" {
			if e, ok := data["error"].(map[string]any); ok {
				msg = jsonStringField(e, "message")
			}
		}
		if msg == "" {
			msg = jsonStringField(data, "error")
		}
		if msg == "" && len(raw) > 0 {
			msg = string(raw)
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return data, nil
}

// zedOrganizationID mirrors resolveZedOrganizationId for a bare userInfo
// payload (the import flow has no stored credentials to consult).
func zedOrganizationID(userInfo map[string]any) string {
	normalize := func(v any) string {
		switch t := v.(type) {
		case string:
			return t
		case float64:
			return fmt.Sprintf("%v", t)
		case []any:
			if len(t) > 0 {
				if s, ok := t[0].(string); ok {
					return s
				}
			}
		case map[string]any:
			if s, ok := t["id"].(string); ok {
				return s
			}
		}
		return ""
	}
	if userInfo == nil {
		return ""
	}
	if id := normalize(userInfo["default_organization_id"]); id != "" {
		return id
	}
	if id := normalize(userInfo["defaultOrganizationId"]); id != "" {
		return id
	}
	orgs, _ := userInfo["organizations"].([]any)
	var first map[string]any
	for _, o := range orgs {
		org, _ := o.(map[string]any)
		if org == nil {
			continue
		}
		if first == nil {
			first = org
		}
		if personal, _ := org["is_personal"].(bool); personal {
			return normalize(org["id"])
		}
	}
	if first != nil {
		return normalize(first["id"])
	}
	return ""
}

// HandleZedImport validates a Zed session token against cloud.zed.dev and
// stores it as a zed connection.
// POST /api/oauth/zed/import {accessToken, userId, systemId?}
func (h *OAuthHandler) HandleZedImport(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	accessToken := jsonStringField(body, "accessToken")
	userID := ""
	if body != nil {
		switch v := body["userId"].(type) {
		case string:
			userID = strings.TrimSpace(v)
		case float64:
			userID = fmt.Sprintf("%v", v)
		}
	}
	systemID := jsonStringField(body, "systemId")
	if systemID == "" {
		systemID = randomUUID()
	}
	if accessToken == "" {
		axonError(w, http.StatusBadRequest, "Access token is required")
		return
	}
	if userID == "" {
		axonError(w, http.StatusBadRequest, "User id is required")
		return
	}
	userInfo, err := fetchZedAuthenticatedUser(r.Context(), userID, accessToken, systemID)
	if err != nil {
		msg := err.Error()
		if msg == "" {
			msg = "Zed token validation failed"
		}
		axonError(w, http.StatusUnauthorized, msg)
		return
	}
	orgID := zedOrganizationID(userInfo)
	email := jsonStringField(userInfo, "email")
	displayName := firstNonEmpty(
		jsonStringField(userInfo, "name"),
		jsonStringField(userInfo, "display_name"),
		jsonStringField(userInfo, "username"),
		"Zed "+userID,
	)
	dataMap := map[string]any{
		"apiKey":      accessToken,
		"accessToken": accessToken,
		"testStatus":  "active",
		"providerSpecificData": map[string]any{
			"authMethod":     "imported",
			"userId":         userID,
			"systemId":       systemID,
			"organizationId": orgID,
		},
	}
	if email != "" {
		dataMap["email"] = email
	}
	connID := "zed-" + shortHash(accessToken)
	if err := h.saveAxonConnection(connID, "zed", "oauth", displayName, email, nil, dataMap); err != nil {
		log.Error("oauth", "zed import save failed", "error", err)
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"connection": map[string]any{
			"id": connID, "provider": "zed",
			"email": nullOr(email), "displayName": displayName,
		},
	})
}

// credential store. GET /api/oauth/zed/auto-import
func (h *OAuthHandler) HandleZedAutoImport(w http.ResponseWriter, r *http.Request) {
	result := readZedIDECredentials()
	if !result.found {
		errText := result.errText
		if errText == "" {
			errText = "Zed IDE credentials not found"
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false, "error": errText, "credentialsUrl": nullOr(result.credentialsURL),
		})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"found":          true,
		"userId":         result.userID,
		"accessToken":    result.accessToken,
		"systemId":       result.systemID,
		"credentialsUrl": result.credentialsURL,
	})
}
