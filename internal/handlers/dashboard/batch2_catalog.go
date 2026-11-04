package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// The model-catalog routes of batch 2: provider-models (aggregate view),
// {id}/sync-models (import a connection's live model list), and
// noauth-sync (refresh no-auth providers' discovered models).
//
// AxonRouter keeps synced models in one JSON document keyed by provider
// then connection. Here they live in the kv store under the scope
// "syncedAvailableModels", one row per provider whose value is the
// {connectionId: [models]} object — the batch-1 kv precedent for storage
// that has no table of its own.

const syncedModelsScope = "syncedAvailableModels"
const noauthConnectionID = "__noauth__"

func (h *DashboardHandler) loadSyncedModelsFor(providerID string) map[string][]map[string]any {
	scope, err := h.Repo.GetKVScope(syncedModelsScope)
	if err != nil {
		return map[string][]map[string]any{}
	}
	raw := scope[providerID]
	if raw == "" {
		return map[string][]map[string]any{}
	}
	out := map[string][]map[string]any{}
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		return map[string][]map[string]any{}
	}
	return out
}

func (h *DashboardHandler) loadAllSyncedModels() map[string]map[string][]map[string]any {
	scope, err := h.Repo.GetKVScope(syncedModelsScope)
	if err != nil {
		return map[string]map[string][]map[string]any{}
	}
	out := make(map[string]map[string][]map[string]any, len(scope))
	for providerID, raw := range scope {
		byConn := map[string][]map[string]any{}
		if json.Unmarshal([]byte(raw), &byConn) == nil && byConn != nil {
			out[providerID] = byConn
		}
	}
	return out
}

// storeSyncedModels replaces one connection's synced list; an empty list
// removes the connection entry, and the provider row when it empties.
func (h *DashboardHandler) storeSyncedModels(providerID, connectionID string, models []map[string]any) error {
	current := h.loadSyncedModelsFor(providerID)
	if len(models) == 0 {
		delete(current, connectionID)
	} else {
		current[connectionID] = models
	}
	if len(current) == 0 {
		return h.Repo.DeleteKV(syncedModelsScope, providerID)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	return h.Repo.SetKV(syncedModelsScope, providerID, string(encoded))
}

// ---------------------------------------------------------------------------
// Discovered-model normalization (AxonRouter importManagedModels /
// normalizeDiscoveredModels).
// ---------------------------------------------------------------------------

func firstModelString(values ...any) string {
	for _, v := range values {
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
	}
	return ""
}

func normalizeDiscoveredModels(models []any) []map[string]any {
	byID := map[string]map[string]any{}
	order := []string{}
	for _, item := range models {
		m, ok := item.(map[string]any)
		if !ok || m == nil {
			continue
		}
		id := firstModelString(m["id"], m["name"], m["model"])
		if id == "" {
			continue
		}
		name := firstModelString(m["name"], m["displayName"], m["model"])
		if name == "" {
			name = id
		}
		normalized := map[string]any{"id": id, "name": name, "source": "imported"}
		if v, ok := m["apiFormat"].(string); ok && v != "" {
			normalized["apiFormat"] = v
		}
		if rawList, isList := m["supportedEndpoints"].([]any); isList {
			seen := map[string]bool{}
			endpoints := []string{}
			for _, e := range rawList {
				if s, ok := e.(string); ok {
					if s = strings.TrimSpace(s); s != "" && !seen[s] {
						seen[s] = true
						endpoints = append(endpoints, s)
					}
				}
			}
			sort.Strings(endpoints)
			if len(endpoints) > 0 {
				list := make([]any, len(endpoints))
				for i, e := range endpoints {
					list[i] = e
				}
				normalized["supportedEndpoints"] = list
			}
		}
		if v, ok := m["inputTokenLimit"].(float64); ok {
			normalized["inputTokenLimit"] = v
		}
		if v, ok := m["outputTokenLimit"].(float64); ok {
			normalized["outputTokenLimit"] = v
		}
		if v, ok := m["description"].(string); ok {
			normalized["description"] = v
		}
		if m["supportsThinking"] == true {
			normalized["supportsThinking"] = true
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
		}
		byID[id] = normalized
	}
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// normalizeManagedSource mirrors AxonRouter's source normalization for
// managed models: api-sync / auto-sync / imported collapse to "imported".
func normalizeManagedSource(value any) string {
	s, _ := value.(string)
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "api-sync", "auto-sync", "imported":
		return "imported"
	}
	if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
		return s
	}
	return "manual"
}

func managedComparable(m map[string]any) map[string]any {
	name := firstModelString(m["name"])
	id := firstModelString(m["id"])
	if name == "" {
		name = id
	}
	apiFormat := dataStr(m, "apiFormat")
	if apiFormat == "" {
		apiFormat = "chat-completions"
	}
	comparable := map[string]any{
		"id":        id,
		"name":      name,
		"source":    normalizeManagedSource(m["source"]),
		"apiFormat": apiFormat,
	}
	endpoints := []string{"chat"}
	if rawList, ok := m["supportedEndpoints"].([]any); ok {
		seen := map[string]bool{}
		list := []string{}
		for _, e := range rawList {
			if s, ok := e.(string); ok && s != "" && !seen[s] {
				seen[s] = true
				list = append(list, s)
			}
		}
		if len(list) > 0 {
			sort.Strings(list)
			endpoints = list
		}
	}
	ep := make([]any, len(endpoints))
	for i, e := range endpoints {
		ep[i] = e
	}
	comparable["supportedEndpoints"] = ep
	return comparable
}

func importedComparable(m map[string]any) map[string]any {
	comparable := managedComparable(m)
	if endpoints, ok := m["supportedEndpoints"].([]any); ok && len(endpoints) > 0 {
		comparable["supportedEndpoints"] = endpoints
	} else {
		delete(comparable, "supportedEndpoints")
	}
	for _, key := range []string{"inputTokenLimit", "outputTokenLimit", "description"} {
		if v, ok := m[key]; ok {
			comparable[key] = v
		}
	}
	if m["supportsThinking"] == true {
		comparable["supportsThinking"] = true
	}
	return comparable
}

func jsonDeepEqual(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ab) == string(bb)
}

// ---------------------------------------------------------------------------
// Aggregate (AxonRouter providerModelsAggregate.js)
// ---------------------------------------------------------------------------

func normalizeAggregateSource(value any, fallback string) string {
	s, _ := value.(string)
	s = strings.ToLower(strings.TrimSpace(s))
	if s != "" {
		return s
	}
	return fallback
}

func mergeModelListsByID(lists ...[]map[string]any) []map[string]any {
	byID := map[string]map[string]any{}
	order := []string{}
	for _, list := range lists {
		for _, m := range list {
			id := firstModelString(m["id"])
			if id == "" {
				continue
			}
			if existing, seen := byID[id]; seen {
				for k, v := range m {
					existing[k] = v
				}
			} else {
				cp := make(map[string]any, len(m))
				for k, v := range m {
					cp[k] = v
				}
				byID[id] = cp
				order = append(order, id)
			}
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// canonicalProviderIDForAlias inverts the published alias the way
// PROVIDER_ALIAS_TO_ID does: a known id wins, then the registry alias
// inversion, then the alias->id resolution table.
func canonicalProviderIDForAlias(alias string) string {
	if _, ok := providers.ProviderRegistry[alias]; ok {
		return alias
	}
	for id := range providers.ProviderRegistry {
		if providers.GetProviderAlias(id) == alias {
			return id
		}
	}
	if id, ok := providers.ProviderAliasMap[alias]; ok {
		return id
	}
	return alias
}

func (h *DashboardHandler) aggregateProviderModels() map[string][]map[string]any {
	syncedAll := h.loadAllSyncedModels()

	customByProvider := map[string][]map[string]any{}
	if customs, err := h.Repo.GetCustomModels(); err == nil {
		for _, cm := range customs {
			if cm == nil {
				continue
			}
			providerID := canonicalProviderIDForAlias(cm.ProviderAlias)
			encoded, mErr := json.Marshal(cm)
			if mErr != nil {
				continue
			}
			var asMap map[string]any
			if json.Unmarshal(encoded, &asMap) != nil || asMap == nil {
				continue
			}
			asMap["source"] = normalizeAggregateSource(asMap["source"], "custom")
			customByProvider[providerID] = append(customByProvider[providerID], asMap)
		}
	}

	providerIDs := map[string]bool{}
	for id := range providers.ProviderRegistry {
		providerIDs[id] = true
	}
	for id := range customByProvider {
		providerIDs[id] = true
	}
	for id := range syncedAll {
		providerIDs[id] = true
	}

	aggregate := map[string][]map[string]any{}
	for providerID := range providerIDs {
		systemModels := make([]map[string]any, 0)
		for _, modelID := range providers.GetProviderModels(providerID) {
			entry := map[string]any{"id": modelID, "name": modelID, "source": "system"}
			if kind := providers.GetProviderModelKind(providerID, modelID); kind != "" {
				entry["kind"] = kind
			}
			systemModels = append(systemModels, entry)
		}

		syncedFlat := make([]map[string]any, 0)
		byConn := syncedAll[providerID]
		connIDs := make([]string, 0, len(byConn))
		for connID := range byConn {
			connIDs = append(connIDs, connID)
		}
		sort.Strings(connIDs)
		flattened := map[string]map[string]any{}
		flatOrder := []string{}
		for _, connID := range connIDs {
			for _, m := range byConn[connID] {
				id := firstModelString(m["id"])
				if id == "" {
					continue
				}
				cp := make(map[string]any, len(m))
				for k, v := range m {
					cp[k] = v
				}
				cp["source"] = normalizeAggregateSource(m["source"], "imported")
				if existing, seen := flattened[id]; seen {
					for k, v := range cp {
						existing[k] = v
					}
				} else {
					flattened[id] = cp
					flatOrder = append(flatOrder, id)
				}
			}
		}
		for _, id := range flatOrder {
			syncedFlat = append(syncedFlat, flattened[id])
		}

		base := systemModels
		if meta, ok := providers.ProviderRegistry[providerID]; ok &&
			(meta.PassthroughModels || meta.ModelsFetcher != nil) && len(syncedFlat) > 0 {
			base = syncedFlat
		}
		aggregate[providerID] = mergeModelListsByID(base, customByProvider[providerID])
	}
	return aggregate
}

// GET /api/provider-models
func (h *DashboardHandler) HandleProviderModels(w http.ResponseWriter, r *http.Request) {
	aggregate := h.aggregateProviderModels()
	if provider := r.URL.Query().Get("provider"); provider != "" {
		models := aggregate[provider]
		if models == nil {
			models = []map[string]any{}
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"provider": provider, "models": models})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"models": aggregate})
}
