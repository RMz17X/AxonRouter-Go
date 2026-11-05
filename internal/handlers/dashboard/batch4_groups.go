package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// Proxy groups — port of AxonRouter's src/app/api/proxy-groups/route.js
// (which wins over the route.ts variant). A group is either one of the four
// system defaults (auto-groups over pools of one type, sticky settings kept
// in settings.defaultProxyGroupSettings) or a custom group stored in the
// proxyGroups table (see internal/db/proxyGroups.go).

type defaultProxyGroupDef struct {
	ID          string
	Key         string
	Name        string
	Type        string
	Description string
}

var defaultProxyGroups = []defaultProxyGroupDef{
	{ID: "default-cloudflare", Key: "cloudflare", Name: "Cloudflare Relay", Type: "cloudflare", Description: "Auto-groups all active Cloudflare relay pools"},
	{ID: "default-http", Key: "http", Name: "HTTP", Type: "http", Description: "Auto-groups all active HTTP proxy pools"},
	{ID: "default-vercel", Key: "vercel", Name: "Vercel", Type: "vercel", Description: "Auto-groups all active Vercel relay pools"},
	{ID: "default-deno", Key: "deno", Name: "Deno", Type: "deno", Description: "Auto-groups all active Deno relay pools"},
}

var reservedProxyGroupNames = map[string]bool{
	"cloudflare": true, "cloudflare relay": true, "http": true,
	"vercel": true, "deno": true,
	"default-cloudflare": true, "default-http": true,
	"default-vercel": true, "default-deno": true,
}

// matchDefaultGroupType maps a default group name/alias/id to its canonical
// proxy type (upstream connectionProxy.js): "cloudflare", "Cloudflare
// Relay", "cf", "default-cloudflare" → "cloudflare", and so on. Returns ""
// when the input names no default group.
func matchDefaultGroupType(input string) string {
	if input == "" {
		return ""
	}
	n := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(input)), "default-")
	switch n {
	case "cloudflare", "cloudflare relay", "cf":
		return "cloudflare"
	case "http", "httpp":
		return "http"
	case "vercel":
		return "vercel"
	case "deno", "dino":
		return "deno"
	}
	return ""
}

// defaultGroupSettings reads settings.defaultProxyGroupSettings as a map.
func (h *DashboardHandler) defaultGroupSettings() map[string]any {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil || raw == nil {
		return map[string]any{}
	}
	settings, _ := raw["defaultProxyGroupSettings"].(map[string]any)
	if settings == nil {
		return map[string]any{}
	}
	return settings
}

// HandleGetProxyGroups handles GET /api/proxy-groups.
func (h *DashboardHandler) HandleGetProxyGroups(w http.ResponseWriter, r *http.Request) {
	allPools, err := h.Repo.ListProxyPools()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch proxy groups"})
		return
	}
	customGroups, err := h.Repo.ListProxyGroups()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch proxy groups"})
		return
	}
	defaultSettings := h.defaultGroupSettings()

	activePoolIDs := map[string]bool{}
	for _, p := range allPools {
		if active, _ := p["isActive"].(bool); active {
			activePoolIDs[handlerutil.GetString(p, "id")] = true
		}
	}

	defaults := make([]any, 0, len(defaultProxyGroups))
	for _, def := range defaultProxyGroups {
		total, active := 0, 0
		for _, p := range allPools {
			if handlerutil.GetString(p, "type") != def.Type {
				continue
			}
			total++
			if isActive, _ := p["isActive"].(bool); isActive {
				active++
			}
		}
		cfg, _ := defaultSettings[def.Type].(map[string]any)
		isSticky, _ := cfg["isSticky"].(bool)
		stickyLimit := 3
		if v, ok := cfg["stickyLimit"].(float64); ok && v > 0 {
			stickyLimit = int(v)
		}
		defaults = append(defaults, map[string]any{
			"id":          def.ID,
			"key":         def.Key,
			"name":        def.Name,
			"type":        def.Type,
			"description": def.Description,
			"isDefault":   true,
			"isSticky":    isSticky,
			"stickyLimit": stickyLimit,
			"poolCount":   total,
			"activeCount": active,
		})
	}

	customs := make([]any, 0, len(customGroups))
	for _, g := range customGroups {
		enriched := g.ToMap()
		enriched["isDefault"] = false
		enriched["poolCount"] = len(g.PoolIDs)
		activeCount := 0
		for _, id := range g.PoolIDs {
			if activePoolIDs[id] {
				activeCount++
			}
		}
		enriched["activeCount"] = activeCount
		customs = append(customs, enriched)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"defaultGroups": defaults,
		"customGroups":  customs,
		"groups":        append(defaults, customs...),
	})
}

// readGroupBody parses a JSON request body into a map ({} when empty).
func readGroupBody(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	body := map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// HandleCreateProxyGroup handles POST /api/proxy-groups.
func (h *DashboardHandler) HandleCreateProxyGroup(w http.ResponseWriter, r *http.Request) {
	body, err := readGroupBody(r)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(handlerutil.GetString(body, "name"))
	if name == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Group name is required"})
		return
	}
	if reservedProxyGroupNames[strings.ToLower(name)] {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "\"" + name + "\" is a reserved system default group name"})
		return
	}
	existing, err := h.Repo.GetProxyGroupByName(name)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if existing != nil {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{"error": "Proxy group \"" + name + "\" already exists"})
		return
	}

	isSticky, _ := body["isSticky"].(bool)
	group := &db.ProxyGroup{
		Name:        name,
		Description: strings.TrimSpace(handlerutil.GetString(body, "description")),
		IsSticky:    isSticky,
	}
	if v, ok := body["stickyLimit"].(float64); ok && v > 0 {
		group.StickyLimit = int(v)
	}
	if _, ok := body["poolIds"]; ok {
		group.PoolIDs = db.NormalizeProxyGroupPatch(body)["poolIds"].([]string)
	}
	if s := strings.TrimSpace(handlerutil.GetString(body, "fetchUrl")); s != "" {
		group.FetchURL = &s
	}
	if v, ok := body["fetchIntervalMs"].(float64); ok && v >= 10000 {
		group.FetchIntervalMs = int(v)
	}

	created, err := h.Repo.CreateProxyGroup(group)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	// A group born with a fetchUrl syncs immediately; a failed first sync
	// does not fail the create (upstream logs and continues).
	if created.FetchURL != nil {
		_, _ = h.Repo.SyncProxyGroupFromURL(r.Context(), created.ID)
	}
	finalGroup, err := h.Repo.GetProxyGroupByID(created.ID)
	if err != nil || finalGroup == nil {
		finalGroup = created
	}
	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{"group": finalGroup.ToMap()})
}
