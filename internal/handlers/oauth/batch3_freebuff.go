package oauth

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// Freebuff import / auto-import / realtest handlers, driven by the probe
// port in batch3_freebuff_probe.go.

// HandleFreebuffImport validates a pasted Freebuff auth token against the
// session endpoint and stores it as a freebuff connection.
// POST /api/oauth/freebuff/import
// Contract: AxonRouter src/app/api/oauth/freebuff/import/route.ts.
func (h *OAuthHandler) HandleFreebuffImport(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	authToken := jsonStringField(body, "authToken")
	if authToken == "" {
		axonError(w, http.StatusBadRequest, "Auth token is required")
		return
	}
	session, err := getFreebuffProbeSession(r.Context(), authToken)
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if (session.status < 200 || session.status >= 300) && session.status != http.StatusTooManyRequests {
		msg := firstNonEmpty(jsonStringField(session.data, "message"), jsonStringField(session.data, "error"), "Invalid Freebuff token")
		axonError(w, http.StatusBadRequest, msg)
		return
	}

	email := jsonStringField(body, "email")
	userName := jsonStringField(body, "name")
	accountID := jsonStringField(body, "accountId")
	displayName := firstNonEmpty(email, userName, accountID, "Freebuff Account")
	authMethod := firstNonEmpty(jsonStringField(body, "authMethod"), "import-session")
	replaceID := jsonStringField(body, "replaceConnectionId")

	if email != "" {
		if h.freebuffEmailCollision(email, replaceID) {
			if replaceID != "" {
				axonError(w, http.StatusConflict, "Akun ini sudah terhubung sebagai koneksi lain. Import gagal.")
			} else {
				axonError(w, http.StatusConflict, "Email ini sudah terhubung di koneksi Freebuff lain. Gunakan Reset Credentials untuk mengganti.")
			}
			return
		}
	}

	cooldown := session.status == http.StatusTooManyRequests
	quotaState, reasonCode, reasonDetail := "ok", "", ""
	if cooldown {
		quotaState = "cooldown"
		reasonCode = "quota_exhausted"
		reasonDetail = firstNonEmpty(jsonStringField(session.data, "message"), "Daily session limit reached")
	}
	resetAt := jsonStringField(session.data, "resetAt")
	nowISO := axonNowISO()
	dataMap := map[string]any{
		"apiKey":        authToken,
		"accessToken":   authToken,
		"routingStatus": "eligible",
		"quotaState":    quotaState,
		"authState":     "ok",
		"healthStatus":  "healthy",
		"reasonCode":    nullOr(reasonCode),
		"reasonDetail":  nullOr(reasonDetail),
		"nextRetryAt":   nullOr(resetAt),
		"resetAt":       nullOr(resetAt),
		"lastCheckedAt": nowISO,
		"providerSpecificData": map[string]any{
			"authMethod":      authMethod,
			"accountId":       nullOr(accountID),
			"fingerprint":     nullOr(jsonStringField(body, "fingerprintId")),
			"fingerprintHash": nullOr(jsonStringField(body, "fingerprintHash")),
			"instanceId":      nullOr(jsonStringField(body, "instanceId")),
			"name":            nullOr(userName),
			"email":           nullOr(email),
		},
	}
	if email != "" {
		dataMap["email"] = email
	}
	connID := replaceID
	if connID == "" {
		connID = "fb-" + shortHash(authToken)
	}
	if err := h.saveAxonConnection(connID, "freebuff", "apikey", displayName, email, nil, dataMap); err != nil {
		log.Error("oauth", "freebuff import save failed", "error", err)
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"replaced": replaceID != "",
		"connection": map[string]any{
			"id":            connID,
			"provider":      "freebuff",
			"email":         nullOr(email),
			"displayName":   displayName,
			"name":          displayName,
			"routingStatus": "eligible",
			"healthStatus":  "healthy",
			"quotaState":    quotaState,
			"authState":     "ok",
			"reasonCode":    nullOr(reasonCode),
			"reasonDetail":  nullOr(reasonDetail),
			"lastCheckedAt": nowISO,
		},
	})
}

// freebuffEmailCollision reports whether another freebuff connection already
// carries this email (column or data blob), excluding excludeID.
func (h *OAuthHandler) freebuffEmailCollision(email, excludeID string) bool {
	if h.Repo == nil {
		return false
	}
	conns, err := h.Repo.GetProviderConnections("freebuff", false)
	if err != nil {
		return false
	}
	for _, c := range conns {
		if c == nil || c.ID == excludeID {
			continue
		}
		var data map[string]any
		_ = json.Unmarshal([]byte(c.Data), &data)
		if connectionEmail(data, c.Email) == email {
			return true
		}
	}
	return false
}

// HandleFreebuffAutoImport reads the Manicode CLI's stored credentials from
// the host (~/.config/manicode) so the dashboard can offer a one-click import.
// GET /api/oauth/freebuff/auto-import
// Contract: AxonRouter src/app/api/oauth/freebuff/auto-import/route.ts.
func (h *OAuthHandler) HandleFreebuffAutoImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	home, err := os.UserHomeDir()
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	credPath := filepath.Join(home, ".config", "manicode", "credentials.json")
	raw, err := os.ReadFile(credPath)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "Freebuff credentials not found at ~/.config/manicode/credentials.json",
		})
		return
	}
	info, _ := os.Stat(credPath)
	var parsed struct {
		Default *struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Email           string `json:"email"`
			AuthToken       string `json:"authToken"`
			FingerprintID   string `json:"fingerprintId"`
			FingerprintHash string `json:"fingerprintHash"`
		} `json:"default"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"found": false, "error": err.Error(),
		})
		return
	}
	account := parsed.Default
	if account == nil || strings.TrimSpace(account.AuthToken) == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"found": false,
			"error": "Freebuff auth token is missing in credentials.json",
		})
		return
	}
	instanceID := ""
	var ownerMtime any
	ownerPath := filepath.Join(home, ".config", "manicode", "freebuff-instance-owner.json")
	if oraw, oerr := os.ReadFile(ownerPath); oerr == nil {
		var owner struct {
			InstanceID string `json:"instanceId"`
		}
		if json.Unmarshal(oraw, &owner) == nil && strings.TrimSpace(owner.InstanceID) != "" {
			instanceID = strings.TrimSpace(owner.InstanceID)
		}
		if oi, serr := os.Stat(ownerPath); serr == nil {
			ownerMtime = oi.ModTime().UnixMilli()
		}
	}
	var credMtime int64
	if info != nil {
		credMtime = info.ModTime().UnixMilli()
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"found":                true,
		"authToken":            account.AuthToken,
		"name":                 firstNonEmpty(account.Name, account.Email, "Freebuff Account"),
		"email":                nullOr(account.Email),
		"accountId":            nullOr(firstNonEmpty(account.ID, account.Email)),
		"fingerprintId":        nullOr(firstNonEmpty(account.FingerprintID, instanceID)),
		"fingerprintHash":      nullOr(account.FingerprintHash),
		"instanceId":           nullOr(instanceID),
		"credentialsMtimeMs":   credMtime,
		"instanceOwnerMtimeMs": ownerMtime,
	})
}

// HandleFreebuffRealtest runs Axon's real upstream probe: force-join a
// session, start an agent run, and send a one-word completion through it.
// POST /api/oauth/freebuff/realtest {authToken, prompt?, clientId?, agentId?, model?}
// Contract: AxonRouter src/app/api/oauth/freebuff/realtest/route.ts.
func (h *OAuthHandler) HandleFreebuffRealtest(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	authToken := jsonStringField(body, "authToken")
	if authToken == "" {
		axonError(w, http.StatusBadRequest, "Auth token is required")
		return
	}
	prompt := firstNonEmpty(jsonStringField(body, "prompt"), "Say hello in one word")
	clientID := firstNonEmpty(jsonStringField(body, "clientId"), freebuffDefaultClientID)
	agentID := firstNonEmpty(jsonStringField(body, "agentId"), freebuffDefaultAgentID)
	model := firstNonEmpty(jsonStringField(body, "model"), freebuffDefaultModel)
	ctx := r.Context()

	ensured, err := ensureFreebuffProbeSession(ctx, authToken, model, true)
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var activePayload map[string]any
	if ensured.join != nil {
		activePayload = ensured.join.data
	} else if ensured.session != nil {
		activePayload = ensured.session.data
	}
	sessionClientID := firstNonEmpty(freebuffFingerprint(activePayload), clientID)
	instanceID := freebuffFingerprint(activePayload)

	run, err := freebuffProbeCall(ctx, http.MethodPost, "/api/v1/agent-runs", authToken, "", map[string]any{
		"action": "START", "agentId": agentID,
	})
	if err != nil {
		axonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var completion *freebuffProbeResult
	if runID := jsonStringField(run.data, "runId"); runID != "" {
		metadata := map[string]any{
			"run_id":    runID,
			"client_id": sessionClientID,
			"cost_mode": "free",
		}
		if instanceID != "" {
			metadata["freebuff_instance_id"] = instanceID
		}
		completion, err = freebuffProbeCall(ctx, http.MethodPost, "/api/v1/chat/completions", authToken, instanceID, map[string]any{
			"model":             model,
			"messages":          []any{map[string]any{"role": "user", "content": prompt}},
			"max_tokens":        50,
			"codebuff_metadata": metadata,
			"provider":          map[string]any{"order": []string{"deepseek"}, "allow_fallbacks": true},
		})
		if err != nil {
			axonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	var sessionStatus, sessionPayload any
	var sessionInterpretation any
	if ensured.session != nil {
		sessionStatus = ensured.session.status
		sessionPayload = ensured.session.data
		sessionInterpretation = explainFreebuffError(ensured.session.data)
	}
	var joinPayload any
	if ensured.join != nil {
		joinPayload = map[string]any{
			"status":         ensured.join.status,
			"payload":        ensured.join.data,
			"interpretation": explainFreebuffError(ensured.join.data),
		}
	}
	var completionPayload any
	if completion != nil {
		completionPayload = map[string]any{
			"status":         completion.status,
			"payload":        completion.data,
			"interpretation": explainFreebuffError(completion.data),
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"session": map[string]any{
			"active":         ensured.active,
			"status":         sessionStatus,
			"payload":        sessionPayload,
			"interpretation": sessionInterpretation,
		},
		"join": joinPayload,
		"activeSession": map[string]any{
			"payload":        activePayload,
			"interpretation": explainFreebuffError(activePayload),
		},
		"run": map[string]any{
			"status":  run.status,
			"payload": run.data,
		},
		"completion": completionPayload,
	})
}
