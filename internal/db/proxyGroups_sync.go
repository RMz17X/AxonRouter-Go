package db

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
)

// This file ports open-sse/services/proxyAutoFetcher.js: the proxy-list
// parser and the subscription reconciler that keeps a fetchUrl-backed
// group's pool set in step with the remote list (retain unchanged, insert
// new, delete obsolete).

const maxPoolsPerGroup = 5000

// ParseProxyLine parses one proxy line into a canonical URL string, or ""
// when the line is blank, a comment, or unparseable. Supported formats:
// scheme URLs, user:pass@host:port, host:port:user:pass, host:port.
func ParseProxyLine(raw string) string {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return ""
	}
	// Strip a trailing comment preceded by whitespace.
	for i := 0; i+1 < len(line); i++ {
		if (line[i] == ' ' || line[i] == '\t') && (line[i+1] == '#' || line[i+1] == '/') {
			line = strings.TrimSpace(line[:i])
			break
		}
	}
	if line == "" {
		return ""
	}

	// 1. Has a protocol scheme.
	if hasSchemePrefix(line) {
		if u, err := url.Parse(line); err == nil && u.Hostname() != "" {
			return strings.TrimSuffix(u.String(), "/")
		}
		return ""
	}

	// 2. user:pass@host:port
	if strings.Contains(line, "@") {
		if u, err := url.Parse("http://" + line); err == nil && u.Hostname() != "" && u.Port() != "" {
			return strings.TrimSuffix(u.String(), "/")
		}
		atIdx := strings.LastIndex(line, "@")
		creds, hostPort := line[:atIdx], line[atIdx+1:]
		if colonIdx := strings.Index(creds, ":"); colonIdx != -1 {
			user, pass := creds[:colonIdx], creds[colonIdx+1:]
			u := &url.URL{
				Scheme: "http",
				User:   url.UserPassword(user, pass),
				Host:   hostPort,
			}
			return u.String()
		}
	}

	// 3/4. Colon-separated forms.
	parts := strings.Split(line, ":")
	if len(parts) >= 4 && isNumeric(parts[1]) {
		u := &url.URL{
			Scheme: "http",
			User:   url.UserPassword(parts[2], strings.Join(parts[3:], ":")),
			Host:   parts[0] + ":" + parts[1],
		}
		return u.String()
	}
	if len(parts) == 2 && isNumeric(parts[1]) {
		u := &url.URL{Scheme: "http", Host: parts[0] + ":" + parts[1]}
		return u.String()
	}
	return ""
}

func hasSchemePrefix(s string) bool {
	idx := strings.Index(s, "://")
	if idx <= 0 {
		return false
	}
	for i, c := range s[:idx] {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) || c == '+' || c == '-' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseProxyList parses a subscription body (plaintext lines or a JSON
// array / {proxies|data: [...]} object) into deduped canonical proxy URLs.
func ParseProxyList(content string) []string {
	results := []string{}
	seen := map[string]bool{}
	add := func(candidate string) {
		parsed := ParseProxyLine(candidate)
		if parsed != "" && !seen[parsed] {
			seen[parsed] = true
			results = append(results, parsed)
		}
	}
	addItem := func(item any) {
		switch v := item.(type) {
		case string:
			add(v)
		case map[string]any:
			for _, key := range []string{"url", "proxy", "proxyUrl", "proxy_url"} {
				if s := handlerutil.GetString(v, key); s != "" {
					add(s)
					return
				}
			}
		}
	}

	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		var decoded any
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			switch v := decoded.(type) {
			case []any:
				for _, item := range v {
					addItem(item)
				}
				return results
			case map[string]any:
				list, _ := v["proxies"].([]any)
				if list == nil {
					list, _ = v["data"].([]any)
				}
				if len(list) > 0 {
					for _, item := range list {
						addItem(item)
					}
					return results
				}
			}
		}
		// Fall through to line-by-line on malformed JSON, like upstream.
	}
	for _, line := range strings.Split(content, "\n") {
		add(strings.TrimRight(line, "\r"))
	}
	return results
}

// ProxyGroupSyncResult is the reconciler's report (upstream sync shape).
type ProxyGroupSyncResult struct {
	Success       bool        `json:"success"`
	Count         int         `json:"count"`
	PoolIDs       []string    `json:"poolIds"`
	RetainedCount int         `json:"retainedCount"`
	AddedCount    int         `json:"addedCount"`
	RemovedCount  int         `json:"removedCount"`
	Group         *ProxyGroup `json:"-"`
}

// InsertProxyPoolWithID inserts a pool with a caller-chosen id (the group
// reconciler mints ids for fetched pools, like upstream's randomUUID).
func (r *Repo) InsertProxyPoolWithID(id string, d ProxyPoolData) error {
	now := time.Now().UTC().Format(time.RFC3339)
	dataBytes, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshal pool data: %w", err)
	}
	if _, err := r.Exec(
		`INSERT INTO proxyPools (id, isActive, testStatus, data, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?)`,
		id, 1, "unknown", string(dataBytes), now, now,
	); err != nil {
		return fmt.Errorf("insert proxy pool: %w", err)
	}
	r.invalidateActivePoolIDs()
	return nil
}

// poolPrimaryURL returns a pool row's canonical proxy URL (proxyUrl field,
// else the first entry of urls).
func poolPrimaryURL(pool map[string]any) string {
	if s := handlerutil.GetString(pool, "proxyUrl"); s != "" {
		return s
	}
	if arr, ok := pool["urls"].([]any); ok {
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// reconcileProxyGroup makes the group's pool set match fetchedURLs:
// pools whose URL survives are retained, vanished ones are deleted, new
// ones are inserted with the group id as their group label.
func (r *Repo) reconcileProxyGroup(group *ProxyGroup, fetchedURLs []string) (*ProxyGroupSyncResult, error) {
	allPools, err := r.ListProxyPools()
	if err != nil {
		return nil, err
	}
	memberIDs := map[string]bool{}
	for _, id := range group.PoolIDs {
		memberIDs[id] = true
	}
	var existing []map[string]any
	for _, p := range allPools {
		id := handlerutil.GetString(p, "id")
		if handlerutil.GetString(p, "group") == group.ID || memberIDs[id] {
			existing = append(existing, p)
		}
	}

	fetchedSet := map[string]bool{}
	uniqueFetched := []string{}
	for _, u := range fetchedURLs {
		if !fetchedSet[u] {
			fetchedSet[u] = true
			uniqueFetched = append(uniqueFetched, u)
		}
	}

	existingByURL := map[string]map[string]any{}
	var obsolete []map[string]any
	for _, p := range existing {
		u := poolPrimaryURL(p)
		if u != "" {
			if _, dup := existingByURL[u]; dup {
				obsolete = append(obsolete, p)
				continue
			}
			existingByURL[u] = p
		}
	}

	var retained []map[string]any
	for u, p := range existingByURL {
		if fetchedSet[u] {
			retained = append(retained, p)
		} else {
			obsolete = append(obsolete, p)
		}
	}

	var newURLs []string
	for _, u := range uniqueFetched {
		if _, ok := existingByURL[u]; !ok {
			newURLs = append(newURLs, u)
		}
	}

	newIDs := []string{}
	for i, u := range newURLs {
		poolType := "http"
		if parsed, err := url.Parse(u); err == nil && strings.HasPrefix(parsed.Scheme, "socks") {
			poolType = "socks5"
		}
		id := randomID()
		err := r.InsertProxyPoolWithID(id, ProxyPoolData{
			Name:     fmt.Sprintf("%s #%d", group.Name, i+1),
			ProxyURL: u,
			Group:    group.ID,
			Type:     poolType,
		})
		if err != nil {
			return nil, err
		}
		newIDs = append(newIDs, id)
	}

	for _, p := range obsolete {
		if id := handlerutil.GetString(p, "id"); id != "" {
			if err := r.DeleteProxyPool(id); err != nil {
				return nil, err
			}
		}
	}

	finalIDs := []string{}
	for _, p := range retained {
		finalIDs = append(finalIDs, handlerutil.GetString(p, "id"))
	}
	finalIDs = append(finalIDs, newIDs...)

	now := time.Now().UTC().Format(time.RFC3339)
	updated, err := r.UpdateProxyGroup(group.ID, map[string]any{
		"poolIds":       finalIDs,
		"lastFetchedAt": now,
	})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		updated = group
	}

	return &ProxyGroupSyncResult{
		Success:       true,
		Count:         len(finalIDs),
		PoolIDs:       finalIDs,
		RetainedCount: len(retained),
		AddedCount:    len(newIDs),
		RemovedCount:  len(obsolete),
		Group:         updated,
	}, nil
}

// SyncProxyGroupFromURL fetches the group's subscription URL and reconciles
// its pools (upstream syncProxyGroupFromUrl, forced regardless of interval).
func (r *Repo) SyncProxyGroupFromURL(ctx context.Context, groupID string) (*ProxyGroupSyncResult, error) {
	group, err := r.GetProxyGroupByID(groupID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return nil, fmt.Errorf("Proxy group not found: %s", groupID)
	}
	if group.FetchURL == nil || *group.FetchURL == "" {
		return nil, fmt.Errorf("Proxy group %q has no fetch_url configured", group.Name)
	}
	fetchURL := *group.FetchURL

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AxonRouter-ProxyAutoFetcher/1.0")
	req.Header.Set("Accept", "text/plain, application/json, */*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch proxies from %s: %w", fetchURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Failed to fetch proxies from %s: HTTP %d %s", fetchURL, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	fetched := ParseProxyList(string(body))
	if len(fetched) > maxPoolsPerGroup {
		fetched = fetched[:maxPoolsPerGroup]
	}
	return r.reconcileProxyGroup(group, fetched)
}
