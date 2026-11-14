package dashboard

import "strings"

// cleanUsageMessage turns a raw fetcher/refresh error into the short,
// human sentence the quota tracker shows on an account card. Upstream
// failures otherwise arrive as Go error dumps — a full request URL with
// query parameters, or the provider's raw JSON body — which are noise to
// an operator and leak per-account identifiers into the UI. The mapping
// is deliberately conservative: wording the dashboard's own classifiers
// key on (terms-of-service / suspension / verification phrases, checked
// by the tracker and by the antigravity branch of the usage handler)
// passes through untouched, and anything unrecognised keeps its text
// with only the dump-shaped parts (JSON bodies, request URLs) cut away.
func cleanUsageMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)

	// Classification-critical wording: never rewrite.
	for _, keep := range []string{
		"terms of service", "tos_violation", "account disabled",
		"disabled in this account", "account suspended",
		"validation_required", "verify your account",
		"email_verification_required", "verify your email address",
	} {
		if strings.Contains(lower, keep) {
			return capUsageMessage(msg)
		}
	}

	switch {
	case strings.Contains(lower, "invalid_grant"),
		strings.Contains(lower, "refresh token can no longer be used"),
		strings.Contains(lower, "start a new authorization"):
		return "Sign-in expired — refresh token no longer valid. Re-authorize this account."
	case strings.Contains(lower, "token refresh rejected"):
		return "Sign-in expired — token refresh rejected by the provider. Re-authorize this account."
	case strings.Contains(lower, "context canceled"):
		return "Quota check was interrupted before it finished — press Refresh to retry."
	case strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		return "Quota check timed out — press Refresh to retry."
	case strings.Contains(lower, "dial tcp"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "tls handshake"),
		strings.Contains(lower, "proxyconnect"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "network is unreachable"):
		return "Could not reach the provider (network or proxy problem) — press Refresh to retry."
	case strings.Contains(lower, "http 401"), strings.Contains(lower, "http 403"),
		strings.Contains(lower, "status 401"), strings.Contains(lower, "status 403"):
		return "The provider refused this account's sign-in — re-authorize this account."
	}

	// Cut a trailing raw JSON body, keeping a meaningful lead-in.
	if i := strings.Index(msg, "{"); i >= 0 {
		head := strings.TrimRight(strings.TrimSpace(msg[:i]), ":-— ")
		if len(head) >= 12 && !strings.Contains(head, "://") {
			return capUsageMessage(head + ".")
		}
		return "The provider returned an error — press Refresh to retry."
	}
	// Cut a Go url.Error dump (method + full URL + wrapped cause).
	if strings.Contains(msg, "://") {
		return "Upstream request failed — press Refresh to retry."
	}
	return capUsageMessage(msg)
}

// capUsageMessage bounds a message to a card-friendly length.
func capUsageMessage(msg string) string {
	const max = 200
	if len(msg) <= max {
		return msg
	}
	return strings.TrimSpace(msg[:max-1]) + "…"
}

// cleanUsageQuotaMessages applies cleanUsageMessage to the message field
// of every quota row (some fetchers park a per-row failure there, e.g. the
// MiniMax M Plan windows row).
func cleanUsageQuotaMessages(quotas map[string]any) {
	for _, row := range quotas {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["message"].(string); ok && s != "" {
			m["message"] = cleanUsageMessage(s)
		}
	}
}
