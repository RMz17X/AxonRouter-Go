package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/proxy"
	"axonrouter/proxy/internal/schedule"
)

// findValidationURL walks a decoded Google error payload for the
// account-specific verification link the error carries
// (error.details[].metadata.validation_url). The link's plt token
// rotates with every fresh error, so the freshest payload always wins
// over whatever a previous verdict stored.
func findValidationURL(v any) string {
	switch node := v.(type) {
	case map[string]any:
		if s, ok := node["validation_url"].(string); ok && s != "" {
			return s
		}
		for _, child := range node {
			if s := findValidationURL(child); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range node {
			if s := findValidationURL(child); s != "" {
				return s
			}
		}
	}
	return ""
}

func validationURLFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return ""
	}
	return findValidationURL(doc)
}

// recheckValidationFlag decides whether a validation-flagged connection
// may be released, using the only verdict that cannot lie: a real
// generate through the ordinary chat lane with the connection pinned
// (the auto-ping machinery). The light account probe (loadCodeAssist)
// accepts accounts the generate surface still refuses with
// VALIDATION_REQUIRED, so releasing on it merely re-parks the account
// on the next quota read — the operator watches the row oscillate and
// the account never actually serves.
//
// The account is temporarily reactivated so the pinned lane will serve
// it (a probe is never redirected to another account, so the verdict
// belongs to this connection alone). A passing ping leaves it
// released. A failing ping that still carries the verification gate
// re-parks it with the freshest verdict: the refusal carries the
// current message and a fresh account-specific verification URL,
// whose token the stored Verify link then carries. A failing ping
// where Google answered with anything else (e.g. quota) proves the
// gate itself is gone and also releases — only a ping with no
// upstream answer at all (transport, timeout) keeps the previously
// stored verdict rather than overwriting it with noise.
func (h *DashboardHandler) recheckValidationFlag(ctx context.Context, conn *models.ProviderConnection, data map[string]any) bool {
	if conn == nil || data == nil || h.Chat == nil {
		return false
	}
	origMessage := lastErrorText(data)
	origURL, _ := data["verificationUrl"].(string)

	// Temporary release: the same field clearing the verified
	// transition performs, so the pinned probe lane treats the account
	// as an ordinary active connection for the duration of the ping.
	released := make(map[string]any, len(data))
	for k, v := range data {
		released[k] = v
	}
	delete(released, "status")
	delete(released, "verificationUrl")
	delete(released, "validationMessage")
	delete(released, "validationAt")
	released["testStatus"] = "active"
	released["lastError"] = nil
	released["lastErrorAt"] = nil
	if encoded, err := json.Marshal(released); err == nil {
		_ = h.Repo.UpdateConnectionData(conn.ID, string(encoded))
	}
	_ = h.Repo.SetConnectionStatus(conn.ID, true)

	pingErr := schedule.PingConnection(ctx, h.Chat, conn.Provider, conn.ID)
	if pingErr == nil {
		log.Info("usage", "validation recheck passed generate probe: account released",
			"provider", conn.Provider, "conn", conn.ID)
		return true
	}

	message := origMessage
	verifyURL := origURL
	var ue *proxy.UpstreamError
	stillValidation := false
	if errors.As(pingErr, &ue) && ue != nil {
		lower := strings.ToLower(string(ue.Body))
		if strings.Contains(lower, "validation_required") || strings.Contains(lower, "verify your account") {
			stillValidation = true
			var doc map[string]any
			if err := json.Unmarshal(ue.Body, &doc); err == nil {
				if msg := extractGoogleErrorMessage(doc); msg != "" {
					message = msg
				}
			}
			if u := validationURLFromBody(ue.Body); u != "" {
				verifyURL = u
			}
		}
	}
	if ue != nil && len(ue.Body) > 0 && !stillValidation {
		// Google answered the generate with a refusal that is NOT the
		// verification gate (e.g. 429 quota exhausted — the request
		// passed the gate and hit quota instead). The validation
		// verdict this row is parked on is therefore stale: the gate
		// is gone (the owner proves it by completing the scan
		// verification in Google's page, after which the account's
		// existing token serves again with no fresh sign-in). Release
		// the account — the temporary release above already left the
		// row clean and active; ordinary quota/cooldown machinery
		// handles the refusal that remains. A ping that produced no
		// upstream answer at all (transport, timeout) is no verdict
		// and still re-parks below.
		log.Info("usage", "validation recheck: upstream answered without the verification gate, account released",
			"provider", conn.Provider, "conn", conn.ID, "upstream_status", ue.StatusCode)
		return true
	}
	if verifyURL == origURL {
		// The ping's own chat-lane classifier may already have stored a
		// fresher URL from the same refusal (its token is newer than the
		// one this recheck started with); keep the freshest of the two
		// rather than overwriting it with the stale one.
		if fresh, ferr := h.Repo.GetProviderConnectionByID(conn.ID); ferr == nil && fresh != nil && fresh.Data != "" {
			var fd map[string]any
			if err := json.Unmarshal([]byte(fresh.Data), &fd); err == nil && fd != nil {
				if u, _ := fd["verificationUrl"].(string); u != "" {
					verifyURL = u
				}
			}
		}
	}
	if verifyURL == "" {
		verifyURL = "https://antigravity.google"
	}
	if message == "" {
		message = "Verify your account to continue. PERMISSION_DENIED VALIDATION_REQUIRED"
	}
	_ = h.Repo.SetConnectionVerificationRequired(conn.ID, verifyURL, message)
	log.Info("usage", "validation recheck failed generate probe: account stays parked",
		"provider", conn.Provider, "conn", conn.ID)
	return false
}
