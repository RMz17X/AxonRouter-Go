package handlers

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// MCP HTTP transports (batch 7, AxonRouter parity): GET + POST
// /api/mcp/sse and POST /api/mcp/stream, backed by the upstream tool
// registry (open-sse/mcp-server/schemas/tools.ts + scopes.ts).
//
// A note on reachability, faithful to upstream: both transports invoke
// tools with a hardcoded null API-key record, so resolveApiKeyScopes
// yields no scopes and every known tool is denied at the scope gate
// (and audit-logged) before any handler can run. The tool handlers
// themselves are reachable only through /api/mcp/invoke and the stdio
// server, which are separate surfaces. This port reproduces that exact
// behaviour — registry, scope gate, audit row — rather than wiring the
// handlers into a path upstream never reaches.

// mcpToolDef is one registry entry: the public tool description plus the
// scopes a call requires.
type mcpToolDef struct {
	Name           string
	Description    string
	InputSchema    map[string]any
	RequiredScopes []string
}

// mcpToolRegistry mirrors MCP_TOOLS in order.
var mcpToolRegistry = []mcpToolDef{
	mcpTool("axonrouter_get_health", "Returns router health, runtime, and activity status.", 1, "", []string{"read:health"}),
	mcpTool("axonrouter_list_combos", "Lists configured model combos.", 1, `{"type":"object","properties":{"includeMetrics":{"type":"boolean"}}}`, []string{"read:combos"}),
	mcpTool("axonrouter_get_combo_metrics", "Returns metrics for a combo.", 1, `{"type":"object","properties":{"comboId":{"type":"string"}},"required":["comboId"]}`, []string{"read:combos"}),
	mcpTool("axonrouter_switch_combo", "Updates a combo configuration.", 1, `{"type":"object","properties":{"comboId":{"type":"string"},"patch":{"type":"object"}},"required":["comboId"]}`, []string{"write:combos"}),
	mcpTool("axonrouter_check_quota", "Returns quota state across providers.", 1, `{"type":"object","properties":{"provider":{"type":"string"}}}`, []string{"read:quota"}),
	mcpTool("axonrouter_route_request", "Routes a multimodal request through AxonRouter policy.", 1, `{"type":"object","properties":{"mode":{"type":"string"},"model":{"type":"string"}},"required":["mode","model"]}`, []string{"execute:completions"}),
	mcpTool("axonrouter_cost_report", "Returns usage and cost aggregates.", 1, `{"type":"object","properties":{"period":{"type":"string"}}}`, []string{"read:usage"}),
	mcpTool("axonrouter_list_models_catalog", "Lists models known to the router.", 1, "", []string{"read:models"}),
	mcpTool("axonrouter_web_search", "Searches the web via configured search provider.", 1, `{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"number"}},"required":["query"]}`, []string{"execute:search"}),
	mcpTool("axonrouter_simulate_route", "Simulates how a request would route.", 2, `{"type":"object","properties":{"mode":{"type":"string"},"model":{"type":"string"}}}`, []string{"read:health", "read:combos"}),
	mcpTool("axonrouter_set_budget_guard", "Updates budget guard settings.", 2, `{"type":"object","properties":{"enabled":{"type":"boolean"},"monthlyBudgetCapUsd":{"type":"number"}}}`, []string{"write:budget"}),
	mcpTool("axonrouter_set_routing_strategy", "Updates routing strategy.", 2, `{"type":"object","properties":{"strategy":{"type":"string"}}}`, []string{"write:resilience"}),
	mcpTool("axonrouter_set_resilience_profile", "Updates resilience profile.", 2, `{"type":"object","properties":{"profile":{"type":"string"}}}`, []string{"write:resilience"}),
	mcpTool("axonrouter_test_combo", "Executes a combo test.", 2, `{"type":"object","properties":{"comboId":{"type":"string"},"prompt":{"type":"string"}},"required":["comboId"]}`, []string{"execute:completions", "read:combos"}),
	mcpTool("axonrouter_get_provider_metrics", "Returns aggregated provider metrics.", 2, `{"type":"object","properties":{"provider":{"type":"string"}}}`, []string{"read:health"}),
	mcpTool("axonrouter_best_combo_for_task", "Suggests the best combo for a task.", 2, `{"type":"object","properties":{"taskType":{"type":"string"}}}`, []string{"read:combos", "read:health"}),
	mcpTool("axonrouter_explain_route", "Explains a routing decision.", 2, `{"type":"object","properties":{"requestDetailId":{"type":"string"},"correlationId":{"type":"string"}}}`, []string{"read:health", "read:usage"}),
	mcpTool("axonrouter_get_session_snapshot", "Returns a request/session snapshot.", 2, `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`, []string{"read:usage"}),
	mcpTool("axonrouter_db_health_check", "Runs DB health diagnostics.", 2, `{"type":"object","properties":{"autoRepair":{"type":"boolean"}}}`, []string{"read:health", "write:resilience"}),
	mcpTool("axonrouter_sync_pricing", "Synchronizes pricing data.", 2, "", []string{"pricing:write"}),
	mcpTool("axonrouter_cache_stats", "Returns cache domain statistics.", 2, "", []string{"read:cache"}),
	mcpTool("axonrouter_cache_flush", "Invalidates a cache layer.", 2, `{"type":"object","properties":{"layer":{"type":"string"}},"required":["layer"]}`, []string{"write:cache"}),
	mcpTool("axonrouter_oneproxy_fetch", "Fetches through configured proxy infrastructure.", 2, `{"type":"object","properties":{"url":{"type":"string"},"proxyPoolId":{"type":"string"}},"required":["url"]}`, []string{"read:proxies"}),
	mcpTool("axonrouter_oneproxy_rotate", "Rotates proxy pool selection.", 2, `{"type":"object","properties":{"proxyPoolId":{"type":"string"}},"required":["proxyPoolId"]}`, []string{"read:proxies"}),
	mcpTool("axonrouter_oneproxy_stats", "Returns proxy pool stats.", 2, "", []string{"read:proxies"}),
}

// mcpTool builds one registry entry from its literal input schema (the
// phase argument only decided the audit level upstream; the transports
// strip phase/auditLevel/requiredScopes from tools/list output anyway).
func mcpTool(name, description string, _ int, schemaJSON string, scopes []string) mcpToolDef {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	if schemaJSON != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(schemaJSON), &parsed); err == nil {
			schema = parsed
		}
	}
	return mcpToolDef{Name: name, Description: description, InputSchema: schema, RequiredScopes: scopes}
}

// mcpToolByName indexes the registry for invoke lookups.
var mcpToolByName = func() map[string]mcpToolDef {
	out := make(map[string]mcpToolDef, len(mcpToolRegistry))
	for _, tool := range mcpToolRegistry {
		out[tool.Name] = tool
	}
	return out
}()

// mcpHTTPTransportState mirrors runtimeHeartbeat's in-process HTTP state:
// transports mark themselves active so the MCP status surface can report
// liveness. Guarded per the repo's concurrency rules.
var mcpHTTPTransportState = struct {
	sync.Mutex
	Online            bool
	Transport         string
	ActiveConnections int
	LastActivityAt    string
}{}

// markHTTPTransportActive records transport activity (delta applies to the
// connection count, clamped at zero).
func markHTTPTransportActive(transport string, delta int) {
	mcpHTTPTransportState.Lock()
	defer mcpHTTPTransportState.Unlock()
	mcpHTTPTransportState.Online = true
	mcpHTTPTransportState.Transport = transport
	mcpHTTPTransportState.ActiveConnections += delta
	if mcpHTTPTransportState.ActiveConnections < 0 {
		mcpHTTPTransportState.ActiveConnections = 0
	}
	mcpHTTPTransportState.LastActivityAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// MCPHTTPTransportSnapshot returns the transport liveness state for the
// MCP status surface.
func MCPHTTPTransportSnapshot() map[string]any {
	mcpHTTPTransportState.Lock()
	defer mcpHTTPTransportState.Unlock()
	var lastActivity any
	if mcpHTTPTransportState.LastActivityAt != "" {
		lastActivity = mcpHTTPTransportState.LastActivityAt
	}
	var transport any
	if mcpHTTPTransportState.Transport != "" {
		transport = mcpHTTPTransportState.Transport
	}
	return map[string]any{
		"online":            mcpHTTPTransportState.Online,
		"transport":         transport,
		"activeConnections": mcpHTTPTransportState.ActiveConnections,
		"lastActivityAt":    lastActivity,
	}
}

// mcpInvokeResult is invokeTool's outcome shape.
type mcpInvokeResult struct {
	OK     bool
	Error  string
	Code   string
	Result any
}

// invokeMCPTool ports server.ts invokeTool + runTool's scope gate. The
// transports pass no API-key record (upstream hardcodes null), so no
// scopes are ever granted here: unknown tools are unsupported, known
// tools are scope-denied and audit-logged.
func invokeMCPTool(repo *db.Repo, name string, input map[string]any, transport string) mcpInvokeResult {
	tool, known := mcpToolByName[name]
	if !known {
		return mcpInvokeResult{OK: false, Error: "Unsupported tool", Code: "unsupported_tool"}
	}
	start := time.Now()
	if len(tool.RequiredScopes) > 0 {
		message := "Missing MCP scopes: " + strings.Join(tool.RequiredScopes, ", ")
		duration := time.Since(start).Milliseconds()
		_ = repo.LogMCPToolCall(name, input, nil, duration, false, "scope_denied", "", transport)
		return mcpInvokeResult{OK: false, Error: message, Code: "scope_denied"}
	}
	// No registered tool is scopeless, so this is unreachable today; it
	// keeps the gate honest if a scopeless tool is ever added.
	return mcpInvokeResult{OK: false, Error: "Tool call failed", Code: "tool_error"}
}

// mcpTransportGate applies the transports' API-key check: open when no API
// keys are configured, otherwise a valid Bearer API key is required.
// Returns true when the request may proceed; otherwise the denial has
// already been written.
func mcpTransportGate(repo *db.Repo, w http.ResponseWriter, r *http.Request) bool {
	keys, err := repo.GetApiKeys()
	if err != nil {
		writeJSONRPCError(w, http.StatusInternalServerError, nil, true, -32603, "Internal error")
		return false
	}
	if len(keys) == 0 {
		return true
	}
	key := ""
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		key = strings.TrimPrefix(authHeader, "Bearer ")
	}
	if key == "" {
		writeJSONRPCError(w, http.StatusUnauthorized, nil, true, -32000, "Missing API key")
		return false
	}
	record, err := repo.GetApiKeyByKey(key)
	if err != nil || record == nil || record.IsActive != 1 {
		writeJSONRPCError(w, http.StatusUnauthorized, nil, true, -32000, "Invalid API key")
		return false
	}
	return true
}

// writeJSONRPCError writes a JSON-RPC 2.0 error body. The id key is
// omitted entirely when includeID is false (JSON.stringify drops
// undefined ids upstream).
func writeJSONRPCError(w http.ResponseWriter, status int, id any, includeID bool, code int, message string) {
	body := map[string]any{
		"jsonrpc": "2.0",
		"error":   map[string]any{"code": code, "message": message},
	}
	if includeID {
		body["id"] = id
	}
	handlerutil.WriteJSON(w, status, body)
}

// writeJSONRPCOK writes a JSON-RPC 2.0 success body.
func writeJSONRPCOK(w http.ResponseWriter, id any, includeID bool, result any) {
	body := map[string]any{
		"jsonrpc": "2.0",
		"result":  result,
	}
	if includeID {
		body["id"] = id
	}
	handlerutil.WriteJSON(w, http.StatusOK, body)
}

// mcpInitializeResult is the initialize handshake result both transports
// answer with.
func mcpInitializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"serverInfo":      map[string]any{"name": "axonrouter", "version": "0.5.4"},
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
	}
}

// mcpToolsListResult is the tools/list result: registry order, internal
// fields (phase, auditLevel, requiredScopes) stripped.
func mcpToolsListResult() map[string]any {
	tools := make([]any, 0, len(mcpToolRegistry))
	for _, tool := range mcpToolRegistry {
		tools = append(tools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.InputSchema,
		})
	}
	return map[string]any{"tools": tools}
}

// mcpToolCallResult wraps an invoke outcome as a tools/call result.
func mcpToolCallResult(result mcpInvokeResult) map[string]any {
	text := ""
	if !result.OK {
		text = result.Error
		if text == "" {
			text = "Tool call failed"
		}
	} else if s, ok := result.Result.(string); ok {
		text = s
	} else {
		encoded, err := json.Marshal(result.Result)
		if err == nil {
			text = string(encoded)
		}
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"isError": !result.OK,
	}
}

// HandleMCPSSEStream serves GET /api/mcp/sse: open the event stream, send
// the endpoint event, then keepalive pings every 30s for up to 10 minutes.
func HandleMCPSSEStream(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !mcpTransportGate(repo, w, r) {
			return
		}
		markHTTPTransportActive("sse", 1)

		sessionID := newUUIDv4()
		endpoint := protocolsOrigin(r) + "/api/mcp/sse?sessionId=" + sessionID

		flusher, ok := w.(http.Flusher)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
			return
		}
		header := w.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache, no-store")
		header.Set("Connection", "keep-alive")
		header.Set("X-MCP-Session-Id", sessionID)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint)
		flusher.Flush()

		pings := time.NewTicker(30 * time.Second)
		defer pings.Stop()
		deadline := time.NewTimer(600 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline.C:
				return
			case <-pings.C:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// HandleMCPSSEMessage serves POST /api/mcp/sse (JSON-RPC over HTTP).
func HandleMCPSSEMessage(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !mcpTransportGate(repo, w, r) {
			return
		}
		markHTTPTransportActive("sse", 0)

		body, parsed := readJSONRPCBody(r)
		if !parsed || body["method"] == nil {
			var id any
			if parsed {
				id = body["id"]
			}
			writeJSONRPCError(w, http.StatusBadRequest, id, true, -32600, "Invalid request")
			return
		}
		dispatchMCPMessage(repo, w, r, body, "sse", false)
	}
}

// HandleMCPStream serves POST /api/mcp/stream (streamable HTTP). Unlike
// the SSE transport, an unparseable body degrades to an empty message and
// falls through to the method-not-found path.
func HandleMCPStream(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !mcpTransportGate(repo, w, r) {
			return
		}
		markHTTPTransportActive("streamable-http", 0)

		body, _ := readJSONRPCBody(r)
		if body == nil {
			body = map[string]any{}
		}
		dispatchMCPMessage(repo, w, r, body, "streamable-http", true)
	}
}

// readJSONRPCBody parses a JSON-RPC request body. parsed is false when the
// body is not a JSON object.
func readJSONRPCBody(r *http.Request) (map[string]any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, false
	}
	return body, true
}

// dispatchMCPMessage answers one JSON-RPC message for either transport.
// streamStyle selects the streamable-HTTP variations: notifications/
// initialized is also a 204, and an absent id stays absent from errors.
func dispatchMCPMessage(repo *db.Repo, w http.ResponseWriter, r *http.Request, body map[string]any, transport string, streamStyle bool) {
	id, idPresent := body["id"]
	methodRaw, methodPresent := body["method"]
	method, _ := methodRaw.(string)
	// The upstream error message interpolates the raw method value, so an
	// absent method reads "undefined" there.
	methodDisplay := method
	switch {
	case !methodPresent:
		methodDisplay = "undefined"
	case methodRaw != nil && method != "":
		// method is a non-empty string: display is the value itself.
	default:
		if methodRaw == nil {
			methodDisplay = "null"
		} else if _, isString := methodRaw.(string); !isString {
			methodDisplay = fmt.Sprintf("%v", methodRaw)
		}
	}

	// Response ids echo the request's id key presence exactly: upstream
	// destructures body.id and JSON.stringify drops it when undefined.
	switch method {
	case "initialize":
		writeJSONRPCOK(w, id, idPresent, mcpInitializeResult())
	case "initialized":
		w.WriteHeader(http.StatusNoContent)
	case "notifications/initialized":
		if streamStyle {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSONRPCError(w, http.StatusNotFound, id, idPresent, -32601, "Method not found: "+methodDisplay)
	case "tools/list":
		writeJSONRPCOK(w, id, idPresent, mcpToolsListResult())
	case "tools/call":
		params, _ := body["params"].(map[string]any)
		name, _ := params["name"].(string)
		arguments, _ := params["arguments"].(map[string]any)
		if arguments == nil {
			arguments = map[string]any{}
		}
		result := invokeMCPTool(repo, name, arguments, transport)
		writeJSONRPCOK(w, id, idPresent, mcpToolCallResult(result))
	case "ping":
		writeJSONRPCOK(w, id, idPresent, map[string]any{})
	default:
		writeJSONRPCError(w, http.StatusNotFound, id, idPresent, -32601, "Method not found: "+methodDisplay)
	}
}

// newUUIDv4 returns a random RFC 4122 version-4 UUID.
func newUUIDv4() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
