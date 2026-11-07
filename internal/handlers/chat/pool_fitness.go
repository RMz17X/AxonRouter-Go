package chat

import (
	"context"
	"errors"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/proxy/poolstate"
	"axonrouter/proxy/internal/proxy/smart"
)

// attemptPoolID resolves the proxy pool that served one attempt: the pool
// the per-attempt client resolution actually bound (ResolvedProxyPoolID),
// falling back to the connection's configured pool. It returns "" when the
// attempt used no pool (direct egress or an explicit no-pool binding), in
// which case its failures say nothing about any pool.
func attemptPoolID(connData *ConnectionData) string {
	if connData == nil {
		return ""
	}
	poolID := connData.ResolvedProxyPoolID
	if poolID == "" {
		poolID = connData.ProxyPoolID
	}
	if poolID == "__none__" || poolID == "direct" {
		return ""
	}
	return poolID
}

// classifyPoolFailure folds one failed attempt's verdict about its egress
// pool into the pool fitness tracker: a location refusal
// (FAILED_PRECONDITION) locks the pool for 24h, and a transport failure
// records a progressive-cooldown failure. It returns the pool it
// penalized ("" when the failure says nothing about the pool) so the
// fallback loop can exclude that pool from its remaining attempts.
//
// The multi-connection fallback loop and the pinned-connection path both
// classify through here. The pinned path matters: it skips the loop
// entirely, so before this helper existed its failures — every idle-probe
// ping pins its connection — never reached the tracker, and a stalling
// pool serving pinned traffic kept a clean fitness record and stayed in
// rotation no matter how many attempts it hung.
func (h *ChatHandler) classifyPoolFailure(ctx context.Context, provider, model, connID string, connData *ConnectionData, err error) string {
	poolID := attemptPoolID(connData)
	if poolID == "" || err == nil {
		return ""
	}
	var ue *upstreamError
	errors.As(err, &ue)
	isLocErr := isProxyLocationError(err, func() []byte {
		if ue != nil {
			return ue.Body
		}
		return nil
	}())
	if isLocErr {
		smart.DefaultTracker.RecordFailureWithCooldown(provider, poolID, 24*time.Hour, "FAILED_PRECONDITION: user location not supported")
		poolstate.MarkUnfit(poolID, provider+"::*", time.Now().Add(24*time.Hour).UnixMilli(), "FAILED_PRECONDITION: user location not supported")
		log.WarnCtx(ctx, "fallback", "proxy pool rejected by location policy, locked for 24h via smartfit",
			"conn", connID, "provider", provider, "model", model, "pool", poolID)
		return poolID
	}
	statusCode := 0
	if ue != nil {
		statusCode = ue.StatusCode
	}
	if isProxyTransportError(err, statusCode) {
		smart.DefaultTracker.RecordFailure(provider, poolID, err)
		log.WarnCtx(ctx, "fallback", "proxy transport failed, penalizing proxy fitness",
			"conn", connID, "provider", provider, "model", model, "pool", poolID, "status", statusCode, "error", err)
		return poolID
	}
	return ""
}
