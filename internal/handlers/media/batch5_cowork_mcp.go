package media

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/clisetup"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/proxy"
)

// Cowork MCP routes (batch 5): the Anthropic MCP registry browser and
// the per-server tools probe used by the Cowork settings card. Ports
// of src/app/api/cli-tools/cowork-mcp-registry/route.js and
// cowork-mcp-tools/route.js.

const (
	coworkRegistryURL       = "https://api.anthropic.com/mcp-registry/v0/servers"
	coworkRegistryVisibilty = "commercial,gsuite,gsuite-google"
	coworkRegistryCacheTTL  = time.Hour
	coworkProbeTimeout      = 8 * time.Second
)

// CoworkMcpHandler holds the registry cache (Axon keeps it in a
// globalThis slot with a one-hour TTL).
type CoworkMcpHandler struct {
	mu     sync.Mutex
	client *http.Client
	// guardedClient serves the tools probe for non-local callers: the
	// SSRF-safe transport re-validates the resolved address at dial
	// time, and every redirect hop is re-checked against the resolving
	// public-URL guard before it is followed (council round 1, S-4).
	guardedClient *http.Client
	cacheTS       time.Time
	cacheData     *clisetup.JObj // {servers, total} without the cached flag
}

// NewCoworkMcpHandler returns a CoworkMcpHandler.
func NewCoworkMcpHandler() *CoworkMcpHandler {
	return &CoworkMcpHandler{
		client: &http.Client{},
		guardedClient: &http.Client{
			Transport: proxy.NewSafeTransport(),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				return handlerutil.AssertPublicURL(req.URL.String())
			},
		},
	}
}

// --- registry --------------------------------------------------------

var (
	mcpClaudeRE    = regexp.MustCompile(`^https?://[^/]*\bmcp\.claude\.com\b`)
	anthropicMcpRE = regexp.MustCompile(`^https?://api\.anthropic\.com/mcp\b`)
	templateURLRE  = regexp.MustCompile(`[<{]`)
	httpsRE        = regexp.MustCompile(`^https://`)
)

// isDirectConnect filters out claude.ai-mediated servers (broken in
// 3p) and tenant-required entries, exactly like the JS predicate.
func isDirectConnect(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	if mcpClaudeRE.MatchString(rawURL) || anthropicMcpRE.MatchString(rawURL) {
		return false
	}
	if templateURLRE.MatchString(rawURL) {
		return false
	}
	return httpsRE.MatchString(rawURL)
}

// HandleRegistry serves GET /api/cli-tools/cowork-mcp-registry.
func (h *CoworkMcpHandler) HandleRegistry(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("refresh") == "1"

	h.mu.Lock()
	if !force && h.cacheData != nil && time.Since(h.cacheTS) < coworkRegistryCacheTTL {
		data := h.cacheData
		h.mu.Unlock()
		writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
			"cached", true,
			"servers", data.Get("servers"),
			"total", data.Get("total"),
		))
		return
	}
	h.mu.Unlock()

	servers, err := h.fetchAllServers(r.Context())
	if err != nil {
		writeOrderedJSON(w, http.StatusInternalServerError, clisetup.NewJObj(
			"error", err.Error(),
			"servers", []any{},
			"total", 0,
		))
		return
	}
	data := clisetup.NewJObj("servers", servers, "total", len(servers))
	h.mu.Lock()
	h.cacheTS = time.Now()
	h.cacheData = data
	h.mu.Unlock()
	writeOrderedJSON(w, http.StatusOK, clisetup.NewJObj(
		"cached", false,
		"servers", servers,
		"total", len(servers),
	))
}

// fetchAllServers pages the registry (limit 500, cursor-chained, at
// most 20 pages), keeping only direct-connect servers without
// required tenant fields, deduped by URL.
func (h *CoworkMcpHandler) fetchAllServers(ctx context.Context) ([]any, error) {
	out := []any{}
	cursor := ""
	for i := 0; i < 20; i++ {
		u := coworkRegistryURL + "?limit=500&visibility=" + coworkRegistryVisibilty
		if cursor != "" {
			u += "&cursor=" + urlQueryEscape(cursor)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := h.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			break
		}
		var page struct {
			Servers []struct {
				Server map[string]any `json:"server"`
				Meta   map[string]any `json:"_meta"`
			} `json:"servers"`
			Metadata struct {
				NextCursor string `json:"nextCursor"`
			} `json:"metadata"`
		}
		decodeErr := stdjson.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, item := range page.Servers {
			s := item.Server
			meta, _ := item.Meta["com.anthropic.api/mcp-registry"].(map[string]any)
			if meta == nil {
				meta = map[string]any{}
			}
			remotes, _ := s["remotes"].([]any)
			if len(remotes) == 0 {
				continue
			}
			remote, _ := remotes[0].(map[string]any)
			remoteURL, _ := remote["url"].(string)
			if remoteURL == "" || !isDirectConnect(remoteURL) {
				continue
			}
			if required, _ := meta["requiredFields"].([]any); len(required) > 0 {
				continue
			}
			transport := "http"
			if remote["type"] == "sse" {
				transport = "sse"
			}
			toolNames := []any{}
			if names, ok := meta["toolNames"].([]any); ok {
				toolNames = names
			}
			entry := clisetup.NewJObj()
			if v, ok := s["name"].(string); ok {
				entry.Set("name", v)
			}
			slug := metaString(meta, "slug")
			if slug == "" {
				slug, _ = s["name"].(string)
			}
			if slug != "" || meta["slug"] != nil {
				entry.Set("slug", slug)
			}
			title := metaString(s, "title")
			if title == "" {
				title = metaString(meta, "displayName")
			}
			if title == "" {
				title, _ = s["name"].(string)
			}
			if title != "" || s["title"] != nil {
				entry.Set("title", title)
			}
			description := metaString(s, "description")
			if description == "" {
				description = metaString(meta, "oneLiner")
			}
			entry.Set("description", description)
			entry.Set("url", remoteURL)
			entry.Set("transport", transport)
			entry.Set("oauth", !truthyJS(meta["isAuthless"]))
			entry.Set("toolNames", toolNames)
			entry.Set("toolCount", len(toolNames))
			if icon, ok := meta["iconUrl"]; ok {
				entry.Set("iconUrl", icon)
			} else {
				entry.Set("iconUrl", nil)
			}
			out = append(out, entry)
		}
		cursor = page.Metadata.NextCursor
		if cursor == "" {
			break
		}
	}
	// Dedupe by url, first occurrence wins.
	seen := map[string]bool{}
	deduped := out[:0]
	for _, e := range out {
		u, _ := e.(*clisetup.JObj).Get("url").(string)
		if seen[u] {
			continue
		}
		seen[u] = true
		deduped = append(deduped, e)
	}
	return deduped, nil
}

func metaString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// urlQueryEscape mirrors encodeURIComponent for the cursor param.
func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// --- tools probe -----------------------------------------------------

// HandleTools serves POST /api/cli-tools/cowork-mcp-tools: probe one
// MCP server (initialize + tools/list, no auth) and report its tools.
// Remote (non-local) callers are SSRF-guarded; local callers may probe
// self-hosted servers.
//
// The non-local guard is the product's full standard, in layers
// (council round 1, S-4): the literal-host check below stays as the
// fast path, handlerutil.AssertPublicURL then resolves the name and
// rejects private results, and the fetch itself runs through the
// guarded client, whose dial-time check defeats rebinding and whose
// redirect policy re-validates every hop. Before this fix the probe
// stopped at the literal check and fetched with a plain client, so a
// public name resolving to — or redirecting to — an internal address
// sailed through. The local-caller branch is unchanged: an operator
// probing their own MCP servers from this machine keeps the plain
// client and no public-URL requirement.
func (h *CoworkMcpHandler) HandleTools(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL any `json:"url"`
	}
	if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOrderedJSON(w, http.StatusInternalServerError, clisetup.NewJObj(
			"error", err.Error(),
			"tools", []any{},
		))
		return
	}
	rawURL, _ := body.URL.(string)
	if rawURL == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "url required"})
		return
	}
	client := h.client
	if !isLocalRequest(r) {
		if err := assertPublicURL(rawURL); err != nil {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "URL not allowed"})
			return
		}
		if err := handlerutil.AssertPublicURL(rawURL); err != nil {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "URL not allowed"})
			return
		}
		client = h.guardedClient
	}
	result := h.probeMcp(r.Context(), rawURL, client)
	writeOrderedJSON(w, http.StatusOK, result)
}

// probeMcp runs the three-step MCP handshake under one 8s budget.
func (h *CoworkMcpHandler) probeMcp(ctx context.Context, rawURL string, client *http.Client) *clisetup.JObj {
	ctx, cancel := context.WithTimeout(ctx, coworkProbeTimeout)
	defer cancel()

	headers := map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream",
		"MCP-Protocol-Version": "2025-06-18",
	}
	post := func(payload string, hdrs map[string]string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewBufferString(payload))
		if err != nil {
			return nil, err
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		return client.Do(req)
	}
	fail := func(err error) *clisetup.JObj {
		msg := err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = "timeout"
		}
		return clisetup.NewJObj("error", msg, "tools", []any{})
	}

	// Step 1: initialize.
	initRes, err := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"axonrouter","version":"1"}}}`, headers)
	if err != nil {
		return fail(err)
	}
	if initRes.StatusCode == 401 || initRes.StatusCode == 403 {
		initRes.Body.Close()
		return clisetup.NewJObj("requiresAuth", true, "tools", []any{})
	}
	if initRes.StatusCode < 200 || initRes.StatusCode >= 300 {
		initRes.Body.Close()
		return clisetup.NewJObj("error", fmt.Sprintf("init %d", initRes.StatusCode), "tools", []any{})
	}
	sessionID := initRes.Header.Get("Mcp-Session-Id")
	_, _ = io.Copy(io.Discard, initRes.Body)
	initRes.Body.Close()

	listHeaders := map[string]string{}
	for k, v := range headers {
		listHeaders[k] = v
	}
	if sessionID != "" {
		listHeaders["mcp-session-id"] = sessionID
	}

	// Step 2: notifications/initialized (result ignored, per spec order).
	if resp, err := post(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`, listHeaders); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Step 3: tools/list.
	listRes, err := post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, listHeaders)
	if err != nil {
		return fail(err)
	}
	defer listRes.Body.Close()
	if listRes.StatusCode == 401 || listRes.StatusCode == 403 {
		return clisetup.NewJObj("requiresAuth", true, "tools", []any{})
	}
	var parsed map[string]any
	ct := listRes.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		text, _ := io.ReadAll(listRes.Body)
		for _, line := range strings.Split(string(text), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var obj map[string]any
			if err := stdjson.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &obj); err != nil {
				continue
			}
			if id, ok := obj["id"].(float64); ok && id == 2 && obj["result"] != nil {
				parsed = obj
				break
			}
		}
	} else {
		_ = stdjson.NewDecoder(listRes.Body).Decode(&parsed)
	}
	tools := []any{}
	if parsed != nil {
		if result, ok := parsed["result"].(map[string]any); ok {
			if list, ok := result["tools"].([]any); ok {
				for _, t := range list {
					tm, _ := t.(map[string]any)
					name, _ := tm["name"].(string)
					desc, _ := tm["description"].(string)
					tools = append(tools, clisetup.NewJObj("name", name, "description", desc))
				}
			}
		}
	}
	return clisetup.NewJObj("tools", tools)
}
