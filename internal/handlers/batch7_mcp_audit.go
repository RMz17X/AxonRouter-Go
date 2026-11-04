package handlers

import (
	"net/http"
	"strconv"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// MCP audit endpoints (batch 7, AxonRouter parity): GET /api/mcp/audit and
// GET /api/mcp/audit/stats read the mcp_tool_audit store back. Both sit
// behind requireManagementAuth upstream, applied at registration time via
// RequireManagementAuth.

// HandleMCPAuditList serves GET /api/mcp/audit?limit&offset&tool&success&apiKeyId.
func HandleMCPAuditList(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		filter := db.MCPToolAuditFilter{
			Limit:    auditQueryInt(query.Get("limit"), 50),
			Offset:   auditQueryInt(query.Get("offset"), 0),
			Tool:     query.Get("tool"),
			APIKeyID: query.Get("apiKeyId"),
		}
		switch query.Get("success") {
		case "true", "1":
			v := true
			filter.Success = &v
		case "false", "0":
			v := false
			filter.Success = &v
		}

		entries, total, err := repo.QueryMCPToolAudit(filter)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"entries": entries,
			"total":   total,
			"limit":   filter.Limit,
			"offset":  filter.Offset,
		})
	}
}

// HandleMCPAuditStats serves GET /api/mcp/audit/stats (last 24 hours).
func HandleMCPAuditStats(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, err := repo.MCPToolAuditStats()
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, stats)
	}
}

// auditQueryInt parses a pagination parameter the way the upstream route
// does with Number(...): a missing parameter yields the default. A
// non-numeric one also falls back here (upstream would hand NaN to its
// database and fail; the fallback keeps the endpoint usable and is recorded in
// .changes/918).
func auditQueryInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
