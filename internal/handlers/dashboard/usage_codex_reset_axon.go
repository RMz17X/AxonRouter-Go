package dashboard

import (
	json "encoding/json/v2"
	"net/http"

	"9router/proxy/internal/codexquota"
	"9router/proxy/internal/handlerutil"
	"github.com/google/uuid"
)

// Codex reset credits, Axon contract — port of AxonRouter's
// GET/POST /api/usage/[connectionId]/codex-reset-credits (route.js), backed
// by the Axon-flavoured service calls in codexquota (FetchAxonResetCredits /
// ConsumeAxonResetCredit). This is a second, distinct contract next to the
// OmniRoute-shaped reset-credits handlers in usage_codex_reset.go: Axon
// lists every credit unfiltered, consumes with a server-minted redeem id
// and no credit selection, and answers with code/reset/windows_reset
// fields.
//
// Two Axon behaviours are not reproduced, both for the reason recorded in
// usage_codex_reset.go: Axon refreshes an OAuth connection's credentials
// before calling and retries once on an auth-expired result, but the
// dashboard package deliberately holds no token refresher, so an expired
// token surfaces as the upstream error instead; and Axon's per-connection
// proxy resolution has no equivalent in the dashboard's shared HTTP client.

// axonCodexTarget is a validated Codex connection for the Axon endpoints.
type axonCodexTarget struct {
	AccessToken string
	AccountID   string
}

// resolveAxonCodexTarget applies Axon's connection gate: the connection
// must exist, belong to the codex provider, and authenticate by OAuth or
// access token. Failures are written in Axon's flat error shape.
func (h *DashboardHandler) resolveAxonCodexTarget(w http.ResponseWriter, r *http.Request) (axonCodexTarget, bool) {
	connID := getURLParam(r, "connectionId")
	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return axonCodexTarget{}, false
	}
	if conn == nil {
		writePlainError(w, http.StatusNotFound, "Connection not found")
		return axonCodexTarget{}, false
	}
	if conn.Provider != "codex" {
		writePlainError(w, http.StatusBadRequest,
			"Codex reset credits are only available for Codex connections.")
		return axonCodexTarget{}, false
	}
	if conn.AuthType != "oauth" && conn.AuthType != "access_token" {
		writePlainError(w, http.StatusBadRequest,
			"Codex reset credits require an OAuth or access-token connection.")
		return axonCodexTarget{}, false
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}
	accessToken, apiKey, psd := usageCreds(data)
	if accessToken == "" {
		accessToken = apiKey
	}
	return axonCodexTarget{
		AccessToken: accessToken,
		AccountID:   psdStr(psd, "workspaceId", "accountId", "chatgptAccountId", "chatgpt_account_id"),
	}, true
}

// HandleGetCodexResetCredits handles GET
// /api/usage/{connectionId}/codex-reset-credits.
func (h *DashboardHandler) HandleGetCodexResetCredits(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveAxonCodexTarget(w, r)
	if !ok {
		return
	}
	credits, err := codexquota.FetchAxonResetCredits(r.Context(), nil, target.AccessToken, target.AccountID)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, credits)
}

// HandleConsumeCodexResetCredits handles POST
// /api/usage/{connectionId}/codex-reset-credits. The redeem id is minted
// server-side, as in Axon, so a client cannot replay a chosen one.
func (h *DashboardHandler) HandleConsumeCodexResetCredits(w http.ResponseWriter, r *http.Request) {
	target, ok := h.resolveAxonCodexTarget(w, r)
	if !ok {
		return
	}

	redeemRequestID := uuid.NewString()
	result, err := codexquota.ConsumeAxonResetCredit(r.Context(), nil, target.AccessToken, redeemRequestID)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}

	switch {
	case result.OK:
		var credit any
		if result.Raw != nil {
			credit = result.Raw["credit"]
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"code":            result.Code,
			"reset":           true,
			"windows_reset":   result.WindowsReset,
			"redeemRequestId": redeemRequestID,
			"credit":          credit,
		})
	case result.NoCredit:
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"code":          "no_credit",
			"reset":         false,
			"windows_reset": result.WindowsReset,
			"message":       "No Codex reset credits available.",
		})
	default:
		status := http.StatusBadGateway
		if result.Status >= 400 && result.Status < 500 {
			status = result.Status
		}
		code := "unknown_response"
		if result.Code != nil && *result.Code != "" {
			code = *result.Code
		}
		message := "Codex reset credit consume returned an unexpected response."
		if result.Message != nil && *result.Message != "" {
			message = *result.Message
		}
		handlerutil.WriteJSON(w, status, map[string]any{
			"code":          code,
			"reset":         false,
			"windows_reset": result.WindowsReset,
			"message":       message,
		})
	}
}
