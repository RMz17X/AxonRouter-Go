package dashboard

import (
	"net/http"
	"strings"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/providers"
)

// HandleProviderProxyResolution handles GET /api/providers/proxy-resolution?id=<provider>:
// a read-only readout of what the provider's stored proxy strategy actually
// resolves to right now — the selector the chat path will use and the active
// pools it matches.
//
// It exists because every proxy-binding bug in this area shared one shape:
// the dashboard showed a binding as set while the resolver matched zero
// pools and requests silently went direct. Surfacing the resolution next to
// the control makes that state visible at a glance. The selector derivation
// mirrors chat.resolveProviderProxyPoolID (same strategy-key walk, same
// "__none__"/"none" sentinels, same legacy bare-type heal); candidate
// matching is the production Repo.ActivePoolsByTarget itself. Per-connection
// bindings are not covered: this reports the provider-level strategy a
// connection without its own binding inherits.
func (h *DashboardHandler) HandleProviderProxyResolution(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.URL.Query().Get("id"))
	if provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing id")
		return
	}

	out := map[string]any{
		"providerId":     provider,
		"binding":        "none",
		"selector":       "",
		"candidates":     []map[string]any{},
		"candidateCount": 0,
		"resolves":       false,
	}
	if h.Repo == nil {
		handlerutil.WriteJSON(w, http.StatusOK, out)
		return
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil || settings.ProviderStrategies == nil {
		handlerutil.WriteJSON(w, http.StatusOK, out)
		return
	}

	// Strategy keys: the provider id, its alias, and its canonical id —
	// the same walk providerStrategyKeys performs in the chat package.
	keys := []string{provider}
	seen := map[string]bool{provider: true}
	for _, key := range []string{providers.GetProviderAlias(provider), providers.ResolveAlias(provider)} {
		if key != "" && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}

	var singleID, proxyGroup, rotate string
	explicitDirect := false
	for _, key := range keys {
		strat, ok := settings.ProviderStrategies[key]
		if !ok {
			continue
		}
		if strat.ProxyPoolID == "__none__" {
			explicitDirect = true
		}
		if singleID == "" && strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
			singleID = strat.ProxyPoolID
		}
		if proxyGroup == "" && strat.ProxyGroup != "" && strat.ProxyGroup != "none" {
			proxyGroup = strat.ProxyGroup
		}
		if rotate == "" && db.IsProxyPoolRotation(strat.ProxyRotateStrategy) {
			rotate = strat.ProxyRotateStrategy
		}
	}

	selector := singleID
	if selector == "" && proxyGroup != "" {
		// The same derivation the chat resolver applies
		// (db.NormalizeProxyTarget): per comma-separated part, so a
		// multi binding reads out as the union the chat path will use.
		selector = db.NormalizeProxyTarget(proxyGroup)
	}

	switch {
	case selector != "":
		pools := h.Repo.ActivePoolsByTarget(selector)
		candidates := make([]map[string]any, 0, len(pools))
		for _, p := range pools {
			candidates = append(candidates, map[string]any{
				"id": p.ID, "name": p.Name, "type": p.Type, "group": p.Group,
			})
		}
		// The binding label names the selector's shape: a single kind
		// keeps its name ("group" also when every part of a multi
		// selector is a group), a mixed multi selector is "multi".
		binding := "pool"
		kinds := map[string]bool{}
		for _, part := range db.SplitProxyTarget(selector) {
			switch {
			case strings.HasPrefix(part, "group:"):
				kinds["group"] = true
			case strings.HasPrefix(part, "type:"):
				kinds["type"] = true
			default:
				kinds["pool"] = true
			}
		}
		if len(kinds) == 1 {
			for kind := range kinds {
				binding = kind
			}
		} else if len(kinds) > 1 {
			binding = "multi"
		}
		out["binding"] = binding
		out["selector"] = selector
		out["candidates"] = candidates
		out["candidateCount"] = len(candidates)
		out["resolves"] = len(candidates) > 0
	case rotate != "" && providers.IsNoAuthProvider(provider):
		// Pool rotation (NoAuth providers): the request-time pick rotates
		// across every active pool, so the candidate set is all of them.
		active := 0
		if pools, err := h.Repo.ListProxyPools(); err == nil {
			for _, p := range pools {
				if on, _ := p["isActive"].(bool); on {
					active++
				}
			}
		}
		out["binding"] = "rotation"
		out["candidateCount"] = active
		out["resolves"] = active > 0
	case explicitDirect:
		out["binding"] = "direct"
		out["resolves"] = true
	}

	handlerutil.WriteJSON(w, http.StatusOK, out)
}
