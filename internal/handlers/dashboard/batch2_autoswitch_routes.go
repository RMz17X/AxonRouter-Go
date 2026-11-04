package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
)

// Route handlers for the antigravity / codex auto-switch families
// (AxonRouter src/app/api/providers/{antigravity,codex}/auto-switch/*).
// Each path serves GET+PUT (settings, active) or POST (rotate); the
// method switch lives here so registration stays one line per path.

type autoSwitchFamily struct {
	provider       string
	label          string // "Antigravity CLI" | "Codex"
	settingsGetErr string
	settingsPutErr string
	activeGetErr   string
	activeSetErr   string
	notFoundErr    string
	rotateErr      string
	rotateNoneMsg  string
}

var antigravityFamily = autoSwitchFamily{
	provider:       antigravityProviderID,
	label:          "Antigravity CLI",
	settingsGetErr: "Failed to get Antigravity CLI auto-switch settings",
	settingsPutErr: "Failed to update Antigravity CLI auto-switch settings",
	activeGetErr:   "Failed to get active Antigravity CLI account",
	activeSetErr:   "Failed to set active Antigravity CLI account",
	notFoundErr:    "Connection not found or not a valid Antigravity account",
	rotateErr:      "Failed to rotate Antigravity CLI account",
	rotateNoneMsg:  "No rotation needed or no other account available",
}

var codexFamily = autoSwitchFamily{
	provider:       codexProviderID,
	label:          "Codex",
	settingsGetErr: "Failed to get Codex auto-switch settings",
	settingsPutErr: "Failed to update Codex auto-switch settings",
	activeGetErr:   "Failed to get active Codex account",
	activeSetErr:   "Failed to set active Codex account",
	notFoundErr:    "Connection not found or not a valid Codex account",
	rotateErr:      "Failed to rotate Codex account",
	rotateNoneMsg:  "No rotation needed or no other healthy account available",
}

func (h *DashboardHandler) autoSwitchSettings(w http.ResponseWriter, r *http.Request, fam autoSwitchFamily) {
	switch r.Method {
	case http.MethodGet:
		cfg := h.autoSwitchConfig(fam.provider)
		payload := map[string]any{
			"enabled":            cfg["enabled"] == true,
			"activeConnectionId": cfgString(cfg, "activeConnectionId"),
			"lastRotatedAt":      cfgString(cfg, "lastRotatedAt"),
			"lastRotatedFrom":    cfgString(cfg, "lastRotatedFrom"),
			"lastRotatedTo":      cfgString(cfg, "lastRotatedTo"),
		}
		if fam.provider == codexProviderID {
			payload["thresholdPercent"] = normalizeCodexThreshold(cfg["thresholdPercent"])
		}
		handlerutil.WriteJSON(w, http.StatusOK, payload)
	case http.MethodPut:
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.settingsPutErr})
			return
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body == nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.settingsPutErr})
			return
		}
		next := h.autoSwitchConfig(fam.provider)
		if next == nil {
			next = map[string]any{}
		}
		merged := make(map[string]any, len(next)+3)
		for k, v := range next {
			merged[k] = v
		}
		if v, present := body["enabled"]; present {
			merged["enabled"] = v == true
		}
		if fam.provider == codexProviderID {
			if v, present := body["thresholdPercent"]; present {
				merged["thresholdPercent"] = normalizeCodexThreshold(v)
			}
		}
		if v, present := body["activeConnectionId"]; present {
			if s, ok := v.(string); ok {
				merged["activeConnectionId"] = s
			} else {
				merged["activeConnectionId"] = nil
			}
		}
		if err := h.Repo.UpdateSettingsRaw(map[string]any{autoSwitchSettingsKey(fam.provider): merged}); err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.settingsPutErr})
			return
		}
		payload := map[string]any{
			"enabled":            merged["enabled"] == true,
			"activeConnectionId": cfgString(merged, "activeConnectionId"),
		}
		if fam.provider == codexProviderID {
			if _, present := merged["thresholdPercent"]; present {
				payload["thresholdPercent"] = normalizeCodexThreshold(merged["thresholdPercent"])
			}
		}
		handlerutil.WriteJSON(w, http.StatusOK, payload)
	default:
		handlerutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
	}
}

func (h *DashboardHandler) autoSwitchActive(w http.ResponseWriter, r *http.Request, fam autoSwitchFamily) {
	switch r.Method {
	case http.MethodGet:
		payload, conn, err := h.resolveAutoSwitchActive(fam.provider)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.activeGetErr})
			return
		}
		if fam.provider == codexProviderID {
			payload["planType"] = nil
			payload["remainingPercent"] = nil
			if conn != nil {
				psd := dataMap(connDataMap(conn), "providerSpecificData")
				if v, ok := psd["planType"]; ok {
					payload["planType"] = v
				}
				payload["remainingPercent"] = codexRemainingPercent(conn)
			}
		} else {
			payload["projectId"] = nil
			if conn != nil {
				psd := dataMap(connDataMap(conn), "providerSpecificData")
				if v, ok := psd["projectId"]; ok {
					payload["projectId"] = v
				}
			}
		}
		handlerutil.WriteJSON(w, http.StatusOK, payload)
	case http.MethodPut:
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.activeSetErr})
			return
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.activeSetErr})
			return
		}
		var connectionID *string
		if v, present := body["connectionId"]; present {
			if s, ok := v.(string); ok {
				connectionID = &s
			}
		}
		ok, setErr := h.setAutoSwitchActive(fam.provider, connectionID)
		if setErr != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.activeSetErr})
			return
		}
		if !ok {
			handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": fam.notFoundErr})
			return
		}
		var activeValue any
		if connectionID != nil {
			activeValue = *connectionID
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "activeConnectionId": activeValue})
	default:
		handlerutil.WriteJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Method not allowed"})
	}
}

func (h *DashboardHandler) autoSwitchRotate(w http.ResponseWriter, r *http.Request, fam autoSwitchFamily) {
	newID, err := h.checkAndRotateAutoSwitch(fam.provider)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": fam.rotateErr})
		return
	}
	if newID == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"rotated": false, "message": fam.rotateNoneMsg})
		return
	}
	payload := map[string]any{"rotated": true, "newConnectionId": newID}
	if fam.provider == codexProviderID {
		var name any
		if conns, listErr := h.Repo.GetProviderConnections(fam.provider, true); listErr == nil {
			if conn := findConnectionByID(conns, newID); conn != nil {
				if display := autoSwitchDisplayName(conn); display != "" {
					name = display
				}
			}
		}
		payload["newAccountName"] = name
	}
	handlerutil.WriteJSON(w, http.StatusOK, payload)
}

func (h *DashboardHandler) HandleAntigravityAutoSwitchSettings(w http.ResponseWriter, r *http.Request) {
	h.autoSwitchSettings(w, r, antigravityFamily)
}

func (h *DashboardHandler) HandleAntigravityAutoSwitchActive(w http.ResponseWriter, r *http.Request) {
	h.autoSwitchActive(w, r, antigravityFamily)
}

func (h *DashboardHandler) HandleAntigravityAutoSwitchRotate(w http.ResponseWriter, r *http.Request) {
	h.autoSwitchRotate(w, r, antigravityFamily)
}

func (h *DashboardHandler) HandleCodexAutoSwitchSettings(w http.ResponseWriter, r *http.Request) {
	h.autoSwitchSettings(w, r, codexFamily)
}

func (h *DashboardHandler) HandleCodexAutoSwitchActive(w http.ResponseWriter, r *http.Request) {
	h.autoSwitchActive(w, r, codexFamily)
}

func (h *DashboardHandler) HandleCodexAutoSwitchRotate(w http.ResponseWriter, r *http.Request) {
	h.autoSwitchRotate(w, r, codexFamily)
}
