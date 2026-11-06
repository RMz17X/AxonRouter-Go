package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"sort"
	"strings"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

// Connection status administration — ports of AxonRouter's
// src/app/api/providers/[id]/reset-status, [id]/unlock-model,
// providers/bulk-reset-status and providers/bulk-proxy routes.
//
// AxonRouter keeps health in columns and deletes the persisted quota
// snapshot (usage_snapshots table) as a side effect; here the snapshot is
// the connection's own "quotas" object, so a reset deletes that key — the
// same state, the store this codebase actually reads.

// resetConnectionData applies the single-connection reset field set to a
// decoded data map (AxonRouter's reset-status updates object).
func resetConnectionData(data map[string]any) {
	data["testStatus"] = "active"
	data["status"] = "active"
	data["lastError"] = nil
	data["lastErrorAt"] = nil
	data["errorCode"] = nil
	data["backoffLevel"] = 0
	data["rateLimitedUntil"] = nil
	data["oauthLockedUntil"] = nil
	data["oauthFailureCount"] = 0
	data["lockedAllUntil"] = nil
	data["lockedToModel"] = nil
	data["lockedToModelUntil"] = nil
	data["modelLocks"] = map[string]any{}
	data["disabledReason"] = nil
	data["previousStatus"] = nil
	data["disabledAt"] = nil
	data["disabledBy"] = nil
	delete(data, "verificationUrl")
	for k := range data {
		if strings.HasPrefix(k, modelLockKeyPrefix) {
			data[k] = nil
		}
	}
	if psd := dataMap(data, "providerSpecificData"); psd != nil {
		changed := false
		for k := range psd {
			if strings.HasPrefix(k, modelLockKeyPrefix) {
				delete(psd, k)
				changed = true
			}
		}
		if _, ok := psd["refreshBlocked"]; ok {
			delete(psd, "refreshBlocked")
			changed = true
		}
		if _, ok := psd["refreshBlockedAt"]; ok {
			delete(psd, "refreshBlockedAt")
			changed = true
		}
		for _, k := range []string{"validationUrl", "validationMessage", "validationAt"} {
			if _, ok := psd[k]; ok {
				delete(psd, k)
				changed = true
			}
		}
		if changed {
			data["providerSpecificData"] = psd
		}
	}
	delete(data, "quotas")
}

// HandleResetConnectionStatus serves POST /api/providers/{id}/reset-status.
func (h *DashboardHandler) HandleResetConnectionStatus(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Connection not found"})
		return
	}
	data := connDataMap(conn)
	resetConnectionData(data)
	// AxonRouter sets isActive=true in the same update; here the data blob
	// and the toggle are separate writers.
	if err := h.persistConnectionData(conn, data); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset connection status"})
		return
	}
	if err := h.Repo.SetConnectionStatus(conn.ID, true); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset connection status"})
		return
	}
	updated, err := h.Repo.GetProviderConnectionByID(conn.ID)
	if err != nil || updated == nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset connection status"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"connection": providerConnectionResponse(flattenConnection(updated)),
	})
}

// HandleUnlockConnectionModel serves POST /api/providers/{id}/unlock-model:
// releases only the model affinity lock (lockedToModel / lockedToModelUntil).
func (h *DashboardHandler) HandleUnlockConnectionModel(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	conn, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil || conn == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Connection not found"})
		return
	}
	data := connDataMap(conn)
	data["lockedToModel"] = nil
	data["lockedToModelUntil"] = nil
	if err := h.persistConnectionData(conn, data); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to unlock model"})
		return
	}
	updated, err := h.Repo.GetProviderConnectionByID(conn.ID)
	if err != nil || updated == nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to unlock model"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"connection": providerConnectionResponse(flattenConnection(updated)),
	})
}

// bulkTargetConnections resolves a bulk route's target set: an explicit id
// list wins, otherwise every connection of the provider.
func (h *DashboardHandler) bulkTargetConnections(provider string, ids []string) ([]*models.ProviderConnection, error) {
	if len(ids) > 0 {
		out := make([]*models.ProviderConnection, 0, len(ids))
		for _, id := range ids {
			conn, err := h.Repo.GetProviderConnectionByID(id)
			if err != nil {
				return nil, err
			}
			if conn != nil {
				out = append(out, conn)
			}
		}
		return out, nil
	}
	return h.Repo.GetProviderConnections(provider, false)
}

// storedConnectionDisabled reports whether the decoded data blob carries
// the disabled state (status / testStatus). The blob and the isActive
// flag are written by different paths, and either one is a verdict a
// cooldown reset must not clear.
func storedConnectionDisabled(data map[string]any) bool {
	if data == nil {
		return false
	}
	status, _ := data["status"].(string)
	testStatus, _ := data["testStatus"].(string)
	return strings.EqualFold(status, "disabled") || strings.EqualFold(testStatus, "disabled")
}

// bulkResetConnectionData applies the bulk reset field set. Unlike the
// single reset, AxonRouter's bulk SQL DELETES the disabled*/validation keys
// from the data blob instead of nulling them.
func bulkResetConnectionData(data map[string]any) {
	data["testStatus"] = "active"
	data["status"] = "active"
	data["lastError"] = nil
	data["lastErrorAt"] = nil
	data["errorCode"] = nil
	data["backoffLevel"] = 0
	data["rateLimitedUntil"] = nil
	data["oauthLockedUntil"] = nil
	data["oauthFailureCount"] = 0
	data["lockedAllUntil"] = nil
	data["lockedToModel"] = nil
	data["lockedToModelUntil"] = nil
	data["modelLocks"] = map[string]any{}
	for k := range data {
		if strings.HasPrefix(k, modelLockKeyPrefix) {
			data[k] = nil
		}
	}
	delete(data, "disabledReason")
	delete(data, "disabledAt")
	delete(data, "disabledBy")
	delete(data, "verificationUrl")
	if psd := dataMap(data, "providerSpecificData"); psd != nil {
		for _, k := range []string{"refreshBlocked", "refreshBlockedAt", "validationUrl", "validationMessage", "validationAt"} {
			delete(psd, k)
		}
		data["providerSpecificData"] = psd
	}
	delete(data, "quotas")
}

type bulkBody struct {
	Provider              string   `json:"provider"`
	IDs                   []string `json:"ids"`
	Action                string   `json:"action"`
	ProxyPoolID           string   `json:"proxyPoolId"`
	ProxyGroup            *string  `json:"proxyGroup"`
	ProxyRotationStrategy string   `json:"proxyRotationStrategy"`
	ProxyPoolIDs          []string `json:"proxyPoolIds"`
	ActivePoolIDs         []string `json:"activePoolIds"`
}

func readBulkBody(w http.ResponseWriter, r *http.Request, errStatus int, errMessage string) (*bulkBody, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		handlerutil.WriteJSON(w, errStatus, map[string]any{"error": errMessage})
		return nil, false
	}
	defer r.Body.Close()
	var body bulkBody
	if err := json.Unmarshal(raw, &body); err != nil {
		handlerutil.WriteJSON(w, errStatus, map[string]any{"error": errMessage})
		return nil, false
	}
	return &body, true
}

// HandleBulkResetStatus serves POST /api/providers/bulk-reset-status.
func (h *DashboardHandler) HandleBulkResetStatus(w http.ResponseWriter, r *http.Request) {
	body, ok := readBulkBody(w, r, http.StatusInternalServerError, "Failed to reset connection status")
	if !ok {
		return
	}
	if body.Provider == "" && len(body.IDs) == 0 {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "provider or ids is required"})
		return
	}
	provider := body.Provider
	if provider != "" {
		provider = providers.ResolveAlias(provider)
	}
	conns, err := h.bulkTargetConnections(provider, body.IDs)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset connection status"})
		return
	}
	onlyCooldown := strings.EqualFold(body.Action, "cooldown")
	count := 0
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		data := connDataMap(conn)
		if onlyCooldown && (conn.IsActive == 0 || storedConnectionDisabled(data)) {
			// A cooldown reset must never revive an inactive or disabled
			// account, however it was targeted — provider sweep or
			// explicit id list alike (fragment 973; the id-list form
			// previously reactivated a ToS-disabled account and wiped
			// its verdict). Reactivation stays the full reset's
			// deliberate job.
			continue
		}
		bulkResetConnectionData(data)
		if err := h.persistConnectionData(conn, data); err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset connection status"})
			return
		}
		if !onlyCooldown || conn.IsActive != 0 {
			if err := h.Repo.SetConnectionStatus(conn.ID, true); err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset connection status"})
				return
			}
		}
		count++
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count})
}

// HandleBulkProxy serves POST /api/providers/bulk-proxy: bulk proxy binding
// over a provider's connections (or an explicit id list), written into
// data.providerSpecificData exactly as AxonRouter's bulkUpdateProviderProxy.
func (h *DashboardHandler) HandleBulkProxy(w http.ResponseWriter, r *http.Request) {
	body, ok := readBulkBody(w, r, http.StatusInternalServerError, "Failed to update proxy")
	if !ok {
		return
	}
	if body.Provider == "" && len(body.IDs) == 0 {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "provider or ids is required"})
		return
	}
	if body.Action == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "action is required"})
		return
	}
	provider := body.Provider
	if provider != "" {
		provider = providers.ResolveAlias(provider)
	}

	conns, err := h.bulkTargetConnections(provider, body.IDs)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update proxy"})
		return
	}

	strategy := body.ProxyRotationStrategy
	if strategy == "" {
		strategy = "round-robin"
	}

	apply := func(conn *models.ProviderConnection, mutate func(psd map[string]any)) error {
		data := connDataMap(conn)
		psd := dataMap(data, "providerSpecificData")
		if psd == nil {
			psd = map[string]any{}
		}
		mutate(psd)
		data["providerSpecificData"] = psd
		return h.persistConnectionData(conn, data)
	}

	updated := 0
	switch body.Action {
	case "one-to-one":
		if len(body.ActivePoolIDs) == 0 {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "No active proxy pools provided"})
			return
		}
		// Round-robin assignment in (priority ASC, created_at ASC) order —
		// priority NULLs last, matching the Postgres original.
		sort.SliceStable(conns, func(i, j int) bool {
			pi, pj := conns[i].Priority, conns[j].Priority
			if (pi == nil) != (pj == nil) {
				return pj == nil
			}
			if pi != nil && pj != nil && *pi != *pj {
				return *pi < *pj
			}
			return conns[i].CreatedAt < conns[j].CreatedAt
		})
		for i, conn := range conns {
			poolID := body.ActivePoolIDs[i%len(body.ActivePoolIDs)]
			if err := apply(conn, func(psd map[string]any) {
				delete(psd, "proxyPoolIds")
				delete(psd, "proxyRotationStrategy")
				delete(psd, "proxyGroup")
				psd["proxyPoolId"] = poolID
			}); err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update proxy"})
				return
			}
			updated++
		}
	case "group":
		for _, conn := range conns {
			if err := apply(conn, func(psd map[string]any) {
				delete(psd, "proxyPoolId")
				delete(psd, "proxyPoolIds")
				if body.ProxyGroup != nil {
					psd["proxyGroup"] = *body.ProxyGroup
				} else {
					psd["proxyGroup"] = nil
				}
				psd["proxyRotationStrategy"] = strategy
				psd["proxyPoolIds"] = []any{}
			}); err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update proxy"})
				return
			}
			updated++
		}
	case "strategy":
		poolIDs := make([]any, 0, len(body.ProxyPoolIDs))
		for _, id := range body.ProxyPoolIDs {
			poolIDs = append(poolIDs, id)
		}
		for _, conn := range conns {
			if err := apply(conn, func(psd map[string]any) {
				delete(psd, "proxyPoolId")
				delete(psd, "proxyGroup")
				psd["proxyPoolIds"] = poolIDs
				psd["proxyRotationStrategy"] = strategy
			}); err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update proxy"})
				return
			}
			updated++
		}
	case "unbind":
		for _, conn := range conns {
			if err := apply(conn, func(psd map[string]any) {
				for _, k := range []string{"proxyPoolId", "proxyPoolIds", "proxyRotationStrategy", "proxyGroup", "connectionProxyEnabled", "connectionProxyUrl", "connectionNoProxy"} {
					delete(psd, k)
				}
			}); err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update proxy"})
				return
			}
			updated++
		}
	case "single":
		if body.ProxyPoolID == "" {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "proxyPoolId is required for single proxy action"})
			return
		}
		for _, conn := range conns {
			if err := apply(conn, func(psd map[string]any) {
				delete(psd, "proxyPoolIds")
				delete(psd, "proxyRotationStrategy")
				delete(psd, "proxyGroup")
				psd["proxyPoolId"] = body.ProxyPoolID
			}); err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update proxy"})
				return
			}
			updated++
		}
	default:
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Unsupported bulk proxy action: " + body.Action})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "updatedCount": updated})
}
