package db

import (
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// ProxyGroup is one custom proxy group — the Go counterpart of AxonRouter's
// proxy_groups row (src/lib/db/repos/proxyGroupsRepo.js). The canonical
// fields live in typed form here; any extra keys an operator or the upstream
// dashboard wrote into the payload ride along in Data and are spread back
// into the API object on read, exactly like upstream's rowToGroup.
type ProxyGroup struct {
	ID              string
	Name            string
	Description     string
	IsSticky        bool
	StickyLimit     int
	PoolIDs         []string
	FetchURL        *string
	FetchIntervalMs int
	LastFetchedAt   *string
	Data            map[string]any
	CreatedAt       string
	UpdatedAt       string
}

// ToMap renders the group the way upstream's rowToGroup does: the stored
// payload first, canonical fields overriding it.
func (g *ProxyGroup) ToMap() map[string]any {
	out := make(map[string]any, len(g.Data)+12)
	for k, v := range g.Data {
		out[k] = v
	}
	out["id"] = g.ID
	out["name"] = g.Name
	out["description"] = g.Description
	out["isSticky"] = g.IsSticky
	out["stickyLimit"] = g.StickyLimit
	poolIDs := g.PoolIDs
	if poolIDs == nil {
		poolIDs = []string{}
	}
	out["poolIds"] = poolIDs
	if g.FetchURL != nil {
		out["fetchUrl"] = *g.FetchURL
	} else {
		out["fetchUrl"] = nil
	}
	out["fetchIntervalMs"] = g.FetchIntervalMs
	if g.LastFetchedAt != nil {
		out["lastFetchedAt"] = *g.LastFetchedAt
	} else {
		out["lastFetchedAt"] = nil
	}
	out["createdAt"] = g.CreatedAt
	out["updatedAt"] = g.UpdatedAt
	return out
}

// proxyGroupFromParts rebuilds a group from its stored payload + columns,
// applying upstream's read-side defaults (stickyLimit 3 when not positive,
// fetchIntervalMs 600000 when below the 10s floor).
func proxyGroupFromParts(id, name, dataStr, createdAt, updatedAt string) *ProxyGroup {
	raw := map[string]any{}
	if dataStr != "" {
		_ = json.Unmarshal([]byte(dataStr), &raw)
	}
	g := &ProxyGroup{ID: id, Name: name, CreatedAt: createdAt, UpdatedAt: updatedAt, Data: raw}
	g.Description = handlerutil.GetString(raw, "description")
	if v, ok := raw["isSticky"].(bool); ok {
		g.IsSticky = v
	}
	g.StickyLimit = 3
	if v, ok := raw["stickyLimit"].(float64); ok && v > 0 {
		g.StickyLimit = int(v)
	}
	if arr, ok := raw["poolIds"].([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				g.PoolIDs = append(g.PoolIDs, s)
			}
		}
	}
	if g.PoolIDs == nil {
		g.PoolIDs = []string{}
	}
	if s, ok := raw["fetchUrl"].(string); ok && s != "" {
		g.FetchURL = &s
	}
	g.FetchIntervalMs = 600000
	if v, ok := raw["fetchIntervalMs"].(float64); ok && v >= 10000 {
		g.FetchIntervalMs = int(v)
	}
	if s, ok := raw["lastFetchedAt"].(string); ok && s != "" {
		g.LastFetchedAt = &s
	}
	return g
}

// proxyGroupData marshals the payload column for a group.
func proxyGroupData(g *ProxyGroup) (string, error) {
	payload := g.ToMap()
	delete(payload, "id")
	delete(payload, "createdAt")
	delete(payload, "updatedAt")
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal proxy group: %w", err)
	}
	return string(b), nil
}

const proxyGroupColumns = `id, name, data, createdAt, updatedAt`

func scanProxyGroup(row interface{ Scan(...any) error }) (*ProxyGroup, error) {
	var id, name, dataStr, createdAt, updatedAt string
	if err := row.Scan(&id, &name, &dataStr, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	return proxyGroupFromParts(id, name, dataStr, createdAt, updatedAt), nil
}

// ListProxyGroups returns every custom group, newest activity first
// (upstream order: updated_at DESC, created_at DESC).
func (r *Repo) ListProxyGroups() ([]*ProxyGroup, error) {
	rows, err := r.Query(`SELECT ` + proxyGroupColumns + ` FROM proxyGroups ORDER BY updatedAt DESC, createdAt DESC`)
	if err != nil {
		return nil, fmt.Errorf("list proxy groups: %w", err)
	}
	defer rows.Close()
	groups := []*ProxyGroup{}
	for rows.Next() {
		g, err := scanProxyGroup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan proxy group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy groups: %w", err)
	}
	return groups, nil
}

// GetProxyGroupByID returns the group or (nil, nil) when absent.
func (r *Repo) GetProxyGroupByID(id string) (*ProxyGroup, error) {
	if id == "" {
		return nil, nil
	}
	g, err := scanProxyGroup(r.QueryRow(`SELECT `+proxyGroupColumns+` FROM proxyGroups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get proxy group %s: %w", id, err)
	}
	return g, nil
}

// GetProxyGroupByName returns the group whose name matches case-insensitively
// (upstream: WHERE LOWER(name) = LOWER($1)), or (nil, nil) when absent.
func (r *Repo) GetProxyGroupByName(name string) (*ProxyGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	g, err := scanProxyGroup(r.QueryRow(`SELECT `+proxyGroupColumns+` FROM proxyGroups WHERE LOWER(name) = LOWER(?)`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get proxy group by name %q: %w", name, err)
	}
	return g, nil
}

// CreateProxyGroup inserts a group, assigning an id when the caller left it
// empty. The returned group carries the stored defaults.
func (r *Repo) CreateProxyGroup(g *ProxyGroup) (*ProxyGroup, error) {
	if strings.TrimSpace(g.Name) == "" {
		return nil, fmt.Errorf("group name is required")
	}
	if g.ID == "" {
		g.ID = randomID()
	}
	now := time.Now().UTC().Format(time.RFC3339)
	g.CreatedAt = now
	g.UpdatedAt = now
	if g.StickyLimit <= 0 {
		g.StickyLimit = 3
	}
	if g.FetchIntervalMs < 10000 {
		g.FetchIntervalMs = 600000
	}
	if g.PoolIDs == nil {
		g.PoolIDs = []string{}
	}
	dataStr, err := proxyGroupData(g)
	if err != nil {
		return nil, err
	}
	if _, err := r.Exec(
		`INSERT INTO proxyGroups (id, name, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?)`,
		g.ID, g.Name, dataStr, g.CreatedAt, g.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert proxy group: %w", err)
	}
	return g, nil
}

// NormalizeProxyGroupPatch applies upstream normalizePatch semantics to a raw
// patch map: trims strings, floors numeric fields, dedupes poolIds, and maps
// an empty fetchUrl to null. Unknown keys are dropped.
func NormalizeProxyGroupPatch(data map[string]any) map[string]any {
	patch := map[string]any{}
	if _, ok := data["name"]; ok {
		patch["name"] = strings.TrimSpace(handlerutil.GetString(data, "name"))
	}
	if _, ok := data["description"]; ok {
		patch["description"] = strings.TrimSpace(handlerutil.GetString(data, "description"))
	}
	if _, ok := data["isSticky"]; ok {
		b, _ := data["isSticky"].(bool)
		patch["isSticky"] = b
	}
	if _, ok := data["stickyLimit"]; ok {
		patch["stickyLimit"] = normalizeStickyLimit(data["stickyLimit"])
	}
	if _, ok := data["poolIds"]; ok {
		patch["poolIds"] = normalizePoolIDs(data["poolIds"])
	}
	if _, ok := data["fetchUrl"]; ok {
		if s := strings.TrimSpace(handlerutil.GetString(data, "fetchUrl")); s != "" {
			patch["fetchUrl"] = s
		} else {
			patch["fetchUrl"] = nil
		}
	}
	if _, ok := data["fetchIntervalMs"]; ok {
		patch["fetchIntervalMs"] = normalizeFetchInterval(data["fetchIntervalMs"])
	}
	if _, ok := data["lastFetchedAt"]; ok {
		if s := handlerutil.GetString(data, "lastFetchedAt"); s != "" {
			patch["lastFetchedAt"] = s
		} else {
			patch["lastFetchedAt"] = nil
		}
	}
	if raw, ok := data["data"].(map[string]any); ok {
		patch["data"] = raw
	}
	return patch
}

func normalizeStickyLimit(v any) int {
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return 3
	}
	return int(f)
}

func normalizeFetchInterval(v any) int {
	f, ok := v.(float64)
	if !ok || f < 10000 {
		return 600000
	}
	return int(f)
}

func normalizePoolIDs(v any) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	// Callers pass either a JSON-decoded []any (dashboard patches) or a
	// []string (the sync reconciler); both are the same wire value.
	switch arr := v.(type) {
	case []any:
		for _, item := range arr {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, s := range arr {
			add(s)
		}
	}
	return out
}

// UpdateProxyGroup merges a normalized patch into the stored group and
// returns the result, or (nil, nil) when the group does not exist.
func (r *Repo) UpdateProxyGroup(id string, patch map[string]any) (*ProxyGroup, error) {
	existing, err := r.GetProxyGroupByID(id)
	if err != nil || existing == nil {
		return nil, err
	}
	merged := existing.ToMap()
	for k, v := range NormalizeProxyGroupPatch(patch) {
		if k == "data" {
			if extra, ok := v.(map[string]any); ok {
				if existing.Data == nil {
					existing.Data = map[string]any{}
				}
				for ek, ev := range extra {
					existing.Data[ek] = ev
				}
			}
			continue
		}
		merged[k] = v
	}
	updated := proxyGroupFromParts(id, handlerutil.GetString(merged, "name"), mustJSON(merged), existing.CreatedAt, time.Now().UTC().Format(time.RFC3339))
	updated.Data = existing.Data
	dataStr, err := proxyGroupData(updated)
	if err != nil {
		return nil, err
	}
	if _, err := r.Exec(
		`UPDATE proxyGroups SET name = ?, data = ?, updatedAt = ? WHERE id = ?`,
		updated.Name, dataStr, updated.UpdatedAt, id,
	); err != nil {
		return nil, fmt.Errorf("update proxy group %s: %w", id, err)
	}
	return updated, nil
}

func mustJSON(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// DeleteProxyGroup removes the group and returns it as it was, or (nil, nil)
// when absent.
func (r *Repo) DeleteProxyGroup(id string) (*ProxyGroup, error) {
	existing, err := r.GetProxyGroupByID(id)
	if err != nil || existing == nil {
		return nil, err
	}
	if _, err := r.Exec(`DELETE FROM proxyGroups WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("delete proxy group %s: %w", id, err)
	}
	return existing, nil
}

// CountProxyGroupBoundConnections mirrors upstream
// countProxyGroupBoundConnections: connections whose stored
// providerSpecificData.proxyGroup equals the given id-or-name value.
func (r *Repo) CountProxyGroupBoundConnections(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	conns, err := r.GetProviderConnections("", false)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range conns {
		if c == nil {
			continue
		}
		if psd := connectionPSD(c.Data); psd != nil && handlerutil.GetString(psd, "proxyGroup") == value {
			count++
		}
	}
	return count, nil
}

// ClearProxyGroupIDReferences removes providerSpecificData.proxyGroupId == id
// from every connection and returns how many connections were cleared
// (upstream remove-all route's connection pass).
func (r *Repo) ClearProxyGroupIDReferences(id string) (int, error) {
	conns, err := r.GetProviderConnections("", false)
	if err != nil {
		return 0, err
	}
	cleared := 0
	for _, c := range conns {
		if c == nil || c.Data == "" {
			continue
		}
		var dataMap map[string]any
		if err := json.Unmarshal([]byte(c.Data), &dataMap); err != nil || dataMap == nil {
			continue
		}
		psd, _ := dataMap["providerSpecificData"].(map[string]any)
		if psd == nil || handlerutil.GetString(psd, "proxyGroupId") != id {
			continue
		}
		delete(psd, "proxyGroupId")
		dataMap["providerSpecificData"] = psd
		newData, err := json.Marshal(dataMap)
		if err != nil {
			continue
		}
		if err := r.UpdateConnectionData(c.ID, string(newData)); err != nil {
			continue
		}
		cleared++
	}
	return cleared, nil
}

// connectionPSD extracts providerSpecificData from a connection's data JSON.
func connectionPSD(data string) map[string]any {
	if data == "" {
		return nil
	}
	var dataMap map[string]any
	if err := json.Unmarshal([]byte(data), &dataMap); err != nil {
		return nil
	}
	psd, _ := dataMap["providerSpecificData"].(map[string]any)
	return psd
}
