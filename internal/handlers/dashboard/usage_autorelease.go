package dashboard

import (
	"context"
	json "encoding/json/v2"
	"strconv"
	"strings"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
)

// Auto-release of quota lock state on a healthy quota read (fragment 986).
//
// Before this, a quota read in the tracker was one-way: it could disable an
// account (the Antigravity ToS leg) but a read showing the quota had
// refilled changed nothing — the account stayed parked until its cooldown
// timer lapsed or an operator pressed the manual Reset button. Now, when a
// live usage fetch succeeds and reports quota available again, the
// connection's quota-class lock state is released through the same two Repo
// calls the DeepSeek credential renewal uses after a successful re-auth:
// the account-scoped rate limit and every per-model lock.
//
// Deliberate boundaries:
//   - Only quota-class state is released. The gate requires stored evidence
//     of a quota/rate-limit park (a rate-limit timer, a model lock, or a
//     stored quota/throttle error); a healthy read on an account parked for
//     anything else changes nothing.
//   - A stored ToS-disable signature vetoes the release outright, and
//     disabled accounts never reach this code (the usage handler returns
//     their stored reason before any fetch). Nothing here reactivates an
//     account or clears a verification flag — that stays with the manual
//     reset and the appeal flow.
//   - Cached usage responses return before any fetch, so the release fires
//     on live reads only (a forced refresh, or the first read after the
//     short response cache lapses) — never on a served-from-cache number.
//   - Antigravity's in-memory strike breaker is a separate, short-lived
//     layer owned by the chat path; it expires on its own windows and is
//     not touched here. What is released is the persisted state the tracker
//     and the manual reset govern.

// usageQuotasShowAvailability reports whether a fresh usage read shows the
// account can serve again: any unlimited row, any credit balance above
// zero, or any window with quota remaining.
func usageQuotasShowAvailability(quotas map[string]any) bool {
	for _, raw := range quotas {
		q, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if b, _ := q["unlimited"].(bool); b {
			return true
		}
		if b, _ := q["isCreditBalance"].(bool); b {
			if usageNum(q["total"], 0) > 0 {
				return true
			}
			continue
		}
		if usageNum(q["remainingPercentage"], 0) > 0 {
			return true
		}
	}
	return false
}

// connectionQuotaLockState reports whether the connection's stored data
// carries a quota-class park worth releasing, and vetoes the release when
// the stored error carries a ToS-disable signature.
func connectionQuotaLockState(data map[string]any) bool {
	if data == nil {
		return false
	}
	lastError := lastErrorText(data)
	if lastError != "" && providers.LooksLikeToSDisabled(storedErrorCode(data), lastError) {
		return false
	}
	if s, _ := data["rateLimitedUntil"].(string); strings.TrimSpace(s) != "" {
		return true
	}
	if s, _ := data["lockedAllUntil"].(string); strings.TrimSpace(s) != "" {
		return true
	}
	for k, v := range data {
		if strings.HasPrefix(k, "modelLock_") {
			if s, _ := v.(string); strings.TrimSpace(s) != "" {
				return true
			}
		}
	}
	if lastError != "" && (providers.LooksLikeQuotaExhausted(lastError) || providers.LooksLikeRateLimited(lastError)) {
		return true
	}
	return false
}

// storedErrorCode coerces the stored errorCode (number or string) to an int.
func storedErrorCode(data map[string]any) int {
	switch v := data["errorCode"].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

// maybeAutoReleaseQuotaLocks releases a connection's quota lock state when
// a live usage read shows quota available again. Best-effort: failures are
// logged and never fail the usage response. The connection is re-read
// before deciding, so a lock written by traffic that landed while the
// usage fetch was in flight is evaluated as it stands now, not as it stood
// when the read began.
func (h *DashboardHandler) maybeAutoReleaseQuotaLocks(ctx context.Context, connID string, quotas map[string]any) {
	if !usageQuotasShowAvailability(quotas) {
		return
	}
	fresh, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil || fresh == nil || fresh.IsActive == 0 {
		return
	}
	var data map[string]any
	if fresh.Data != "" {
		_ = json.Unmarshal([]byte(fresh.Data), &data)
	}
	if !connectionQuotaLockState(data) {
		return
	}
	if err := h.Repo.ClearConnectionRateLimit(connID); err != nil {
		log.Warn("usage", "auto-release rate limit failed", "conn", connID, "error", err)
		return
	}
	if err := h.Repo.ClearConnectionModelLocks(ctx, connID); err != nil {
		log.Warn("usage", "auto-release model locks failed", "conn", connID, "error", err)
		return
	}
	log.Info("usage", "auto-released quota locks after a healthy quota read", "provider", fresh.Provider, "conn", connID)
}
