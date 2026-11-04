package dashboard

import (
	"net/http"
	"strconv"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// Single-group routes — port of AxonRouter's
// src/app/api/proxy-groups/[id]/route.js (GET/PUT/DELETE),
// [id]/sync/route.js, and [id]/remove-all/route.ts (the .ts-only route;
// its contract is followed as written).

// HandleGetProxyGroup handles GET /api/proxy-groups/{id}.
func (h *DashboardHandler) HandleGetProxyGroup(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	group, err := h.Repo.GetProxyGroupByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch proxy group"})
		return
	}
	if group == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Proxy group not found"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"group": group.ToMap()})
}

// HandleUpdateProxyGroup handles PUT /api/proxy-groups/{id}. For a default
// group id only the sticky settings are writable, and they live in
// settings.defaultProxyGroupSettings — upstream parity.
func (h *DashboardHandler) HandleUpdateProxyGroup(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	body, err := readGroupBody(r)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if defaultType := matchDefaultGroupType(id); defaultType != "" {
		isSticky, _ := body["isSticky"].(bool)
		stickyLimit := 3
		if v, ok := body["stickyLimit"].(float64); ok && v > 0 {
			stickyLimit = int(v)
		}
		next := h.defaultGroupSettings()
		next[defaultType] = map[string]any{"isSticky": isSticky, "stickyLimit": stickyLimit}
		if err := h.Repo.UpdateSettingsRaw(map[string]any{"defaultProxyGroupSettings": next}); err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"group": map[string]any{
				"id":          "default-" + defaultType,
				"key":         defaultType,
				"isDefault":   true,
				"isSticky":    isSticky,
				"stickyLimit": stickyLimit,
			},
		})
		return
	}

	existing, err := h.Repo.GetProxyGroupByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if existing == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Proxy group not found"})
		return
	}

	patch := map[string]any{}
	for _, key := range []string{"name", "description", "isSticky", "stickyLimit", "poolIds", "fetchUrl", "fetchIntervalMs"} {
		if v, ok := body[key]; ok {
			patch[key] = v
		}
	}
	if rawName, ok := patch["name"]; ok {
		name, _ := rawName.(string)
		name = strings.TrimSpace(name)
		if name == "" {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Group name cannot be empty"})
			return
		}
		if !strings.EqualFold(name, existing.Name) {
			collision, err := h.Repo.GetProxyGroupByName(name)
			if err != nil {
				handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if collision != nil && collision.ID != id {
				handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{"error": "Proxy group \"" + name + "\" already exists"})
				return
			}
		}
		patch["name"] = name
	}

	updated, err := h.Repo.UpdateProxyGroup(id, patch)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// Providing a fetchUrl re-syncs the group (upstream parity); a sync
	// failure is logged by the caller chain and does not fail the update.
	if rawURL, ok := patch["fetchUrl"]; ok {
		if s, _ := rawURL.(string); strings.TrimSpace(s) != "" {
			_, _ = h.Repo.SyncProxyGroupFromURL(r.Context(), id)
		}
	}
	finalGroup, err := h.Repo.GetProxyGroupByID(id)
	if err != nil || finalGroup == nil {
		finalGroup = updated
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"group": finalGroup.ToMap()})
}

// HandleDeleteProxyGroup handles DELETE /api/proxy-groups/{id}.
func (h *DashboardHandler) HandleDeleteProxyGroup(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if matchDefaultGroupType(id) != "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "System default groups cannot be deleted"})
		return
	}
	existing, err := h.Repo.GetProxyGroupByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if existing == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Proxy group not found"})
		return
	}
	boundByID, err := h.Repo.CountProxyGroupBoundConnections(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	boundByName, err := h.Repo.CountProxyGroupBoundConnections(existing.Name)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if total := boundByID + boundByName; total > 0 {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":      "Cannot delete group \"" + existing.Name + "\". It is currently in use by " + strconv.Itoa(total) + " connection(s).",
			"boundCount": total,
		})
		return
	}
	deleted, err := h.Repo.DeleteProxyGroup(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted.ToMap()})
}

// HandleSyncProxyGroup handles POST /api/proxy-groups/{id}/sync.
func (h *DashboardHandler) HandleSyncProxyGroup(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	group, err := h.Repo.GetProxyGroupByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to sync proxy group"})
		return
	}
	if group == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Proxy group not found"})
		return
	}
	if group.FetchURL == nil || *group.FetchURL == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Proxy group \"" + group.Name + "\" has no fetchUrl configured"})
		return
	}
	result, err := h.Repo.SyncProxyGroupFromURL(r.Context(), id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	updatedGroup := result.Group
	if updatedGroup == nil {
		updatedGroup = group
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"count":         result.Count,
		"group":         updatedGroup.ToMap(),
		"retainedCount": result.RetainedCount,
		"addedCount":    result.AddedCount,
		"removedCount":  result.RemovedCount,
	})
}

// HandleRemoveAllProxyGroup handles POST /api/proxy-groups/{id}/remove-all:
// clears the group assignment from every connection and from the provider
// proxy defaults in settings.
func (h *DashboardHandler) HandleRemoveAllProxyGroup(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	existing, err := h.Repo.GetProxyGroupByID(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to remove proxy group references"})
		return
	}
	if existing == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "Proxy group not found"})
		return
	}

	clearedConnections, err := h.Repo.ClearProxyGroupIDReferences(id)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to remove proxy group references"})
		return
	}

	clearedProviderDefaults := 0
	clearedProviderIds := []string{}
	if raw, err := h.Repo.GetSettingsRaw(); err == nil && raw != nil {
		defaults, _ := raw["providerProxyDefaults"].(map[string]any)
		if defaults != nil {
			updated := make(map[string]any, len(defaults))
			for providerID, cfgRaw := range defaults {
				cfg, _ := cfgRaw.(map[string]any)
				if cfg == nil || handlerutil.GetString(cfg, "proxyGroupId") != id {
					updated[providerID] = cfgRaw
					continue
				}
				rest := map[string]any{}
				for k, v := range cfg {
					if k != "proxyGroupId" {
						rest[k] = v
					}
				}
				if len(rest) > 0 {
					updated[providerID] = rest
				}
				clearedProviderDefaults++
				clearedProviderIds = append(clearedProviderIds, providerID)
			}
			if clearedProviderDefaults > 0 {
				// A failed settings write is logged upstream and swallowed;
				// the cleared counts still report what was attempted.
				_ = h.Repo.UpdateSettingsRaw(map[string]any{"providerProxyDefaults": updated})
			}
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":                 true,
		"clearedConnections":      clearedConnections,
		"clearedProviderDefaults": clearedProviderDefaults,
		"clearedProviderIds":      clearedProviderIds,
	})
}
