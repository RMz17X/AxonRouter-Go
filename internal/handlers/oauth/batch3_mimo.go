package oauth

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
)

// Xiaomi MiMo key-provider imports (AxonRouter src/app/api/oauth/
// xiaomi-mimo/{api-key,auto-import}/route.js) plus the desktop passToken
// reader (open-sse/shared/mimoAccount.js) that both this file's auto-import
// and the generic dispatch's mimo exchange use. These serve the sk- key
// provider (api.xiaomimimo.com/v1) — a separate provider from the free
// tier. The pre-existing mimo ECDH machinery in mimo.go is untouched.

// mimoDefaultBaseURL is the Xiaomi MiMo API endpoint for sk- keys.
const mimoDefaultBaseURL = "https://api.xiaomimimo.com/v1"

// HandleMimoAPIKey imports a Xiaomi MiMo API key: the key is validated
// against the models endpoint (soft-fail — an unvalidated key is still
// stored, marked untested), then stored as a xiaomi-mimo connection.
// Re-importing the same uid or key updates the existing connection.
// POST /api/oauth/xiaomi-mimo/api-key {apiKey, uid?, baseUrl?,
// mimoPassToken?, mimoUserId?, mimoCUserId?}
func (h *OAuthHandler) HandleMimoAPIKey(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var bodyAny any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &bodyAny); err != nil {
			// Axon's route-wide catch: an unparseable body is a 500 here,
			// not a field-validation 400.
			axonError(w, http.StatusInternalServerError, "API key import failed")
			return
		}
	}
	body, _ := bodyAny.(map[string]any)
	apiKey, _ := body["apiKey"].(string)
	if strings.TrimSpace(apiKey) == "" {
		axonError(w, http.StatusBadRequest, "API key is required")
		return
	}
	key := strings.TrimSpace(apiKey)
	if !strings.HasPrefix(key, "sk-") {
		axonError(w, http.StatusBadRequest, "Invalid key format — expected sk- prefix")
		return
	}
	uid := jsonStringField(body, "uid")
	baseURL := strings.TrimRight(jsonStringField(body, "baseUrl"), "/")
	if baseURL == "" {
		baseURL = mimoDefaultBaseURL
	}
	passToken := jsonStringField(body, "mimoPassToken")
	mimoUserID := jsonStringField(body, "mimoUserId")
	mimoCUserID := jsonStringField(body, "mimoCUserId")

	validated, modelCount := validateMimoKey(r.Context(), baseURL, key)

	if id, name, email, blob, found := h.findMimoKeyConnection(uid, key); found {
		h.updateMimoKeyConnection(w, id, name, email, blob, mimoPSDUpdate{
			key: key, uid: uid, baseURL: baseURL, modelCount: modelCount,
			validated: validated, passToken: passToken,
			userID: mimoUserID, cUserID: mimoCUserID,
		})
		return
	}

	displayName := "Xiaomi MiMo"
	email := ""
	if uid != "" {
		email = uid + "@xiaomi"
		displayName = "Xiaomi " + uid
	}
	testStatus := "untested"
	if validated {
		testStatus = "active"
	}
	dataMap := map[string]any{
		"apiKey":      key,
		"accessToken": key,
		"providerSpecificData": map[string]any{
			"uid": nullOr(uid), "baseUrl": baseURL,
			"authMethod": "api_key", "provider": "API Key",
			"modelCount":    modelCount,
			"mimoPassToken": nullOr(passToken),
			"mimoUserId":    nullOr(mimoUserID),
			"mimoCUserId":   nullOr(mimoCUserID),
		},
		"testStatus": testStatus,
		"expiresAt":  axonExpiresISO(365 * 24 * time.Hour),
	}
	if email != "" {
		dataMap["email"] = email
	}
	connID := "xiaomi-mimo-" + shortHash(key)
	if err := h.saveAxonConnection(connID, "xiaomi-mimo", "api_key", displayName, email, nil, dataMap); err != nil {
		log.Error("oauth", "xiaomi-mimo api-key save failed", "error", err)
		axonError(w, http.StatusInternalServerError, "API key import failed")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "validated": validated, "modelCount": modelCount,
		"connection": map[string]any{
			"id": connID, "provider": "xiaomi-mimo",
			"email": nullOr(email), "displayName": displayName,
		},
	})
}

// mimoPSDUpdate carries one api-key import's merge inputs for an existing
// connection.
type mimoPSDUpdate struct {
	key        string
	uid        string
	baseURL    string
	modelCount int
	validated  bool
	passToken  string
	userID     string
	cUserID    string
}

// updateMimoKeyConnection applies Axon's dedup update: same row, new key,
// providerSpecificData merged over the stored one (import values win, then
// stored values, then null), testStatus promoted to active only when the
// new key validated.
func (h *OAuthHandler) updateMimoKeyConnection(w http.ResponseWriter, id, name, email string, blob map[string]any, up mimoPSDUpdate) {
	psd, _ := blob["providerSpecificData"].(map[string]any)
	merged := make(map[string]any, len(psd)+6)
	for k, v := range psd {
		merged[k] = v
	}
	merged["uid"] = nullOr(firstNonEmpty(up.uid, psdStr(psd, "uid")))
	merged["baseUrl"] = up.baseURL
	merged["mimoPassToken"] = nullOr(firstNonEmpty(up.passToken, psdStr(psd, "mimoPassToken")))
	merged["mimoUserId"] = nullOr(firstNonEmpty(up.userID, psdStr(psd, "mimoUserId")))
	merged["mimoCUserId"] = nullOr(firstNonEmpty(up.cUserID, psdStr(psd, "mimoCUserId")))
	merged["modelCount"] = up.modelCount
	blob["providerSpecificData"] = merged
	blob["accessToken"] = up.key
	blob["apiKey"] = up.key
	if up.validated {
		blob["testStatus"] = "active"
	}
	if err := h.saveAxonConnection(id, "xiaomi-mimo", "api_key", name, email, nil, blob); err != nil {
		log.Error("oauth", "xiaomi-mimo api-key update failed", "error", err)
		axonError(w, http.StatusInternalServerError, "API key import failed")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true, "validated": up.validated, "modelCount": up.modelCount,
		"updated": true,
		"connection": map[string]any{
			"id": id, "provider": "xiaomi-mimo",
			"email": nullOr(email), "displayName": nullOr(name),
		},
	})
}

// findMimoKeyConnection locates the xiaomi-mimo connection an api-key
// import should update: same account email (uid@xiaomi) or same stored
// access token. Returns the row's id, name column, flattened email, and
// parsed data blob.
func (h *OAuthHandler) findMimoKeyConnection(uid, key string) (string, string, string, map[string]any, bool) {
	if h.Repo == nil || h.Repo.RawDB() == nil {
		return "", "", "", nil, false
	}
	rows, err := h.Repo.Query(
		"SELECT id, name, email, data FROM providerConnections WHERE provider = 'xiaomi-mimo'")
	if err != nil {
		return "", "", "", nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var nameCol, emailCol, dataCol sql.NullString
		if err := rows.Scan(&id, &nameCol, &emailCol, &dataCol); err != nil {
			continue
		}
		blob := map[string]any{}
		if dataCol.Valid {
			_ = json.Unmarshal([]byte(dataCol.String), &blob)
		}
		var emailPtr *string
		if emailCol.Valid {
			emailPtr = &emailCol.String
		}
		email := connectionEmail(blob, emailPtr)
		token, _ := blob["accessToken"].(string)
		if (uid != "" && email == uid+"@xiaomi") || (token != "" && token == key) {
			return id, nameCol.String, email, blob, true
		}
	}
	return "", "", "", nil, false
}

// validateMimoKey probes GET {baseUrl}/models the way Axon's import does:
// any failure (network, non-2xx, unparseable body) is a soft fail — the
// import proceeds unvalidated.
func validateMimoKey(ctx context.Context, baseURL, key string) (bool, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return false, 0
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Mimo-Source", "mimocode-cli")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, 0
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return false, 0
	}
	count := 0
	if arr, ok := data["data"].([]any); ok {
		count = len(arr)
	}
	return true, count
}

// HandleMimoAutoImport detects Xiaomi MiMo credentials from the local
// MiMoCode/Desktop auth.json (~/.local/share/mimocode/auth.json): the
// xiaomi field's sk- key plus its uid/base_url metadata, enriched with the
// Desktop account-session passToken when the cookie store is readable.
// GET /api/oauth/xiaomi-mimo/auto-import — all negative answers are
// HTTP 200 {found:false, error}.
func (h *OAuthHandler) HandleMimoAutoImport(w http.ResponseWriter, r *http.Request) {
	candidates := mimoAuthCandidates()
	var authPath string
	var raw []byte
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		body, err := os.ReadFile(candidate)
		if err == nil {
			authPath, raw = candidate, body
			break
		}
	}
	if authPath == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "Xiaomi MiMo Desktop auth file not found. Checked:\n" +
				strings.Join(candidates, "\n") +
				"\n\nMake sure Xiaomi MiMo Desktop is installed and you are signed in.",
		})
		return
	}
	var auth map[string]any
	if err := json.Unmarshal(raw, &auth); err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "auth.json is not valid JSON. Please sign in to Xiaomi MiMo Desktop again.",
		})
		return
	}
	xiaomi, _ := auth["xiaomi"].(map[string]any)
	key := mimoKeyString(xiaomi["key"])
	if xiaomi == nil || key == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "No Xiaomi credentials found in auth.json. Please sign in to Xiaomi MiMo Desktop.",
		})
		return
	}
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "sk-") {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "Xiaomi key does not appear to be a valid API key (expected sk- prefix).",
		})
		return
	}
	metadata, _ := xiaomi["metadata"].(map[string]any)
	var uid any
	baseURL := mimoDefaultBaseURL
	if metadata != nil {
		switch v := metadata["uid"].(type) {
		case string:
			if v != "" {
				uid = v
			}
		case float64, bool:
			uid = v
		}
		if s, ok := metadata["base_url"].(string); ok && s != "" {
			baseURL = s
		}
	}
	resp := map[string]any{
		"found": true, "apiKey": key, "uid": uid, "baseUrl": baseURL,
		"source":        authPath,
		"mimoPassToken": nil, "mimoUserId": nil, "mimoCUserId": nil,
	}
	if pt := readDesktopPassToken(); pt != nil {
		resp["mimoPassToken"] = pt.passToken
		resp["mimoUserId"] = nullOr(pt.userID)
		resp["mimoCUserId"] = nullOr(pt.cUserID)
	}
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// mimoAuthCandidates lists the auth.json locations Axon probes, in order.
func mimoAuthCandidates() []string {
	home, _ := os.UserHomeDir()
	paths := []string{filepath.Join(home, ".local", "share", "mimocode", "auth.json")}
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		paths = append(paths, filepath.Join(appData, "Xiaomi MiMo", "auth.json"))
	case "darwin":
		paths = append(paths, filepath.Join(home, "Library", "Application Support", "mimocode", "auth.json"))
	}
	return paths
}

// mimoKeyString renders auth.json's key field the way JS String() would
// for the scalar shapes it can take; absent/falsy values yield "".
func mimoKeyString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == 0 {
			return ""
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if !t {
			return ""
		}
		return "true"
	}
	return ""
}
