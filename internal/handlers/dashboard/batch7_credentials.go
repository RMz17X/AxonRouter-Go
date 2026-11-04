package dashboard

import (
	"io"
	"net/http"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// Credential backup export/import (batch 7, AxonRouter parity):
// GET /api/credentials/export and POST /api/credentials/import. Both sit
// behind requireManagementAuth upstream (applied at registration). The
// import orchestration lives in batch7_credentials_import.go.

// credentialBackupKeys is the export whitelist, in upstream order.
var credentialBackupKeys = []string{
	"id", "provider", "authType", "name", "displayName", "email",
	"priority", "isActive", "defaultModel", "globalPriority",
	"accessToken", "refreshToken", "idToken", "apiKey", "expiresAt",
	"expiresIn", "tokenType", "scope", "projectId", "providerSpecificData",
	"routingStatus", "quotaState", "healthStatus", "authState",
	"reasonCode", "reasonDetail", "nextRetryAt", "resetAt", "lastCheckedAt",
	"usageSnapshot", "version", "lastUsedAt", "consecutiveUseCount",
	"backoffLevel",
}

// flattenCredentialConnection is the connection view the credentials
// backup reads: the shared flattenConnection (batch 2) plus the
// consecutive-use counter column, which the export whitelist carries
// but the shared view leaves in the data blob.
func flattenCredentialConnection(c *models.ProviderConnection) map[string]any {
	flat := flattenConnection(c)
	if c.ConsecutiveUseCount != nil {
		flat["consecutiveUseCount"] = *c.ConsecutiveUseCount
	}
	return flat
}

// jsTruthy mirrors JS Boolean() for the JSON value kinds a connection
// record can hold (objects and arrays are always truthy).
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

// HandleCredentialsExport serves GET /api/credentials/export: every
// connection carrying credential material, reduced to the backup
// whitelist, in the universal-credentials envelope.
func (h *DashboardHandler) HandleCredentialsExport(w http.ResponseWriter, r *http.Request) {
	connections, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Failed to export credentials",
		})
		return
	}

	entries := []any{}
	for _, conn := range connections {
		flat := flattenCredentialConnection(conn)
		if !hasCredentialData(flat) {
			continue
		}
		record := map[string]any{}
		for _, key := range credentialBackupKeys {
			if v, ok := flat[key]; ok && v != nil {
				record[key] = v
			}
		}
		entries = append(entries, record)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"format":     "universal-credentials",
		"exportedAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"entries":    entries,
	})
}

// hasCredentialData mirrors the export filter of the same name.
func hasCredentialData(flat map[string]any) bool {
	for _, key := range []string{"accessToken", "refreshToken", "idToken", "apiKey", "projectId", "providerSpecificData"} {
		if jsTruthy(flat[key]) {
			return true
		}
	}
	return false
}

// HandleCredentialsImport serves POST /api/credentials/import: normalize,
// validate, dedupe and upsert a credential backup payload. Error mapping
// follows the upstream route exactly, including the two branches
// (INVALID_LEGACY_STATUS_FIELDS, REPLACE_MODE_VALIDATION_FAILED) that no
// current upstream code path can throw — kept so the route contract
// stays complete.
func (h *DashboardHandler) HandleCredentialsImport(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "Failed to import credentials",
		})
		return
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error":     "Invalid JSON request body",
			"errorCode": "INVALID_JSON",
		})
		return
	}

	result, importErr := h.importCredentials(r.Context(), payload)
	if importErr != nil {
		switch importErr.code {
		case "INVALID_IMPORT_PAYLOAD", "DUPLICATE_IMPORT_RECORDS":
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"error":     importErr.message,
				"errorCode": importErr.code,
			})
		case "INVALID_LEGACY_STATUS_FIELDS":
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"error":        importErr.message,
				"errorCode":    importErr.code,
				"legacyFields": []any{},
			})
		case "REPLACE_MODE_VALIDATION_FAILED":
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"error":          importErr.message,
				"errorCode":      importErr.code,
				"invalidRecords": []any{},
			})
		default:
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to import credentials",
			})
		}
		return
	}

	response := map[string]any{"success": true}
	for k, v := range result {
		response[k] = v
	}
	handlerutil.WriteJSON(w, http.StatusOK, response)
}
