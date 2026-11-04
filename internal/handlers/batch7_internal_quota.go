package handlers

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	json "encoding/json/v2"
)

// Quota-signal interpreters for the internal proxy report route
// (batch 7, AxonRouter parity): ports getUsageStatusUpdates and its
// provider interpreters from src/lib/usageStatus.ts. These translate a
// canonical usage payload (or a live quota signal) into the status patch
// syncUsageStatus persists.
//
// One mechanical divergence: JS iterates quota objects in payload
// order; Go maps have no order, so quota entries are visited in sorted
// key order. Only the "first match wins" picks (an exhausted quota's
// name/reset) can differ, and only when several quotas are exhausted at
// once.

// usageStatusOptions carries getUsageStatusUpdates' options.
type usageStatusOptions struct {
	observedAt string
	liveSignal *codexLiveQuotaSignal
}

// getHealthyUsageStatusUpdates ports the base healthy patch. Note its
// lastCheckedAt is the moment of computation, not the observed time —
// several branches override it with the observed time, several keep it.
func getHealthyUsageStatusUpdates(usage map[string]any) map[string]any {
	encoded, _ := json.Marshal(usage)
	return map[string]any{
		"routingStatus": "eligible",
		"healthStatus":  "healthy",
		"quotaState":    "ok",
		"authState":     "ok",
		"reasonCode":    nil,
		"reasonDetail":  nil,
		"lastCheckedAt": isoNowJS(),
		"usageSnapshot": string(encoded),
		"resetAt":       nil,
		"nextRetryAt":   nil,
	}
}

var (
	codexUnavailableMessageRe = regexp.MustCompile(`^Codex connected\. Usage API temporarily unavailable \((\d{3})\)\.?$`)
	codexStatusInMessageRe    = regexp.MustCompile(`\((\d{3})\)`)
)

// getUsageStatusUpdates ports getUsageStatusUpdates.
func getUsageStatusUpdates(flat map[string]any, usage map[string]any, options usageStatusOptions) map[string]any {
	if usage == nil {
		usage = map[string]any{}
	}
	base := getHealthyUsageStatusUpdates(usage)
	nowIso := options.observedAt
	if nowIso == "" {
		nowIso = isoNowJS()
	}
	usageMessage := recStrValue(usage, "message")
	provider := recStrValue(flat, "provider")

	if provider == "codex" && codexUnavailableMessageRe.MatchString(usageMessage) {
		if routing := recStrValue(flat, "routingStatus"); routing == "" || routing == "eligible" {
			out := copyStatusMap(base)
			out["usageSnapshot"] = string(mustJSON(usage))
			out["lastCheckedAt"] = nowIso
			return out
		}
		return map[string]any{
			"usageSnapshot": string(mustJSON(usage)),
			"lastCheckedAt": nowIso,
		}
	}

	var authStatusCode *float64
	if provider == "codex" {
		if m := codexStatusInMessageRe.FindStringSubmatch(usageMessage); m != nil {
			if code, err := strconv.Atoi(m[1]); err == nil {
				asFloat := float64(code)
				authStatusCode = &asFloat
			}
		}
	}
	if authPatch := getConnectionAuthBlockedPatch(usageMessage, nowIso, authStatusCode); authPatch != nil {
		out := copyStatusMap(base)
		for k, v := range authPatch {
			out[k] = v
		}
		out["usageSnapshot"] = string(mustJSON(usage))
		return out
	}

	if options.liveSignal != nil && provider == "codex" {
		signal := options.liveSignal
		out := copyStatusMap(base)
		out["routingStatus"] = "exhausted"
		out["healthStatus"] = "degraded"
		out["quotaState"] = "exhausted"
		out["reasonCode"] = firstNonEmptyString(signal.ReasonCode, "quota_exhausted")
		out["reasonDetail"] = firstNonEmptyString(signal.ReasonDetail, "Codex quota exhausted")
		var resetAt any
		if signal.ResetAt != nil {
			resetAt = *signal.ResetAt
		}
		out["resetAt"] = resetAt
		out["nextRetryAt"] = resetAt
		synthetic := buildCodexSyntheticSnapshot(flat,
			codexSnapshotInput(firstNonEmptyString(signal.ReasonDetail, "Codex quota exhausted"), resetAt, resetAt),
			nowIso)
		out["usageSnapshot"] = string(mustJSON(synthetic))
		return out
	}

	if provider == "kiro" || provider == "amazon-q" {
		label := "Kiro"
		if provider == "amazon-q" {
			label = "Amazon Q"
		}
		if signal := getKiroQuotaSignal(flat, usage); signal != nil {
			out := copyStatusMap(base)
			out["routingStatus"] = "exhausted"
			out["healthStatus"] = "degraded"
			out["quotaState"] = "exhausted"
			if signal.kind == "exhausted" {
				out["reasonCode"] = "quota_exhausted"
				out["reasonDetail"] = label + " quota exhausted"
			} else {
				out["reasonCode"] = "quota_threshold"
				out["reasonDetail"] = label + " remaining quota is at or below " + jsNumberString(signal.minimumPercent) + "%"
			}
			out["resetAt"] = signal.resetAt
			out["nextRetryAt"] = signal.resetAt
			out["usageSnapshot"] = string(mustJSON(usage))
			return out
		}
	}

	if provider == "antigravity" {
		families, allExhausted, someExhausted := getAntigravityFamilyQuotaState(usage)
		enriched := map[string]any{}
		for k, v := range usage {
			enriched[k] = v
		}
		enriched["_familyQuotas"] = families

		out := copyStatusMap(base)
		out["usageSnapshot"] = string(mustJSON(enriched))
		if allExhausted {
			out["routingStatus"] = "exhausted"
			out["healthStatus"] = "degraded"
			out["quotaState"] = "exhausted"
			out["reasonCode"] = "quota_exhausted"
			out["reasonDetail"] = "All models exhausted across all families"
			return out
		}
		if someExhausted {
			names := []string{}
			for _, family := range families {
				if family.Exhausted > 0 {
					names = append(names, family.Name+" exhausted")
				}
			}
			out["reasonDetail"] = strings.Join(names, "; ")
			return out
		}
		return out
	}

	if provider != "codex" {
		return base
	}

	if quotaName, resetAt, exhausted := getCodexExhaustedQuota(usage); exhausted || usage["limitReached"] == true {
		reasonDetail := "Codex quota exhausted"
		if quotaName == "session" {
			reasonDetail = "Codex session quota exhausted"
		} else if quotaName == "weekly" {
			reasonDetail = "Codex weekly quota exhausted"
		}
		out := copyStatusMap(base)
		out["routingStatus"] = "exhausted"
		out["healthStatus"] = "degraded"
		out["quotaState"] = "exhausted"
		out["reasonCode"] = "quota_exhausted"
		out["reasonDetail"] = reasonDetail
		out["resetAt"] = resetAt
		out["nextRetryAt"] = resetAt
		out["usageSnapshot"] = string(mustJSON(usage))
		return out
	}

	minimumPercent := getConfiguredMinimumRemainingQuotaPercent(flat)
	quotas, _ := usage["quotas"].(map[string]any)
	for _, quotaName := range sortedKeys(quotas) {
		if shouldIgnoreKiroQuotaForRouting(quotaName) {
			continue
		}
		quota, ok := quotas[quotaName].(map[string]any)
		if !ok {
			continue
		}
		remainingPercent, valid := getSafeRemainingPercent(quota)
		if !valid || remainingPercent > minimumPercent {
			continue
		}
		out := copyStatusMap(base)
		out["routingStatus"] = "exhausted"
		out["healthStatus"] = "degraded"
		out["quotaState"] = "exhausted"
		out["reasonCode"] = "quota_threshold"
		out["reasonDetail"] = "Remaining quota is at or below " + jsNumberString(minimumPercent) + "%"
		out["resetAt"] = quota["resetAt"]
		out["nextRetryAt"] = quota["resetAt"]
		out["usageSnapshot"] = string(mustJSON(usage))
		return out
	}

	return base
}

// copyStatusMap shallow-copies a status patch.
func copyStatusMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// mustJSON marshals a value, returning "{}" on failure.
func mustJSON(v any) []byte {
	encoded, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// sortedKeys returns a map's keys in sorted order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// getFiniteNumber ports getFiniteNumber: JSON numbers pass through,
// numeric strings are parsed, everything else is absent.
func getFiniteNumber(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case string:
		if strings.TrimSpace(value) == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// jsonNumber reads a raw JSON number (no string coercion), matching
// Number.isFinite / typeof checks in the interpreters.
func jsonNumber(v any) (float64, bool) {
	if f, ok := v.(float64); ok {
		return f, true
	}
	return 0, false
}

// getSafeRemainingPercent ports getSafeRemainingPercent.
func getSafeRemainingPercent(quota map[string]any) (float64, bool) {
	if quota == nil {
		return 0, false
	}
	if explicit, ok := getFiniteNumber(quota["remainingPercentage"]); ok && explicit >= 0 && explicit <= 100 {
		return explicit, true
	}
	total, hasTotal := getFiniteNumber(quota["total"])
	used, hasUsed := getFiniteNumber(quota["used"])
	remaining, hasRemaining := getFiniteNumber(quota["remaining"])

	if hasTotal && total > 0 && hasRemaining && remaining >= 0 && remaining <= total {
		percent := remaining / total * 100
		if percent >= 0 && percent <= 100 {
			return percent, true
		}
		return 0, false
	}
	if !hasTotal || total <= 0 || !hasUsed || used < 0 {
		return 0, false
	}
	percent := (total - used) / total * 100
	if percent >= 0 && percent <= 100 {
		return percent, true
	}
	return 0, false
}

// getConfiguredMinimumRemainingQuotaPercent ports the function of the
// same name (the report path never passes the global option, so only
// the connection's own threshold applies).
func getConfiguredMinimumRemainingQuotaPercent(flat map[string]any) float64 {
	psd, _ := flat["providerSpecificData"].(map[string]any)
	raw := psd["minimumRemainingQuotaPercent"]
	if raw == nil || raw == "" {
		return 10
	}
	parsed, ok := getFiniteNumber(raw)
	if !ok {
		return 10
	}
	if parsed < 0 {
		return 0
	}
	if parsed > 100 {
		return 100
	}
	return parsed
}

// shouldIgnoreKiroQuotaForRouting ports the *_freetrial exclusion.
func shouldIgnoreKiroQuotaForRouting(quotaName string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(quotaName)), "_freetrial")
}

// kiroQuotaSignal is getKiroQuotaSignal's result.
type kiroQuotaSignal struct {
	kind           string // "exhausted" | "threshold"
	resetAt        any
	minimumPercent float64
}

// getKiroQuotaSignal ports getKiroQuotaSignal.
func getKiroQuotaSignal(flat map[string]any, usage map[string]any) *kiroQuotaSignal {
	quotas, hasQuotas := usage["quotas"].(map[string]any)
	limitReached := usage["limitReached"] == true || usage["revoked"] == true
	if !hasQuotas {
		if limitReached {
			return &kiroQuotaSignal{kind: "exhausted"}
		}
		return nil
	}

	minimumPercent := getConfiguredMinimumRemainingQuotaPercent(flat)
	for _, quotaName := range sortedKeys(quotas) {
		if shouldIgnoreKiroQuotaForRouting(quotaName) {
			continue
		}
		quota, ok := quotas[quotaName].(map[string]any)
		if !ok {
			continue
		}
		remaining, hasRemaining := jsonNumber(quota["remaining"])
		total, hasTotal := jsonNumber(quota["total"])
		used, hasUsed := jsonNumber(quota["used"])

		hasExplicitExhaustion := hasRemaining && remaining <= 0
		hasUsedAll := hasTotal && total > 0 && hasUsed && used >= total
		if hasExplicitExhaustion || hasUsedAll || limitReached {
			return &kiroQuotaSignal{kind: "exhausted", resetAt: quota["resetAt"]}
		}

		remainingPercent, valid := getSafeRemainingPercent(quota)
		if !valid {
			continue
		}
		if remainingPercent <= minimumPercent {
			return &kiroQuotaSignal{
				kind:           "threshold",
				resetAt:        quota["resetAt"],
				minimumPercent: minimumPercent,
			}
		}
	}

	if limitReached {
		return &kiroQuotaSignal{kind: "exhausted"}
	}
	return nil
}

// getCodexExhaustedQuota ports getCodexExhaustedQuota.
func getCodexExhaustedQuota(usage map[string]any) (string, any, bool) {
	quotas, ok := usage["quotas"].(map[string]any)
	if !ok {
		return "", nil, false
	}
	for _, quotaName := range sortedKeys(quotas) {
		quota, ok := quotas[quotaName].(map[string]any)
		if !ok {
			continue
		}
		remaining, hasRemaining := getFiniteNumber(quota["remaining"])
		used, hasUsed := getFiniteNumber(quota["used"])
		total, hasTotal := getFiniteNumber(quota["total"])
		exhaustedRemaining := hasRemaining && remaining <= 0
		exhaustedTotal := hasTotal && total > 0 && hasUsed && used >= total
		if exhaustedRemaining || exhaustedTotal {
			return quotaName, quota["resetAt"], true
		}
	}
	return "", nil, false
}

// --- Antigravity family quotas ------------------------------------------------

type antigravityFamily struct {
	Name                string  `json:"name"`
	Models              int     `json:"models"`
	Exhausted           int     `json:"exhausted"`
	RemainingPercentage float64 `json:"remainingPercentage"`
	ResetAt             *string `json:"resetAt"`
}

var antigravityModelFamilies = map[string]string{
	"claude-opus-4-6-thinking": "Claude",
	"claude-sonnet-4-6":        "Claude",
	"gemini-3.5-flash-low":     "Gemini",
	"gemini-3.5-flash-medium":  "Gemini",
	"gemini-3.5-flash-high":    "Gemini",
	"gemini-3.1-pro-low":       "Gemini",
	"gemini-3.1-pro-high":      "Gemini",
	"gpt-oss-120b-medium":      "Other",
}

// antigravityFamilyForModel ports getAntigravityModelFamily.
func antigravityFamilyForModel(modelKey string) string {
	if family, ok := antigravityModelFamilies[modelKey]; ok {
		return family
	}
	if strings.HasPrefix(modelKey, "claude-") || strings.HasPrefix(modelKey, "anthropic/claude-") {
		return "Claude"
	}
	if strings.HasPrefix(modelKey, "gemini-") {
		return "Gemini"
	}
	return "Other"
}

// getAntigravityFamilyQuotaState ports getAntigravityFamilyQuotaState.
func getAntigravityFamilyQuotaState(usage map[string]any) ([]antigravityFamily, bool, bool) {
	quotas, ok := usage["quotas"].(map[string]any)
	if !ok {
		return []antigravityFamily{}, false, false
	}

	type modelQuota struct {
		remaining float64
		resetAt   *string
	}
	grouped := map[string][]modelQuota{}
	for _, modelKey := range sortedKeys(quotas) {
		quota, ok := quotas[modelKey].(map[string]any)
		if !ok {
			continue
		}
		remaining := 100.0
		if rp, isNumber := jsonNumber(quota["remainingPercentage"]); isNumber && rp >= 0 && rp <= 100 {
			remaining = rp
		}
		var resetAt *string
		if s, isString := quota["resetAt"].(string); isString {
			resetAt = &s
		}
		family := antigravityFamilyForModel(modelKey)
		grouped[family] = append(grouped[family], modelQuota{remaining: remaining, resetAt: resetAt})
	}
	if len(grouped) == 0 {
		return []antigravityFamily{}, false, false
	}

	families := []antigravityFamily{}
	exhaustedFamilies := 0
	for _, familyName := range []string{"Claude", "Gemini", "Other"} {
		models := grouped[familyName]
		if len(models) == 0 {
			continue
		}
		exhausted := 0
		var earliest *string
		bestRemaining := 0.0
		seenPositive := false
		for _, model := range models {
			if model.remaining <= 0 {
				exhausted++
			} else if !seenPositive || model.remaining < bestRemaining {
				bestRemaining = model.remaining
				seenPositive = true
			}
			if model.resetAt != nil && (earliest == nil || *model.resetAt < *earliest) {
				earliest = model.resetAt
			}
		}
		allExhausted := exhausted == len(models)
		remaining := bestRemaining
		if allExhausted {
			remaining = 0
			exhaustedFamilies++
		}
		families = append(families, antigravityFamily{
			Name:                familyName,
			Models:              len(models),
			Exhausted:           exhausted,
			RemainingPercentage: remaining,
			ResetAt:             earliest,
		})
	}

	return families, len(families) > 0 && exhaustedFamilies == len(families), exhaustedFamilies > 0
}
