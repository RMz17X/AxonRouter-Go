package handlers

import (
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

// Connection usage-status engine for the internal proxy report route
// (batch 7, AxonRouter parity): ports src/lib/usageStatus.ts's
// syncUsageStatus, src/lib/usageStatusSnapshots.ts, and
// src/lib/usageStatusPatches.ts.
//
// Storage mapping: upstream persists status patches to a per-connection
// hot-state record that read paths merge over the connection entity. In
// this codebase the status fields live in the connection's data blob —
// the same place the dashboard, chat rotation, and the batch-2/3 writers
// read and write them — so a "hot patch" here is a merge into that blob,
// and the merge's observable result matches upstream's merged read.

var legacyMirrorFields = map[string]bool{
	"testStatus": true, "lastTested": true, "lastErrorType": true,
	"lastErrorAt": true, "rateLimitedUntil": true, "errorCode": true,
	"lastError": true,
}

// isoNowJS renders the current time the way JS Date.toISOString does.
func isoNowJS() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// jsTruthy mirrors JS Boolean() for JSON-shaped values (objects and
// arrays are always truthy).
func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	default:
		return true
	}
}

// stripLegacyMirrorFields removes the fields upstream never persists to
// the status store.
func stripLegacyMirrorFields(updates map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range updates {
		if !legacyMirrorFields[k] {
			out[k] = v
		}
	}
	return out
}

// parseISOTime parses an ISO-8601 timestamp, returning false for values
// JS would read as an invalid date.
func parseISOTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// syncUsageStatus ports syncUsageStatus: merge a status patch into the
// connection's stored status, honouring the stale-update guard, the
// 7-day horizon cap on retry/reset timestamps, and the auth-recovery
// preservation rule. Returns the persisted patch (legacy fields
// stripped), or the minimal stale acknowledgement.
func syncUsageStatus(repo *db.Repo, flat map[string]any, updates map[string]any) map[string]any {
	id := recStrValue(flat, "id")
	if id == "" || updates == nil {
		return nil
	}

	// Stale guard: a patch whose lastCheckedAt is older than the stored
	// one must not overwrite fresher status.
	if currentRaw, ok := flat["lastCheckedAt"]; ok {
		if updateRaw, ok := updates["lastCheckedAt"]; ok {
			currentTime, currentOK := parseISOTime(currentRaw)
			updateTime, updateOK := parseISOTime(updateRaw)
			if currentOK && updateOK && currentTime.After(updateTime) {
				ack := map[string]any{"lastCheckedAt": currentRaw}
				if snapshot, present := flat["usageSnapshot"]; present {
					ack["usageSnapshot"] = snapshot
				} else {
					ack["usageSnapshot"] = nil
				}
				return ack
			}
		}
	}

	sanitized := stripLegacyMirrorFields(updates)

	maxAllowed := time.Now().Add(7 * 24 * time.Hour)
	for _, field := range []string{"nextRetryAt", "resetAt"} {
		if raw, ok := sanitized[field]; ok {
			if t, valid := parseISOTime(raw); valid && t.After(maxAllowed) {
				sanitized[field] = maxAllowed.UTC().Format("2006-01-02T15:04:05.000Z")
			}
		}
	}

	allowAuthRecovery := sanitized["allowAuthRecovery"] == true
	delete(sanitized, "allowAuthRecovery")
	isRecoveryToEligible := recStrValue(sanitized, "routingStatus") == "eligible" &&
		recStrValue(sanitized, "authState") == "ok"
	hasAuthInvalidBlock := recStrValue(flat, "reasonCode") == "auth_invalid" ||
		recStrValue(flat, "authState") == "invalid" ||
		recStrValue(flat, "routingStatus") == "blocked"

	if isRecoveryToEligible && hasAuthInvalidBlock && !allowAuthRecovery {
		preserved := map[string]any{
			"routingStatus": flat["routingStatus"],
			"authState":     flat["authState"],
			"reasonCode":    flat["reasonCode"],
			"reasonDetail":  flat["reasonDetail"],
			"nextRetryAt":   firstPresent(flat["nextRetryAt"], sanitized["nextRetryAt"]),
			"resetAt":       firstPresent(flat["resetAt"], sanitized["resetAt"]),
			"lastCheckedAt": firstPresent(sanitized["lastCheckedAt"], flat["lastCheckedAt"]),
			"usageSnapshot": firstPresent(sanitized["usageSnapshot"], flat["usageSnapshot"]),
			"validationUrl": flat["validationUrl"],
		}
		lastCheckedAt, _ := preserved["lastCheckedAt"].(string)
		if lastCheckedAt == "" {
			lastCheckedAt = isoNowJS()
		}
		hotPatch := ensureUsageSnapshot(flat, preserved, lastCheckedAt)
		hotPatch["lastCheckedAt"] = lastCheckedAt
		hotPatch["version"] = firstPresent(sanitized["version"], float64(time.Now().UnixMilli()))
		persistStatusPatch(repo, flat, hotPatch)
		return stripLegacyMirrorFields(hotPatch)
	}

	lastCheckedAt, _ := sanitized["lastCheckedAt"].(string)
	if lastCheckedAt == "" {
		if fallback, ok := updates["lastTested"].(string); ok && fallback != "" {
			lastCheckedAt = fallback
		} else {
			lastCheckedAt = isoNowJS()
		}
	}
	hotPatch := ensureUsageSnapshot(flat, sanitized, lastCheckedAt)
	hotPatch["lastCheckedAt"] = lastCheckedAt
	hotPatch["version"] = firstPresent(sanitized["version"], updates["version"], float64(time.Now().UnixMilli()))
	persistStatusPatch(repo, flat, hotPatch)
	return stripLegacyMirrorFields(hotPatch)
}

// firstPresent returns the first argument that is not nil.
func firstPresent(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// firstTruthy returns the first argument that is truthy in the JS sense.
func firstTruthy(values ...any) any {
	for _, v := range values {
		if jsTruthy(v) {
			return v
		}
	}
	return nil
}

// persistStatusPatch merges a status patch into the connection's data
// blob (this codebase's status store) and mirrors it into the flattened
// record so later steps in the same request see the new state.
func persistStatusPatch(repo *db.Repo, flat map[string]any, patch map[string]any) {
	id := recStrValue(flat, "id")
	if id == "" {
		return
	}
	conn, err := repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		return
	}
	data := map[string]any{}
	if strings.TrimSpace(conn.Data) != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(conn.Data), &parsed); err == nil && parsed != nil {
			data = parsed
		}
	}
	for k, v := range patch {
		data[k] = v
		flat[k] = v
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = repo.UpdateConnectionData(id, string(encoded))
}

// --- usage snapshots (usageStatusSnapshots.ts) ---------------------------

// parseSnapshotValue reads a stored usageSnapshot (JSON string or object)
// into an object, nil when unusable.
func parseSnapshotValue(v any) map[string]any {
	switch value := v.(type) {
	case map[string]any:
		return value
	case string:
		if value == "" {
			return nil
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return nil
		}
		return parsed
	}
	return nil
}

// buildCodexSyntheticSnapshot ports buildCodexSyntheticSnapshot.
func buildCodexSyntheticSnapshot(flat map[string]any, snapshot map[string]any, checkedAt string) map[string]any {
	previous := parseSnapshotValue(flat["usageSnapshot"])
	reasonDetail := recStrValue(snapshot, "message")
	if reasonDetail == "" {
		reasonDetail = recStrValue(flat, "reasonDetail")
	}

	var quotas map[string]any
	if q, ok := snapshot["quotas"].(map[string]any); ok {
		quotas = q
	} else if previous != nil {
		if q, ok := previous["quotas"].(map[string]any); ok {
			quotas = q
		}
	}
	if quotas == nil {
		quotas = map[string]any{}
	}

	provider := recStrValue(flat, "provider")
	if provider == "" {
		provider = "codex"
	}
	next := map[string]any{"provider": provider, "checkedAt": checkedAt}
	for k, v := range previous {
		next[k] = v
	}
	for k, v := range snapshot {
		next[k] = v
	}
	next["quotas"] = quotas

	if recStrValue(next, "message") == "" && reasonDetail != "" {
		next["message"] = reasonDetail
	}
	if !jsTruthy(next["resetAt"]) {
		if v := firstPresent(snapshot["resetAt"], flat["resetAt"]); jsTruthy(v) {
			next["resetAt"] = v
		}
	}
	if !jsTruthy(next["nextRetryAt"]) {
		if v := firstPresent(snapshot["nextRetryAt"], flat["nextRetryAt"]); jsTruthy(v) {
			next["nextRetryAt"] = v
		}
	}
	return next
}

// codexSnapshotInput builds the snapshot argument for
// buildCodexSyntheticSnapshot with JS || null semantics: falsy values
// are omitted rather than stored.
func codexSnapshotInput(message string, resetAt, nextRetryAt any) map[string]any {
	out := map[string]any{}
	if message != "" {
		out["message"] = message
	}
	if jsTruthy(resetAt) {
		out["resetAt"] = resetAt
	}
	if jsTruthy(nextRetryAt) {
		out["nextRetryAt"] = nextRetryAt
	}
	return out
}

// ensureUsageSnapshot ports ensureUsageSnapshot: when a patch carries no
// snapshot, synthesize one describing the status change.
func ensureUsageSnapshot(flat map[string]any, updates map[string]any, checkedAt string) map[string]any {
	if v, present := updates["usageSnapshot"]; present && v != nil {
		return updates
	}
	if checkedAt == "" {
		checkedAt = isoNowJS()
	}

	out := map[string]any{}
	for k, v := range updates {
		out[k] = v
	}

	if recStrValue(flat, "provider") == "codex" {
		synthetic := buildCodexSyntheticSnapshot(flat,
			codexSnapshotInput(recStrValue(updates, "reasonDetail"), updates["resetAt"], updates["nextRetryAt"]),
			checkedAt)
		encoded, _ := json.Marshal(synthetic)
		out["usageSnapshot"] = string(encoded)
		return out
	}

	synthetic := map[string]any{
		"provider":      flat["provider"],
		"checkedAt":     checkedAt,
		"message":       firstNonEmptyString(recStrValue(updates, "reasonDetail"), recStrValue(updates, "reasonCode"), "Status updated"),
		"routingStatus": updates["routingStatus"],
		"quotaState":    updates["quotaState"],
		"authState":     updates["authState"],
		"healthStatus":  updates["healthStatus"],
	}
	if v, ok := updates["resetAt"]; ok {
		synthetic["resetAt"] = v
	}
	if v, ok := updates["nextRetryAt"]; ok {
		synthetic["nextRetryAt"] = v
	}
	encoded, _ := json.Marshal(synthetic)
	out["usageSnapshot"] = string(encoded)
	return out
}

// firstNonEmptyString returns the first non-empty string.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- auth-blocked patches (usageStatusPatches.ts) -------------------------

var authBlockedPatterns = []string{
	"token invalid", "invalid token", "token expired", "refresh failed",
	"re-authorize", "reauthorize", "sign in again", "unauthorized",
	"unauthenticated", "revoked", "invalid grant", "invalid_client",
	"invalid_token", "oauth", "access token", "authentication",
}

// errorMessageText extracts the human message from a report error value:
// the string itself, or .message / .error / .cause.message of an object.
func errorMessageText(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case map[string]any:
		if s := recStrValue(value, "message"); s != "" {
			return s
		}
		if s := recStrValue(value, "error"); s != "" {
			return s
		}
		if cause, ok := value["cause"].(map[string]any); ok {
			return recStrValue(cause, "message")
		}
	}
	return ""
}

// errorCodeText extracts an error code from a report error value.
func errorCodeText(v any) string {
	if m, ok := v.(map[string]any); ok {
		return recStrValue(m, "code")
	}
	return ""
}

// isConfirmedAuthBlockedError ports isConfirmedAuthBlockedError.
func isConfirmedAuthBlockedError(message string, statusCode *float64) bool {
	if statusCode != nil && *statusCode == 401 {
		return true
	}
	if message == "" {
		return false
	}
	normalized := strings.ToLower(message)
	hasEvidence := false
	for _, pattern := range authBlockedPatterns {
		if strings.Contains(normalized, pattern) {
			hasEvidence = true
			break
		}
	}
	if statusCode != nil && *statusCode == 403 {
		return hasEvidence
	}
	return hasEvidence
}

// getConnectionAuthBlockedPatch ports getConnectionAuthBlockedPatch:
// when the error is confirmed auth-related, the connection is disabled
// with reason auth_invalid until the operator re-authorizes it.
func getConnectionAuthBlockedPatch(errorValue any, lastCheckedAt string, statusCode *float64) map[string]any {
	message := errorMessageText(errorValue)
	if !isConfirmedAuthBlockedError(message, statusCode) {
		return nil
	}
	reasonDetail := message
	if reasonDetail == "" {
		reasonDetail = "Provider error"
	}
	return map[string]any{
		"routingStatus": "disabled",
		"healthStatus":  "healthy",
		"quotaState":    "ok",
		"authState":     "invalid",
		"reasonCode":    "auth_invalid",
		"reasonDetail":  reasonDetail,
		"nextRetryAt":   nil,
		"resetAt":       nil,
		"lastCheckedAt": lastCheckedAt,
	}
}

// --- error classification predicates --------------------------------------

var transientUpstreamTimeoutPatterns = []string{
	"upstream timed out after", "stream idle timed out after",
	"etimedout", "econnaborted",
}

var upstreamProcessingErrorPatterns = []string{
	"error occurred", "request id", "internal error",
}

// isTransientUpstreamTimeoutError ports isTransientUpstreamTimeoutError.
func isTransientUpstreamTimeoutError(errorValue any, statusCode *float64, errorCode string) bool {
	message := errorMessageText(errorValue)
	normalizedMessage := strings.ToLower(message)
	normalizedCode := strings.ToUpper(errorCode)
	if normalizedCode == "" {
		normalizedCode = strings.ToUpper(errorCodeText(errorValue))
	}
	if normalizedCode == "UPSTREAM_TIMEOUT" || normalizedCode == "STREAM_IDLE_TIMEOUT" {
		return true
	}
	if statusCode != nil && *statusCode != 502 && *statusCode != 504 {
		return false
	}
	for _, pattern := range transientUpstreamTimeoutPatterns {
		if strings.Contains(normalizedMessage, pattern) {
			return true
		}
	}
	return false
}

// isUpstreamProcessingError ports isUpstreamProcessingError.
func isUpstreamProcessingError(statusCode *float64, errorMessage any) bool {
	if statusCode == nil || *statusCode < 500 || *statusCode > 599 {
		return false
	}
	message := errorMessageText(errorMessage)
	if message == "" {
		return false
	}
	normalized := strings.ToLower(message)
	for _, pattern := range upstreamProcessingErrorPatterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

// --- Codex live quota signal ------------------------------------------------

var codexLiveQuotaPatterns = []string{
	"exceeded your current quota", "quota exceeded", "quota exhausted",
	"insufficient quota", "billing hard limit", "hard limit reached",
	"usage_limit_reached", "usage limit reached",
	"usage limit has been reached", "weekly quota exhausted",
}

type codexLiveQuotaSignal struct {
	ReasonCode   string
	ReasonDetail string
	ErrorCode    string
	ResetAt      *string
}

// getCodexLiveQuotaSignal ports getCodexLiveQuotaSignal: a 429 from
// Codex whose text confirms quota exhaustion, with any reset time the
// error payload carries.
func getCodexLiveQuotaSignal(flat map[string]any, statusCode *float64, errorText string, errorCode string) *codexLiveQuotaSignal {
	if recStrValue(flat, "provider") != "codex" {
		return nil
	}
	if statusCode == nil || *statusCode != 429 {
		return nil
	}

	parsedErrorType := ""
	var parsedReset any
	if errorText != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(errorText), &parsed); err == nil && parsed != nil {
			errObj, _ := parsed["error"].(map[string]any)
			parsedErrorType = firstNonEmptyString(
				recStrValue(errObj, "type"), recStrValue(parsed, "type"), recStrValue(parsed, "code"))
			parsedReset = firstTruthy(
				errObj["resets_at"], errObj["reset_at"], parsed["resets_at"], parsed["reset_at"])
		}
	}

	parts := []string{}
	for _, part := range []string{errorText, errorCode, parsedErrorType} {
		if part != "" {
			parts = append(parts, strings.ToLower(part))
		}
	}
	normalized := strings.Join(parts, " ")
	if normalized == "" {
		return nil
	}
	matched := false
	for _, pattern := range codexLiveQuotaPatterns {
		if strings.Contains(normalized, pattern) {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}

	// Upstream computes Number(parsedResetAt) unconditionally: a missing
	// reset is Number(null) = 0, which is finite, so the signal carries
	// the epoch as resetAt rather than null. Ported as-is.
	var resetAt *string
	epoch := "1970-01-01T00:00:00.000Z"
	switch value := parsedReset.(type) {
	case nil:
		resetAt = &epoch
	case float64:
		iso := epochMillisISO(value)
		resetAt = &iso
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			iso := epochMillisISO(f)
			resetAt = &iso
		} else if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
			iso := t.UTC().Format("2006-01-02T15:04:05.000Z")
			resetAt = &iso
		}
	}

	return &codexLiveQuotaSignal{
		ReasonCode:   "quota_exhausted",
		ReasonDetail: "Codex quota exhausted",
		ErrorCode:    "codex_live_quota_exhausted",
		ResetAt:      resetAt,
	}
}

// jsNumberString renders a float the way JS String(number) would for the
// values this code handles (used only for message formatting).
func jsNumberString(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// epochMillisISO converts a JS epoch value (seconds when < 1e12, else
// milliseconds) to an ISO timestamp.
func epochMillisISO(value float64) string {
	ms := value
	if ms < 1e12 {
		ms *= 1000
	}
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}
