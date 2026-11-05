package dashboard

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
	"time"

	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

// Batch 2 (Providers family) shared helpers.
//
// AxonRouter stores connection health in dedicated columns (test_status,
// locked_all_until, model_locks, last_error, ...) and spreads the rest of
// the row's data JSON over them when it flattens a connection
// (rowToConnection). This codebase keeps the same facts inside the single
// data JSON blob — model locks as modelLock_<model> keys, the quota
// snapshot as the "quotas" object — so the helpers here translate between
// the two representations without inventing a second store.

// connDataMap decodes a connection's data blob into a mutable map. A
// connection with no blob (or a malformed one) reads as an empty map.
func connDataMap(c *models.ProviderConnection) map[string]any {
	if c == nil {
		return map[string]any{}
	}
	m, _ := decodeConnectionData(c.Data)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// dataStr reads a string field out of a decoded data map.
func dataStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// dataMap reads a nested object field out of a decoded data map.
func dataMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// dataTime parses a stored timestamp (ISO string, or epoch milliseconds as
// a number) the way AxonRouter's `new Date(value)` reads it.
func dataTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
			if parsed, err := time.Parse(layout, s); err == nil {
				return parsed, true
			}
		}
		return time.Time{}, false
	case float64:
		if t <= 0 {
			return time.Time{}, false
		}
		return time.UnixMilli(int64(t)).UTC(), true
	}
	return time.Time{}, false
}

// dataTimeField reads and parses a timestamp field of a data map.
func dataTimeField(m map[string]any, key string) (time.Time, bool) {
	return dataTime(m[key])
}

// futureTime reports whether the field holds a timestamp still ahead of now.
func futureTime(m map[string]any, key string, now time.Time) (time.Time, bool) {
	t, ok := dataTimeField(m, key)
	if !ok || !t.After(now) {
		return time.Time{}, false
	}
	return t, true
}

const modelLockKeyPrefix = "modelLock_"

// activeModelLocks returns the connection's live model locks as a
// model -> until-ISO map, merging AxonRouter's two storage shapes: the
// modelLocks object and the flattened modelLock_<model> keys this codebase
// writes. Expired or null entries are not locks.
func activeModelLocks(data map[string]any, now time.Time) map[string]string {
	locks := map[string]string{}
	if ml := dataMap(data, "modelLocks"); ml != nil {
		for model, v := range ml {
			if t, ok := dataTime(v); ok && t.After(now) {
				locks[model] = t.Format(time.RFC3339Nano)
				if s, isStr := v.(string); isStr {
					locks[model] = s
				}
			}
		}
	}
	for key, v := range data {
		if !strings.HasPrefix(key, modelLockKeyPrefix) {
			continue
		}
		model := strings.TrimPrefix(key, modelLockKeyPrefix)
		if model == "" {
			continue
		}
		if t, ok := dataTime(v); ok && t.After(now) {
			locks[model] = t.Format(time.RFC3339Nano)
			if s, isStr := v.(string); isStr {
				locks[model] = s
			}
		}
	}
	return locks
}

// allModelLocks is activeModelLocks without the expiry filter, for response
// shaping (AxonRouter's flattened modelLocks carries whatever is stored).
func storedModelLocks(data map[string]any) map[string]any {
	locks := map[string]any{}
	if ml := dataMap(data, "modelLocks"); ml != nil {
		for model, v := range ml {
			if v != nil {
				locks[model] = v
			}
		}
	}
	for key, v := range data {
		if !strings.HasPrefix(key, modelLockKeyPrefix) || v == nil {
			continue
		}
		if model := strings.TrimPrefix(key, modelLockKeyPrefix); model != "" {
			locks[model] = v
		}
	}
	return locks
}

// lastErrorText flattens a stored lastError (a bare string here or in
// AxonRouter, an object with a message in this codebase's recorder) into
// the comparable string.
func lastErrorText(data map[string]any) string {
	switch v := data["lastError"].(type) {
	case string:
		return v
	case map[string]any:
		if msg, ok := v["message"].(string); ok && msg != "" {
			return msg
		}
		if msg, ok := v["error"].(string); ok && msg != "" {
			return msg
		}
	}
	return ""
}

// fatalConnectionError mirrors AxonRouter's FATAL_CONNECTION_ERROR_SQL: a
// last error that marks the credential itself dead.
var fatalConnectionError = regexp.MustCompile(`(?i)(banned|account has been banned|account has been deleted|suspended|revoked|invalid_grant|invalid token|invalid api key|unauthorized|forbidden)`)

// refreshBlocked reports whether providerSpecificData carries a truthy
// refreshBlocked marker (AxonRouter's CONNECTION_UNAVAILABLE_DATA_SQL:
// present, not 'false', not ”).
func refreshBlocked(psd map[string]any) bool {
	if psd == nil {
		return false
	}
	v, ok := psd["refreshBlocked"]
	if !ok || v == nil {
		return false
	}
	if b, isBool := v.(bool); isBool {
		return b
	}
	if s, isStr := v.(string); isStr {
		return s != "" && s != "false"
	}
	return true
}

// flattenConnection builds AxonRouter's rowToConnection shape from a stored
// connection: the data blob's fields as the base, the column fields laid
// over them, and the column-equivalents this codebase keeps in the blob
// (testStatus, locks, lastError, ...) resolved to their Axon names.
func flattenConnection(c *models.ProviderConnection) map[string]any {
	data := connDataMap(c)
	flat := make(map[string]any, len(data)+16)
	for k, v := range data {
		flat[k] = v
	}
	delete(flat, "modelLocks")

	testStatus := dataStr(data, "testStatus")
	if testStatus == "" {
		testStatus = "active"
	}
	flat["id"] = c.ID
	flat["provider"] = c.Provider
	flat["authType"] = c.AuthType
	flat["name"] = ptrString(c.Name)
	flat["email"] = ptrString(c.Email)
	flat["priority"] = ptrInt(c.Priority)
	isActive := c.IsActive == 1 && data["disabledAt"] == nil && testStatus != "disabled"
	flat["isActive"] = isActive
	flat["testStatus"] = testStatus
	flat["lockedAllUntil"] = data["lockedAllUntil"]
	flat["rateLimitedUntil"] = data["rateLimitedUntil"]
	if v := data["lockedToModel"]; v != nil {
		flat["lockedToModel"] = v
	} else {
		flat["lockedToModel"] = nil
	}
	flat["lockedToModelUntil"] = data["lockedToModelUntil"]
	if v, ok := data["tokenExpiresAt"]; ok {
		flat["tokenExpiresAt"] = v
	} else {
		flat["tokenExpiresAt"] = data["expiresAt"]
	}
	if c.LastUsedAt != nil {
		flat["lastUsedAt"] = *c.LastUsedAt
	} else {
		flat["lastUsedAt"] = data["lastUsedAt"]
	}
	flat["modelLocks"] = storedModelLocks(data)
	if le := lastErrorText(data); le != "" {
		flat["lastError"] = le
	} else {
		flat["lastError"] = nil
	}
	flat["errorCode"] = data["errorCode"]
	flat["lastErrorAt"] = data["lastErrorAt"]
	flat["createdAt"] = c.CreatedAt
	flat["updatedAt"] = c.UpdatedAt
	return flat
}

func ptrString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func ptrInt(i *int) any {
	if i == nil {
		return nil
	}
	return *i
}

// providerConnectionResponse mirrors AxonRouter's
// toProviderConnectionResponse: the flattened connection with the
// credential fields removed, plus the AWS session token when the provider
// uses the AWS credential form.
func providerConnectionResponse(flat map[string]any) map[string]any {
	if flat == nil {
		return nil
	}
	out := make(map[string]any, len(flat))
	for k, v := range flat {
		switch k {
		case "apiKey", "accessToken", "refreshToken", "idToken":
			continue
		}
		out[k] = v
	}
	provider, _ := flat["provider"].(string)
	if meta, ok := providers.ProviderRegistry[providers.ResolveAlias(provider)]; ok && meta.CredentialForm == "aws" {
		if psd := dataMap(flat, "providerSpecificData"); psd != nil {
			clean := make(map[string]any, len(psd))
			for k, v := range psd {
				if k != "sessionToken" {
					clean[k] = v
				}
			}
			out["providerSpecificData"] = clean
		}
	}
	return out
}

// persistConnectionData marshals a mutated data map back onto the
// connection row through the standard write path.
func (h *DashboardHandler) persistConnectionData(c *models.ProviderConnection, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return h.Repo.UpdateConnectionData(c.ID, string(encoded))
}

// displayNameOr is AxonRouter's name || email || fallback chain (the
// package already has a connectionDisplayName with a provider fallback,
// hence the distinct name).
func displayNameOr(c *models.ProviderConnection, fallback string) string {
	if c.Name != nil && *c.Name != "" {
		return *c.Name
	}
	if c.Email != nil && *c.Email != "" {
		return *c.Email
	}
	return fallback
}
