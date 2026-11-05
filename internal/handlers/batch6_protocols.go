package handlers

// Batch 6 — the /api/protocols family, ported from AxonRouter
// src/app/api/protocols/**. Dashboard-side status/proxy surface: the
// a2a routes are self-contained; the mcp routes proxy this server's
// own /api/mcp/* family (a batch-8 surface — until it exists, these
// routes reproduce exactly what Axon answers when its MCP backend is
// down: handshake "offline", tools 500, invoke {ok:false,...}, status
// "degraded"). Axon self-calls with fetch and no explicit timeout; the
// port bounds its client at 15 s so a wedged backend cannot hang the
// dashboard (noted in .changes/917). All responses carry
// Cache-Control: no-store, per Axon.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"axonrouter/proxy/internal/handlerutil"
)

var protocolsClient = &http.Client{Timeout: 15 * time.Second}

var mcpCapabilities = []string{"tool-schema", "tool-invocation-mapping", "audit", "runtime-status"}
var a2aCapabilities = []string{"agent-message-envelope", "streaming-handshake"}

func protocolsOrigin(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func protocolsSelfGet(r *http.Request, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, protocolsOrigin(r)+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := protocolsClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, body, err
}

func protocolsSelfPost(r *http.Request, path string, payload any, forwardHeaders http.Header) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, protocolsOrigin(r)+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if forwardHeaders != nil {
		if v := forwardHeaders.Get("Authorization"); v != "" {
			req.Header.Set("Authorization", v)
		}
		if v := forwardHeaders.Get("X-Api-Key"); v != "" {
			req.Header.Set("X-Api-Key", v)
		}
	}
	resp, err := protocolsClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, body, err
}

func protocolsJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	handlerutil.WriteJSON(w, status, payload)
}

func statusOK(status int) bool { return status >= 200 && status < 300 }

// HandleA2AHandshake serves GET /api/protocols/a2a/handshake (static).
func HandleA2AHandshake(w http.ResponseWriter, r *http.Request) {
	protocolsJSON(w, http.StatusOK, map[string]any{
		"protocol":     "a2a",
		"status":       "ok",
		"version":      1,
		"capabilities": a2aCapabilities,
	})
}

// HandleA2AMessage serves POST /api/protocols/a2a/message (ack echo).
func HandleA2AMessage(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if raw, err := io.ReadAll(r.Body); err == nil {
		_ = json.Unmarshal(raw, &body)
	}
	if body == nil {
		body = map[string]any{}
	}
	protocolsJSON(w, http.StatusOK, map[string]any{
		"protocol": "a2a",
		"status":   "ack",
		"received": map[string]any{
			"agent":         body["agent"],
			"message":       body["message"],
			"correlationId": body["correlationId"],
		},
	})
}

// HandleMCPHandshake serves GET /api/protocols/mcp/handshake.
func HandleMCPHandshake(w http.ResponseWriter, r *http.Request) {
	status := "offline"
	if _, raw, err := protocolsSelfGet(r, "/api/mcp/status"); err == nil {
		var statusJSON map[string]any
		if json.Unmarshal(raw, &statusJSON) == nil {
			if s, ok := statusJSON["status"].(string); ok && s != "" {
				status = s
			}
		}
	}
	protocolsJSON(w, http.StatusOK, map[string]any{
		"protocol":     "mcp",
		"status":       status,
		"version":      1,
		"capabilities": mcpCapabilities,
	})
}

// HandleMCPTools serves GET /api/protocols/mcp/tools (JSON passthrough
// of the server's own /api/mcp/tools).
func HandleMCPTools(w http.ResponseWriter, r *http.Request) {
	_, raw, err := protocolsSelfGet(r, "/api/mcp/tools")
	if err == nil && json.Valid(raw) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	// Axon's unguarded res.json() throws on a non-JSON backend answer
	// and Next serves its HTML 500; the port keeps the status and
	// answers a JSON error body instead (recorded in .changes/917).
	protocolsJSON(w, http.StatusInternalServerError, map[string]any{
		"error": "Internal Server Error",
	})
}

// HandleMCPInvoke serves POST /api/protocols/mcp/invoke.
func HandleMCPInvoke(w http.ResponseWriter, r *http.Request) {
	var payload any
	if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	status, raw, err := protocolsSelfPost(r, "/api/mcp/invoke", payload, r.Header)
	if err != nil {
		protocolsJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "Failed to invoke MCP tool",
			"message": err.Error(),
		})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if json.Valid(raw) {
		_, _ = w.Write(raw)
		return
	}
	out, _ := json.Marshal(map[string]any{"ok": false, "error": "Invalid response"})
	_, _ = w.Write(out)
}

// HandleProtocolsStatus serves GET /api/protocols/status: it probes
// the mcp backend and the a2a routes on this same server and
// aggregates per Axon's logic.
func HandleProtocolsStatus(w http.ResponseWriter, r *http.Request) {
	mcpStatusCode, mcpStatusRaw, _ := protocolsSelfGet(r, "/api/mcp/status")
	mcpToolsCode, mcpToolsRaw, _ := protocolsSelfGet(r, "/api/mcp/tools")
	mcpInvokeCode, _, _ := protocolsSelfPost(r, "/api/mcp/invoke",
		map[string]any{"tool": "axonrouter_get_health", "input": map[string]any{}}, nil)
	a2aHandshakeCode, _, _ := protocolsSelfGet(r, "/api/protocols/a2a/handshake")
	a2aMessageCode, _, _ := protocolsSelfPost(r, "/api/protocols/a2a/message",
		map[string]any{"agent": "healthcheck", "message": "ping"}, nil)

	var mcpStatus map[string]any
	_ = json.Unmarshal(mcpStatusRaw, &mcpStatus)
	var mcpTools map[string]any
	_ = json.Unmarshal(mcpToolsRaw, &mcpTools)

	mcpOnline := statusOK(mcpStatusCode)
	mcpState := "degraded"
	if mcpOnline {
		mcpState = "offline"
		if online, ok := mcpStatus["online"].(bool); ok && online {
			mcpState = "online"
		}
	}
	transport := []string{"stdio", "sse", "streamable-http"}
	if t, ok := mcpStatus["transport"].(string); ok && t != "" {
		transport[0] = t
	}
	toolCount := 0
	if tc, ok := mcpStatus["toolCount"].(float64); ok && tc != 0 {
		toolCount = int(tc)
	} else if tools, ok := mcpTools["tools"].([]any); ok {
		toolCount = len(tools)
	}
	_ = mcpToolsCode

	a2aState := "planned"
	if statusOK(a2aHandshakeCode) && statusOK(a2aMessageCode) {
		a2aState = "staging"
	}
	handshakeState := func(ok bool) string {
		if ok {
			return "ok"
		}
		return "degraded"
	}

	protocolsJSON(w, http.StatusOK, map[string]any{
		"protocols": []any{
			map[string]any{
				"id":           "mcp",
				"label":        "MCP",
				"status":       mcpState,
				"transport":    transport,
				"capabilities": mcpCapabilities,
				"handshake":    handshakeState(mcpOnline),
				"runtimeFlow":  handshakeState(statusOK(mcpInvokeCode)),
				"toolCount":    toolCount,
			},
			map[string]any{
				"id":           "a2a",
				"label":        "A2A",
				"status":       a2aState,
				"transport":    []string{"http", "sse"},
				"capabilities": a2aCapabilities,
				"handshake":    handshakeState(statusOK(a2aHandshakeCode)),
				"runtimeFlow":  handshakeState(statusOK(a2aMessageCode)),
			},
		},
	})
}
