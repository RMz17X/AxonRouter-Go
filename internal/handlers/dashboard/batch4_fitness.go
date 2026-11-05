package dashboard

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/proxy/poolstate"
)

// Pool fitness + egress geo routes — port of AxonRouter's
// src/app/api/proxy-pools/fitness/route.js, fitness/clear-all/route.js,
// [id]/fitness/clear/route.js, and [id]/geo-probe/route.js, backed by the
// poolstate registry (open-sse/services/proxyPoolFitness.js + poolGeo.js).

// settingsFitnessStore persists the fitness snapshot in the settings row
// under the same key upstream uses.
type settingsFitnessStore struct{ repo *db.Repo }

func (s settingsFitnessStore) LoadPoolFitness() (poolstate.Snapshot, error) {
	raw, err := s.repo.GetSettingsRaw()
	if err != nil {
		return nil, err
	}
	out := poolstate.Snapshot{}
	top, _ := raw["proxyPoolFitness"].(map[string]any)
	for poolID, byScopeRaw := range top {
		byScope, _ := byScopeRaw.(map[string]any)
		if byScope == nil {
			continue
		}
		entry := map[string]poolstate.Mark{}
		for scope, markRaw := range byScope {
			mm, _ := markRaw.(map[string]any)
			if mm == nil {
				continue
			}
			until, _ := mm["until"].(float64)
			reason, _ := mm["reason"].(string)
			entry[scope] = poolstate.Mark{Until: int64(until), Reason: reason}
		}
		if len(entry) > 0 {
			out[poolID] = entry
		}
	}
	return out, nil
}

func (s settingsFitnessStore) SavePoolFitness(snap poolstate.Snapshot) error {
	top := map[string]any{}
	for poolID, byScope := range snap {
		inner := map[string]any{}
		for scope, mark := range byScope {
			inner[scope] = map[string]any{"until": mark.Until, "reason": mark.Reason}
		}
		top[poolID] = inner
	}
	return s.repo.UpdateSettingsRaw(map[string]any{"proxyPoolFitness": top})
}

func (h *DashboardHandler) fitnessStore() settingsFitnessStore {
	return settingsFitnessStore{repo: h.Repo}
}

// HandleGetProxyPoolsFitness handles GET /api/proxy-pools/fitness: one row
// per live (pool, scope) mark, joined with the pool identity and the
// cached egress geo, newest cooldown first.
func (h *DashboardHandler) HandleGetProxyPoolsFitness(w http.ResponseWriter, r *http.Request) {
	poolstate.EnsureHydrated(h.fitnessStore())
	snapshot := poolstate.SnapshotLive()

	byID := map[string]map[string]any{}
	if len(snapshot) > 0 {
		pools, err := h.Repo.ListProxyPools()
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to read proxy fitness"})
			return
		}
		for _, p := range pools {
			byID[handlerutil.GetString(p, "id")] = p
		}
	}

	now := time.Now().UnixMilli()
	rows := []any{}
	for poolID, byScope := range snapshot {
		pool := byID[poolID]
		for scope, info := range byScope {
			if info.Until <= now {
				continue
			}
			provider := scope
			var model any
			if sep := strings.Index(scope, "::"); sep >= 0 {
				provider = scope[:sep]
				if raw := scope[sep+2:]; raw != "*" && raw != "" {
					model = raw
				}
			}
			reason := info.Reason
			if reason == "" {
				reason = "blocked"
			}
			row := map[string]any{
				"poolId":     poolID,
				"poolName":   nil,
				"poolType":   nil,
				"group":      nil,
				"proxyUrl":   nil,
				"isActive":   nil,
				"egress":     poolstate.GetPoolGeo(poolID),
				"provider":   provider,
				"model":      model,
				"reason":     reason,
				"until":      info.Until,
				"cooldownMs": max(0, info.Until-now),
			}
			if pool != nil {
				row["poolName"] = nilOrString(handlerutil.GetString(pool, "name"))
				row["poolType"] = nilOrString(handlerutil.GetString(pool, "type"))
				row["group"] = nilOrString(handlerutil.GetString(pool, "group"))
				row["proxyUrl"] = nilOrString(poolPrimaryURLValue(pool))
				if active, ok := pool["isActive"].(bool); ok {
					row["isActive"] = active
				}
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].(map[string]any)["until"].(int64) > rows[j].(map[string]any)["until"].(int64)
	})
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": len(rows)})
}

func nilOrString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// HandleClearProxyPoolsFitness handles POST /api/proxy-pools/fitness/clear-all.
// Body { provider?: string } clears every mark, or only that provider's.
func (h *DashboardHandler) HandleClearProxyPoolsFitness(w http.ResponseWriter, r *http.Request) {
	poolstate.EnsureHydrated(h.fitnessStore())
	body, _ := readGroupBody(r)
	provider := ""
	if body != nil {
		provider = strings.TrimSpace(handlerutil.GetString(body, "provider"))
	}
	poolstate.ClearAll(provider)
	var providerOut any
	if provider != "" {
		providerOut = provider
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": providerOut})
}

// HandleClearProxyPoolFitness handles POST /api/proxy-pools/{id}/fitness/clear.
// Body { scope: "provider::model" } clears that pool+scope mark.
func (h *DashboardHandler) HandleClearProxyPoolFitness(w http.ResponseWriter, r *http.Request) {
	poolstate.EnsureHydrated(h.fitnessStore())
	id := getURLParam(r, "id")
	body, _ := readGroupBody(r)
	scope := ""
	if body != nil {
		scope = strings.TrimSpace(handlerutil.GetString(body, "scope"))
	}
	if id == "" || scope == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "pool id and scope are required"})
		return
	}
	poolstate.Clear(id, scope)
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "poolId": id, "scope": scope})
}

// HandleGeoProbeProxyPool handles POST /api/proxy-pools/{id}/geo-probe:
// probe the pool's egress geo on demand and cache it. Both success and
// probe failure answer HTTP 200 (upstream parity — a parked pool with an
// unreachable proxy is an expected failure, not a server error).
func (h *DashboardHandler) HandleGeoProbeProxyPool(w http.ResponseWriter, r *http.Request) {
	poolstate.EnsureHydrated(h.fitnessStore())
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "pool id is required"})
		return
	}
	pools, err := h.Repo.ListProxyPools()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to probe pool geo"})
		return
	}
	var pool map[string]any
	for _, p := range pools {
		if handlerutil.GetString(p, "id") == id {
			pool = p
			break
		}
	}
	if pool == nil {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{"error": "pool not found"})
		return
	}
	geo, failure := poolstate.ProbePoolGeo(r.Context(), poolPrimaryURLValue(pool), handlerutil.GetString(pool, "type"), 15*time.Second)
	if failure == nil {
		poolstate.SetPoolGeo(id, *geo)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "geo": geo})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": failure.Kind, "detail": failure.Detail})
}
