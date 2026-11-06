package providers

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
)

// tos_disabled.go holds the ONE signature for a Google ToS disable, shared
// by every generation surface (chat, media image/search/STT) and the chat
// fallback chain. Before fragment 973 each surface string-matched its own
// phrase list, and the media lanes never matched at all — they parked the
// account on the generic 24h ToS cooldown and kept serving it, which is
// exactly how a quota-path check masked the JS pool's disables: the
// account looked "cooling down", never disabled.
//
// Detection is deliberately narrow and generation-shaped. The verdict is
// a 403 whose body carries the disablement signature: the TOS_VIOLATION
// status/reason token, or the literal disablement sentence ("This service
// has been disabled in this account for violation of Terms of Service").
// PERMISSION_DENIED alone is NOT the signature — VALIDATION_REQUIRED
// answers carry it too, and that account is one verification click from
// healthy, not dead. Other statuses are never the signature either: a
// 429 quoting the sentence is a throttle verdict (see the ExcludeStatus
// reasoning on the ErrorRules ToS entries), and quota/loadCodeAssist
// responses are never fed here by design — those paths keep answering
// for a disabled account, so their errors prove nothing about it.

// LooksLikeToSDisabled reports whether an upstream response is the ToS
// disable verdict. text is matched case-insensitively and should be the
// fullest text available (raw body or extracted error text — the status
// and reason tokens the signature needs live outside error.message).
func LooksLikeToSDisabled(statusCode int, text string) bool {
	if statusCode != http.StatusForbidden || text == "" {
		return false
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, "tos_violation") ||
		strings.Contains(lower, "disabled in this account") ||
		strings.Contains(lower, "violation of terms of service") ||
		strings.Contains(lower, "violation of the terms of service")
}

// GoogleErrorText flattens a Google-shaped error body into the verbatim
// text a stored disable reason carries: error.message, then error.status,
// then any details[].reason tokens, space-joined (the same shape the chat
// fallback's extractErrorText stores, so a disabled account reads the
// same whichever surface caught it). A body that is not that shape falls
// back to its trimmed self, truncated so a pathological body cannot bloat
// the connection row.
func GoogleErrorText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err == nil && raw.Error.Message != "" {
		parts := []string{raw.Error.Message}
		if raw.Error.Status != "" {
			parts = append(parts, raw.Error.Status)
		}
		for _, d := range raw.Error.Details {
			if d.Reason != "" {
				parts = append(parts, d.Reason)
			}
		}
		return strings.Join(parts, " ")
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}
