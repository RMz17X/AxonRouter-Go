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
// released. A failing one re-parks it with the freshest verdict: a
// generate refusal carries the current message and a fresh
// account-specific verification URL, whose token the stored Verify
// link then carries; any other failure (transport, timeout) keeps the
// previously stored verdict rather than overwriting it with noise.
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
	if errors.As(pingErr, &ue) && ue != nil {
		lower := strings.ToLower(string(ue.Body))
		if strings.Contains(lower, "validation_required") || strings.Contains(lower, "verify your account") {
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
