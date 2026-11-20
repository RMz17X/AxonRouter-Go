package chat

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/proxy/poolstate"
)

const (
	// A strike pair is counted per proxy pool and per 24h window. After the
	// window the ledger is forgotten, so an unrelated request from a healthy
	// egress cannot inherit a stale strike (docs/BYPASS-429.md).
	opencodeEgressStrikeReset = 24 * time.Hour
	opencodeEgressParkCap     = 24 * time.Hour
	opencodeEgressMaxStrike   = 6 // 0-based ladder indices 0..5; 6+ stay at the 7200s ceiling
	// Bounded state: proxy pool ids are operator-configured, but a long-lived
	// process must not accumulate entries forever (AGENTS.md §4.G).
	opencodeEgressStateCap = 512
	// Bounded rotation across distinct proxy pools in a single request.
	opencodeEgressRotateMaxDefault = 4
	// The only gateway-generated response header safe to publish verbatim.
	headerXOpenCodeGate = "X-OpenCode-Gate"
)

// Error types emitted by opencodeClosureError. They are a new payload:
// isOpencodeFreeTierRefusal only recognises the upstream's original type
// tokens, so a classified refusal would otherwise lose its gate verdict on
// the way to the client.
const (
	opencodeClassifiedRegionBlocked   = "free_tier_region_blocked"
	opencodeClassifiedQuotaExhausted  = "free_tier_quota_exhausted"
	opencodeClassifiedUpgradeRequired = "free_tier_upgrade_required"
	opencodeClassifiedAbuseDetected   = "free_tier_abuse_detected"
	opencodeClassifiedUnavailable     = "free_tier_unavailable"
)

type opencodeEgressEntry struct {
	strike      int
	last429     time.Time
	parkedUntil time.Time
}

var (
	opencodeEgressMu      sync.Mutex
	opencodeEgressEntries = make(map[string]*opencodeEgressEntry, 32)
)

// envPositiveInt reads a positive integer override. Zero, negative and
// malformed values fall back to the default instead of disabling the guard.
func envPositiveInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// opencodeEgressRotateMax bounds how many distinct proxy pools one request may
// try after an egress refusal. A fresh account on an already-parked exit does
// not change the gate verdict, so traversing every pool only burns budget.
func opencodeEgressRotateMax() int {
	max := envPositiveInt("EGRESS_ROTATE_MAX", opencodeEgressRotateMaxDefault)
	if max > 16 {
		return 16
	}
	return max
}

func opencodeEgressKey(provider, poolID string) string {
	return providers.ResolveAlias(provider) + "|" + poolID
}

// isOpencodeEgressRefusal is the strike-worthy subset: a free-tier 429
// from an opencode lane. A 403 is a shape refusal (docs/BYPASS-429.md),
// and a 429 without the free-usage token is a genuine rate limit; neither
// says the egress is parked, so neither may strike the pool.
func isOpencodeEgressRefusal(provider string, ue *upstreamError) bool {
	if ue == nil || ue.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return isOpencodeFreeTierRefusal(providers.ResolveAlias(provider), ue)
}

// opencodeParkSeconds follows docs/BYPASS-429.md: strike 0 is the configurable
// 60s first refusal, then 600/1200/1800/2400s, with a 7200s ceiling from
// strike 5 onward. Env overrides apply only to strike 0.
func opencodeParkSeconds(strike int) int {
	if strike <= 0 {
		return envPositiveInt("OPENCODE_EGRESS_PARK_SEC", 60)
	}
	switch strike {
	case 1:
		return 600
	case 2:
		return 1200
	case 3:
		return 1800
	case 4:
		return 2400
	default:
		return 7200
	}
}

// parkOpenCodeEgress records one 429 strike against the pool that actually
// served the attempt and marks that pool unfit until the park expires. A 403
// gate shape is reported to the client but never strikes the pool.
func parkOpenCodeEgress(provider, poolID string, ue *upstreamError) (strike int, parked time.Duration) {
	if poolID == "" || !isOpencodeEgressRefusal(provider, ue) {
		return 0, 0
	}
	now := time.Now()
	key := opencodeEgressKey(provider, poolID)

	opencodeEgressMu.Lock()
	entry := opencodeEgressEntries[key]
	if entry == nil {
		if len(opencodeEgressEntries) >= opencodeEgressStateCap {
			evictOldestOpenCodeEgressEntry()
		}
		entry = &opencodeEgressEntry{}
		opencodeEgressEntries[key] = entry
	}
	if now.Sub(entry.last429) >= opencodeEgressStrikeReset {
		entry.strike = 0
	}
	entry.strike++
	entry.last429 = now
	strike = entry.strike
	if strike > opencodeEgressMaxStrike {
		strike = opencodeEgressMaxStrike
		entry.strike = strike
	}
	// entry.strike counts refusals (1-based); the duration ladder is 0-based.
	parked = time.Duration(opencodeParkSeconds(strike-1)) * time.Second
	if parked > opencodeEgressParkCap {
		parked = opencodeEgressParkCap
	}
	until := now.Add(parked)
	entry.parkedUntil = until
	opencodeEgressMu.Unlock()

	reason := fmt.Sprintf("opencode free-tier 429 strike %d", strike)
	// Mark both the wire provider and its canonical id: callers may pass either
	// "oc" or "opencode" into smart.IsUnfit, and each scope is checked exactly.
	scopes := []string{provider + "::*"}
	if canonical := providers.ResolveAlias(provider); canonical != provider {
		scopes = append(scopes, canonical+"::*")
	}
	for _, scope := range scopes {
		poolstate.MarkUnfit(poolID, scope, until.UnixMilli(), reason)
	}
	return strike, parked
}

// evictOldestOpenCodeEgressEntry is called with opencodeEgressMu held.
func evictOldestOpenCodeEgressEntry() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range opencodeEgressEntries {
		if oldestKey == "" || entry.last429.Before(oldest) {
			oldestKey, oldest = key, entry.last429
		}
	}
	delete(opencodeEgressEntries, oldestKey)
}

// opencodeGateVerdict maps a classified refusal to a short token published in
// X-OpenCode-Gate so a caller can tell a request-shape problem from an
// egress-level one without parsing the body. A version floor is not a gate
// verdict and therefore publishes nothing.
func opencodeGateVerdict(detail *opencodeRefusalDetail, ue *upstreamError) string {
	if detail == nil {
		return ""
	}
	switch detail.ErrorType {
	case opencodeClassifiedAbuseDetected:
		return "ip-abuse"
	case opencodeClassifiedQuotaExhausted:
		return "ip-throttled"
	case opencodeClassifiedUpgradeRequired:
		return ""
	case opencodeClassifiedRegionBlocked, opencodeClassifiedUnavailable:
		return "shape-rejected"
	}
	if ue != nil && ue.StatusCode == http.StatusTooManyRequests {
		return "ip-throttled"
	}
	if ue != nil && ue.StatusCode == http.StatusForbidden {
		return "shape-rejected"
	}
	return ""
}

// isClassifiedOpencodeRefusal recognises the structured body produced by
// opencodeClosureError. The original detector matches only the upstream's
// FreeTierError/FreeUsageLimitError tokens, so a 503/426 classified answer
// would reach the writer with no gate header at all.
func isClassifiedOpencodeRefusal(provider string, ue *upstreamError) bool {
	if ue == nil {
		return false
	}
	switch providers.ResolveAlias(provider) {
	case "opencode", "opencode-zen", "opencode-go":
	default:
		return false
	}
	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(ue.Body, &payload) != nil {
		return false
	}
	switch payload.Error.Type {
	case opencodeClassifiedRegionBlocked,
		opencodeClassifiedQuotaExhausted,
		opencodeClassifiedUpgradeRequired,
		opencodeClassifiedAbuseDetected,
		opencodeClassifiedUnavailable:
		return true
	default:
		return false
	}
}

// publishOpenCodeGateVerdict exposes a compact refusal token to the caller.
// Only opencode's own free-tier answers get a verdict: a transport error or a
// model refusal from another provider says nothing about this gate.
func publishOpenCodeGateVerdict(w http.ResponseWriter, provider string, err error) {
	canonical := providers.ResolveAlias(provider)
	var ue *upstreamError
	if !errors.As(err, &ue) {
		return
	}
	if !isOpencodeFreeTierRefusal(canonical, ue) && !isClassifiedOpencodeRefusal(canonical, ue) {
		return
	}
	verdict := opencodeGateVerdict(nil, ue)
	if detail := classifyOpencodeFreeTierRefusal(canonical, ue.Body); detail.ErrorType != "" {
		verdict = opencodeGateVerdict(&detail, ue)
	}
	if verdict == "" {
		return
	}
	if ue.Header == nil {
		ue.Header = make(http.Header)
	}
	ue.Header.Set(headerXOpenCodeGate, verdict)
	if committed, ok := w.(interface{ IsCommitted() bool }); ok && committed.IsCommitted() {
		return
	}
	w.Header().Set(headerXOpenCodeGate, verdict)
}
