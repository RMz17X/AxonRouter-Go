package dashboard

import (
	"fmt"
	"strings"
)

// Credential import pipeline (batch 7, AxonRouter parity): a port of
// src/lib/credentials/{normalizer,validator,matcher,importer}.ts.
// Records are normalized from the many shapes a backup can take,
// sanitized to the canonical field set, checked for duplicates, matched
// against existing connections, and upserted: a matched connection whose
// stored credential still probes valid is preserved untouched, a matched
// but invalid one is updated, anything else is created. Imports never
// delete.

type credImportError struct {
	code    string
	message string
}

func invalidRecordError(message string) *credImportError {
	return &credImportError{code: "INVALID_RECORD", message: message}
}

// --- record helpers -----------------------------------------------------

func toObject(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// pickValue returns the first value that is neither nil (undefined) nor
// JSON null, mirroring the normalizer's pickValue.
func pickValue(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// nestedPick reads key from a nested object that may itself be absent.
func nestedPick(obj map[string]any, key string) any {
	if obj == nil {
		return nil
	}
	return obj[key]
}

// compactObject drops nil values, mirroring compactObject.
func compactObject(input map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range input {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

// recStr reads a string field, "" when absent or not a string.
func recStr(record map[string]any, key string) string {
	if s, ok := record[key].(string); ok {
		return s
	}
	return ""
}

// --- normalizer -----------------------------------------------------------

var legacyStatusFields = map[string]bool{
	"testStatus": true, "test_status": true,
	"lastTested": true, "last_tested": true,
	"lastError": true, "last_error": true,
	"lastErrorAt": true, "last_error_at": true,
	"lastErrorType": true, "last_error_type": true,
	"rateLimitedUntil": true, "rate_limited_until": true,
	"errorCode": true, "error_code": true,
}

// extractInputRecords mirrors extractInputRecords: a bare array payload,
// or the first array found under the known wrapper keys.
func extractInputRecords(payload any) []any {
	if arr, ok := payload.([]any); ok {
		return arr
	}
	obj := toObject(payload)
	if obj == nil {
		return nil
	}
	for _, key := range []string{"credentials", "entries", "items", "connections", "providerConnections", "data"} {
		if arr, ok := obj[key].([]any); ok {
			return arr
		}
	}
	return nil
}

// normalizeInputRecord mirrors normalizeInputRecord: fold snake_case and
// nested credentials/secrets/token/auth/identity/meta shapes into the
// canonical field set. Returns nil for non-object items. Fields whose
// picked value is absent are simply not set (JS: undefined).
func normalizeInputRecord(raw any) map[string]any {
	record := toObject(raw)
	if record == nil {
		return nil
	}
	cleaned := map[string]any{}
	for k, v := range record {
		if !legacyStatusFields[k] {
			cleaned[k] = v
		}
	}

	credentials := toObject(cleaned["credentials"])
	secrets := toObject(cleaned["secrets"])
	token := toObject(cleaned["token"])
	auth := toObject(cleaned["auth"])
	identity := toObject(cleaned["identity"])
	meta := toObject(cleaned["meta"])
	metadata := toObject(cleaned["metadata"])

	provider := pickValue(cleaned["provider"], cleaned["providerId"], cleaned["provider_id"])

	mergedPSD := map[string]any{}
	for k, v := range compactObject(toObject(cleaned["providerSpecificData"])) {
		mergedPSD[k] = v
	}
	for k, v := range compactObject(toObject(cleaned["provider_specific_data"])) {
		mergedPSD[k] = v
	}
	for k, v := range compactObject(metadata) {
		mergedPSD[k] = v
	}
	for k, v := range compactObject(meta) {
		mergedPSD[k] = v
	}

	normalized := map[string]any{}
	set := func(key string, value any) {
		if value != nil {
			normalized[key] = value
		}
	}
	set("id", pickValue(cleaned["id"], cleaned["connectionId"], cleaned["connection_id"]))
	set("provider", provider)
	set("authType", pickValue(cleaned["authType"], cleaned["auth_type"],
		nestedPick(auth, "type"), nestedPick(auth, "authType"), nestedPick(auth, "auth_type")))
	set("name", pickValue(cleaned["name"], nestedPick(identity, "name"), nestedPick(identity, "label")))
	set("displayName", pickValue(cleaned["displayName"], cleaned["display_name"]))
	set("email", pickValue(cleaned["email"], nestedPick(identity, "email")))
	set("priority", pickValue(cleaned["priority"]))
	set("isActive", pickValue(cleaned["isActive"], cleaned["is_active"]))
	set("defaultModel", pickValue(cleaned["defaultModel"], cleaned["default_model"]))
	set("globalPriority", pickValue(cleaned["globalPriority"], cleaned["global_priority"]))
	set("accessToken", pickTokenField(cleaned, credentials, secrets, token, "accessToken", "access_token"))
	set("refreshToken", pickTokenField(cleaned, credentials, secrets, token, "refreshToken", "refresh_token"))
	set("idToken", pickTokenField(cleaned, credentials, secrets, token, "idToken", "id_token"))
	set("apiKey", pickValue(cleaned["apiKey"], cleaned["api_key"],
		nestedPick(credentials, "apiKey"), nestedPick(credentials, "api_key"),
		nestedPick(secrets, "apiKey"), nestedPick(secrets, "api_key"),
		nestedPick(auth, "apiKey"), nestedPick(auth, "api_key")))
	set("expiresAt", pickTokenField(cleaned, credentials, secrets, token, "expiresAt", "expires_at"))
	set("expiresIn", pickTokenField(cleaned, credentials, secrets, token, "expiresIn", "expires_in"))
	set("tokenType", pickTokenField(cleaned, credentials, secrets, token, "tokenType", "token_type"))
	set("scope", pickValue(cleaned["scope"],
		nestedPick(credentials, "scope"), nestedPick(secrets, "scope"), nestedPick(token, "scope")))
	set("projectId", pickValue(cleaned["projectId"], cleaned["project_id"],
		nestedPick(credentials, "projectId"), nestedPick(credentials, "project_id"),
		nestedPick(secrets, "projectId"), nestedPick(secrets, "project_id"),
		nestedPick(metadata, "projectId"), nestedPick(metadata, "project_id"),
		nestedPick(meta, "projectId"), nestedPick(meta, "project_id")))
	set("routingStatus", pickValue(cleaned["routingStatus"], cleaned["routing_status"]))
	set("quotaState", pickValue(cleaned["quotaState"], cleaned["quota_state"]))
	set("healthStatus", pickValue(cleaned["healthStatus"], cleaned["health_status"]))
	set("authState", pickValue(cleaned["authState"], cleaned["auth_state"]))
	set("reasonCode", pickValue(cleaned["reasonCode"], cleaned["reason_code"]))
	set("reasonDetail", pickValue(cleaned["reasonDetail"], cleaned["reason_detail"]))
	set("nextRetryAt", pickValue(cleaned["nextRetryAt"], cleaned["next_retry_at"]))
	set("resetAt", pickValue(cleaned["resetAt"], cleaned["reset_at"]))
	set("lastCheckedAt", pickValue(cleaned["lastCheckedAt"], cleaned["last_checked_at"]))
	set("usageSnapshot", pickValue(cleaned["usageSnapshot"], cleaned["usage_snapshot"]))
	set("version", pickValue(cleaned["version"]))
	set("lastUsedAt", pickValue(cleaned["lastUsedAt"], cleaned["last_used_at"]))
	set("consecutiveUseCount", pickValue(cleaned["consecutiveUseCount"], cleaned["consecutive_use_count"]))
	set("backoffLevel", pickValue(cleaned["backoffLevel"], cleaned["backoff_level"]))

	providerName, _ := provider.(string)
	if psd := normalizeProviderSpecificData(providerName, mergedPSD); psd != nil {
		normalized["providerSpecificData"] = psd
	}

	if providerName == "codex" {
		if psd, ok := normalized["providerSpecificData"].(map[string]any); ok {
			if rawPlan, _ := psd["planTypeRaw"].(string); rawPlan != "" {
				if _, hasPlan := psd["planType"]; !hasPlan {
					if reNormalized := normalizeCodexProviderSpecificData(psd); reNormalized != nil {
						normalized["providerSpecificData"] = reNormalized
					}
				}
			}
		}
	}

	if _, ok := normalized["routingStatus"]; !ok && recStr(normalized, "quotaState") == "ok" {
		normalized["routingStatus"] = "eligible"
	}
	if _, ok := normalized["reasonCode"]; !ok && recStr(normalized, "routingStatus") == "exhausted" {
		normalized["reasonCode"] = "quota_exhausted"
	}
	if psd, ok := normalized["providerSpecificData"].(map[string]any); ok && len(psd) == 0 {
		delete(normalized, "providerSpecificData")
	}
	return normalized
}

// pickTokenField resolves a token-family field across the record and the
// credentials/secrets/token nests, in upstream order.
func pickTokenField(record, credentials, secrets, token map[string]any, camel, snake string) any {
	return pickValue(
		record[camel], record[snake],
		nestedPick(credentials, camel), nestedPick(credentials, snake),
		nestedPick(secrets, camel), nestedPick(secrets, snake),
		nestedPick(token, camel), nestedPick(token, snake),
	)
}

// normalizeProviderSpecificData mirrors the normalizer's helper: compact,
// then the Codex-specific normalization for the codex provider.
func normalizeProviderSpecificData(provider string, input map[string]any) map[string]any {
	compacted := compactObject(input)
	if len(compacted) == 0 {
		return nil
	}
	if provider == "codex" {
		return normalizeCodexProviderSpecificData(compacted)
	}
	return compacted
}

// --- Codex providerSpecificData normalization ----------------------------

var codexWorkspacePlanTypes = map[string]bool{
	"team": true, "business": true, "enterprise": true, "edu": true,
	"hc": true, "self_serve_business_usage_based": true,
	"enterprise_cbp_usage_based": true,
}

var codexUsageWindowTypes = map[string]bool{
	"weekly_only": true, "session_and_weekly": true, "unknown": true,
}

var codexKnownPlanLabels = map[string]string{
	"free": "Free", "plus": "Plus", "pro": "Pro", "go": "Go",
	"team": "Team", "business": "Business", "enterprise": "Enterprise",
	"edu": "Edu",
}

// normalizeCodexPlanLabel ports normalizeCodexPlanLabel.
func normalizeCodexPlanLabel(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	if label, ok := codexKnownPlanLabels[trimmed]; ok {
		return label
	}
	parts := []string{}
	for _, part := range strings.Split(trimmed, "_") {
		if part == "" {
			continue
		}
		parts = append(parts, strings.ToUpper(part[:1])+part[1:])
	}
	return strings.Join(parts, " ")
}

// normalizeCodexProviderSpecificData ports the function of the same name
// from src/lib/oauth/codexAccount.ts.
func normalizeCodexProviderSpecificData(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	trimmedString := func(key string) string {
		if s, ok := input[key].(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	rawPlanType := trimmedString("planTypeRaw")
	// planType: a string value is taken trimmed (even when it trims to
	// empty); only a non-string falls back to the label derived from
	// planTypeRaw — the upstream ternary exactly.
	planType := ""
	if s, ok := input["planType"].(string); ok {
		planType = strings.TrimSpace(s)
	} else {
		planType = normalizeCodexPlanLabel(rawPlanType)
	}
	chatgptUserId := trimmedString("chatgptUserId")
	chatgptAccountId := trimmedString("chatgptAccountId")

	isWorkspace := false
	if b, ok := input["isWorkspaceAccount"].(bool); ok {
		isWorkspace = b
	} else {
		isWorkspace = rawPlanType != "" && codexWorkspacePlanTypes[rawPlanType]
	}
	hasSessionWindow, hasSessionWindowSet := input["hasSessionWindow"].(bool)
	hasWeeklyWindow, hasWeeklyWindowSet := input["hasWeeklyWindow"].(bool)
	usageWindowType := ""
	if s, ok := input["usageWindowType"].(string); ok && codexUsageWindowTypes[s] {
		usageWindowType = s
	}

	out := map[string]any{}
	for k, v := range input {
		out[k] = v
	}
	setOrDelete := func(key, value string) {
		if value != "" {
			out[key] = value
		} else {
			delete(out, key)
		}
	}
	setOrDelete("planType", planType)
	setOrDelete("planTypeRaw", rawPlanType)
	setOrDelete("chatgptUserId", chatgptUserId)
	setOrDelete("chatgptAccountId", chatgptAccountId)
	_, isWorkspacePresent := input["isWorkspaceAccount"]
	if planType != "" || rawPlanType != "" || chatgptUserId != "" || chatgptAccountId != "" || isWorkspacePresent {
		out["isWorkspaceAccount"] = isWorkspace
	} else {
		delete(out, "isWorkspaceAccount")
	}
	if hasSessionWindowSet {
		out["hasSessionWindow"] = hasSessionWindow
	} else {
		delete(out, "hasSessionWindow")
	}
	if hasWeeklyWindowSet {
		out["hasWeeklyWindow"] = hasWeeklyWindow
	} else {
		delete(out, "hasWeeklyWindow")
	}
	setOrDelete("usageWindowType", usageWindowType)

	if len(out) == 0 {
		return nil
	}
	return out
}

// --- validator --------------------------------------------------------------

var credentialAllowedFields = []string{
	"provider", "authType", "name", "displayName", "email", "priority",
	"isActive", "defaultModel", "globalPriority", "accessToken",
	"refreshToken", "idToken", "apiKey", "expiresAt", "expiresIn",
	"tokenType", "scope", "projectId", "providerSpecificData",
	"routingStatus", "quotaState", "healthStatus", "authState",
	"reasonCode", "reasonDetail", "nextRetryAt", "resetAt", "lastCheckedAt",
	"usageSnapshot", "version", "lastUsedAt", "consecutiveUseCount",
	"backoffLevel",
}

// sanitizeCredentialRecord mirrors sanitizeCredentialRecord: keep only the
// allowed non-null fields, resolve the auth type, seed fresh Codex OAuth
// state, and reject records without a provider or any credential payload.
func sanitizeCredentialRecord(record map[string]any) (map[string]any, *credImportError) {
	data := map[string]any{}
	for _, field := range credentialAllowedFields {
		if v, ok := record[field]; ok && v != nil {
			data[field] = v
		}
	}

	if s, ok := data["provider"].(string); ok {
		data["provider"] = strings.TrimSpace(s)
	} else {
		data["provider"] = ""
	}

	authType, err := inferAuthType(record, data["authType"])
	if err != nil {
		return nil, err
	}
	data["authType"] = authType

	if data["provider"] == "codex" && authType == "oauth" &&
		data["routingStatus"] == nil && data["quotaState"] == nil &&
		data["healthStatus"] == nil && data["authState"] == nil {
		data["routingStatus"] = "eligible"
		data["quotaState"] = "ok"
	}

	if recStr(data, "provider") == "" {
		return nil, invalidRecordError("Credential record is missing provider")
	}
	if !hasCredentialPayload(data) {
		return nil, invalidRecordError("Credential record has no credential payload")
	}
	return data, nil
}

// inferAuthType mirrors inferAuthType + normalizeAuthType.
func inferAuthType(record map[string]any, rawAuthType any) (string, *credImportError) {
	var normalized any
	switch v := rawAuthType.(type) {
	case nil:
		normalized = nil
	case string:
		normalized = strings.TrimSpace(strings.ToLower(v))
	default:
		normalized = v
	}
	if normalized == "apikey" || normalized == "oauth" {
		return normalized.(string), nil
	}
	if normalized != nil {
		original := record["authType"]
		if original == nil {
			original = normalized
		}
		return "", invalidRecordError(fmt.Sprintf("Unsupported authType: %v", original))
	}
	hasAPIKey := false
	if s, ok := record["apiKey"].(string); ok && strings.TrimSpace(s) != "" {
		hasAPIKey = true
	}
	hasOAuthToken := jsTruthy(record["accessToken"]) || jsTruthy(record["refreshToken"]) || jsTruthy(record["idToken"])
	if hasAPIKey && !hasOAuthToken {
		return "apikey", nil
	}
	return "oauth", nil
}

// hasCredentialPayload mirrors the validator's check of the same name.
func hasCredentialPayload(data map[string]any) bool {
	for _, key := range []string{"accessToken", "refreshToken", "idToken", "apiKey", "projectId"} {
		if jsTruthy(data[key]) {
			return true
		}
	}
	return false
}
