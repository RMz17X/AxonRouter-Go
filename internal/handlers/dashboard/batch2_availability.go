package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"time"

	"axonrouter/proxy/internal/handlerutil"
)

// GET /api/models/availability — port of AxonRouter's
// src/app/api/models/availability/route.js.
//
// One entry per live lock on an unavailable-or-locked connection: the
// account-wide lock surfaces as model "__all", each live per-model lock as
// its own entry. A connection marked unavailable with no live lock left
// contributes a single "__all" unavailable entry (without an until).

type availabilityEntry struct {
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	Status         string  `json:"status"`
	Until          *string `json:"until,omitempty"`
	ConnectionID   string  `json:"connectionId"`
	ConnectionName string  `json:"connectionName"`
	LastError      any     `json:"lastError"`
}

func (h *DashboardHandler) HandleModelAvailability(w http.ResponseWriter, r *http.Request) {
	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch model availability"})
		return
	}
	now := time.Now()
	models := make([]availabilityEntry, 0)
	for _, c := range conns {
		if c == nil {
			continue
		}
		data := connDataMap(c)
		locks := activeModelLocks(data, now)
		_, hasAccountLock := futureTime(data, "lockedAllUntil", now)
		testStatus := dataStr(data, "testStatus")
		if !hasAccountLock && len(locks) == 0 && testStatus != "unavailable" {
			continue
		}
		name := displayNameOr(c, c.ID)
		var lastError any
		if le := lastErrorText(data); le != "" {
			lastError = le
		}
		// AxonRouter emits the account lock first (from lockedAllUntil),
		// then the modelLocks entries in stored order; the stored order is
		// not recoverable from a Go map, so the rest follow in map order.
		if hasAccountLock {
			u, _ := data["lockedAllUntil"].(string)
			models = append(models, availabilityEntry{
				Provider: c.Provider, Model: "__all", Status: "cooldown",
				Until: &u, ConnectionID: c.ID, ConnectionName: name, LastError: lastError,
			})
		}
		for model, until := range locks {
			if model == "__all" && hasAccountLock {
				continue
			}
			u := until
			models = append(models, availabilityEntry{
				Provider: c.Provider, Model: model, Status: "cooldown",
				Until: &u, ConnectionID: c.ID, ConnectionName: name, LastError: lastError,
			})
		}
		if !hasAccountLock && len(locks) == 0 && testStatus == "unavailable" {
			models = append(models, availabilityEntry{
				Provider: c.Provider, Model: "__all", Status: "unavailable",
				ConnectionID: c.ID, ConnectionName: name, LastError: lastError,
			})
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"models":           models,
		"unavailableCount": len(models),
	})
}

// POST /api/models/availability — clears one model's cooldown across a
// provider's connections ({action:"clearCooldown", provider, model}).
// AxonRouter also pokes its speed-layer cooldown cache; this codebase
// reads locks straight from the stored data, so clearing the data is the
// whole operation.
func (h *DashboardHandler) HandleClearModelCooldown(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to clear cooldown"})
		return
	}
	defer r.Body.Close()
	var body struct {
		Action   string `json:"action"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to clear cooldown"})
		return
	}
	if body.Action != "clearCooldown" || body.Provider == "" || body.Model == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request"})
		return
	}

	conns, err := h.Repo.GetProviderConnections(body.Provider, false)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to clear cooldown"})
		return
	}
	lockKey := modelLockKeyPrefix + body.Model
	isAll := body.Model == "__all"
	for _, c := range conns {
		if c == nil {
			continue
		}
		data := connDataMap(c)
		ml := dataMap(data, "modelLocks")
		hasLock := data[lockKey] != nil || (ml != nil && ml[body.Model] != nil)
		if !hasLock && isAll && (data["lockedAllUntil"] != nil || data["rateLimitedUntil"] != nil) {
			hasLock = true
		}
		if !hasLock {
			continue
		}
		data[lockKey] = nil
		if ml == nil {
			ml = map[string]any{}
		}
		ml[body.Model] = nil
		data["modelLocks"] = ml
		if isAll {
			data["lockedAllUntil"] = nil
			data["rateLimitedUntil"] = nil
		}
		if dataStr(data, "testStatus") == "unavailable" {
			data["testStatus"] = "active"
			data["lastError"] = nil
			data["lastErrorAt"] = nil
			data["backoffLevel"] = 0
		}
		_ = h.persistConnectionData(c, data)
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
