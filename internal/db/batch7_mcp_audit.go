package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	json "encoding/json/v2"
)

// MCP tool-call audit store (batch 7, AxonRouter parity).
//
// Ports open-sse/mcp-server/audit.ts + auditRuntime.ts: every MCP tool call
// the scope gate rejects (and, once the invoke surface lands, every executed
// call) leaves one row in mcp_tool_audit. created_at is an ISO-8601 string
// written explicitly by the logger, exactly as upstream does, so the 24h
// stats window can be a plain string comparison in both dialects.

// MCPToolAuditEntry is one audit row in the wire shape GET /api/mcp/audit
// returns: the raw column set, snake_case keys, success as 0/1.
type MCPToolAuditEntry struct {
	ID            int64   `json:"id"`
	ToolName      string  `json:"tool_name"`
	InputHash     string  `json:"input_hash"`
	OutputSummary *string `json:"output_summary"`
	DurationMs    int64   `json:"duration_ms"`
	APIKeyID      *string `json:"api_key_id"`
	Transport     *string `json:"transport"`
	Success       int     `json:"success"`
	ErrorCode     *string `json:"error_code"`
	CreatedAt     string  `json:"created_at"`
}

// MCPToolAuditFilter carries the GET /api/mcp/audit query parameters.
type MCPToolAuditFilter struct {
	Limit    int
	Offset   int
	Tool     string
	Success  *bool
	APIKeyID string
}

// MCPToolAuditStats is the GET /api/mcp/audit/stats result.
type MCPToolAuditStats struct {
	TotalCalls    int64            `json:"totalCalls"`
	AvgDurationMs float64          `json:"avgDurationMs"`
	SuccessRate   float64          `json:"successRate"`
	TopTools      []MCPToolTopTool `json:"topTools"`
}

// MCPToolTopTool is one row of the stats top-tools list.
type MCPToolTopTool struct {
	Tool  string `json:"tool"`
	Count int64  `json:"count"`
}

// LogMCPToolCall appends one audit row. input is hashed (SHA-256 of its JSON
// encoding) and output summarized to 200 chars, mirroring audit.ts.
func (r *Repo) LogMCPToolCall(toolName string, input any, output any, durationMs int64, success bool, errorCode, apiKeyID, transport string) error {
	if input == nil {
		input = map[string]any{}
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		inputJSON = []byte("{}")
	}
	hash := sha256.Sum256(inputJSON)

	summary := mcpOutputSummary(output)
	successInt := 0
	if success {
		successInt = 1
	}
	var errCodeVal, apiKeyVal, transportVal any
	if errorCode != "" {
		errCodeVal = errorCode
	}
	if apiKeyID != "" {
		apiKeyVal = apiKeyID
	}
	if transport != "" {
		transportVal = transport
	}
	createdAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	_, err = r.Exec(
		`INSERT INTO mcp_tool_audit (tool_name, input_hash, output_summary, duration_ms, api_key_id, transport, success, error_code, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		toolName, hex.EncodeToString(hash[:]), summary, durationMs, apiKeyVal, transportVal, successInt, errCodeVal, createdAt,
	)
	if err != nil {
		return fmt.Errorf("log mcp tool call: %w", err)
	}
	return nil
}

// mcpOutputSummary renders the audited output the way audit.ts does: strings
// verbatim, anything else as JSON, truncated to 197 chars + "..." past 200.
func mcpOutputSummary(output any) string {
	var raw string
	if output == nil {
		// Upstream summarizes `output || {}`: a missing output is "{}".
		raw = "{}"
	} else if s, ok := output.(string); ok {
		raw = s
	} else {
		encoded, err := json.Marshal(output)
		if err != nil {
			raw = "{}"
		} else {
			raw = string(encoded)
		}
	}
	if len(raw) > 200 {
		return raw[:197] + "..."
	}
	return raw
}

// QueryMCPToolAudit returns one page of audit rows plus the filtered total,
// newest first, mirroring queryAuditEntries.
func (r *Repo) QueryMCPToolAudit(f MCPToolAuditFilter) ([]MCPToolAuditEntry, int64, error) {
	var clauses []string
	var args []any
	if f.Tool != "" {
		clauses = append(clauses, "tool_name = ?")
		args = append(args, f.Tool)
	}
	if f.Success != nil {
		clauses = append(clauses, "success = ?")
		if *f.Success {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if f.APIKeyID != "" {
		clauses = append(clauses, "api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	var total int64
	if err := r.QueryRow(`SELECT COUNT(*) FROM mcp_tool_audit `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count mcp tool audit: %w", err)
	}

	rows, err := r.Query(
		`SELECT id, tool_name, input_hash, output_summary, duration_ms, api_key_id, transport, success, error_code, created_at
		 FROM mcp_tool_audit `+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("query mcp tool audit: %w", err)
	}
	defer rows.Close()

	entries := []MCPToolAuditEntry{}
	for rows.Next() {
		var e MCPToolAuditEntry
		var summary, apiKeyID, transport, errorCode sql.NullString
		if err := rows.Scan(&e.ID, &e.ToolName, &e.InputHash, &summary, &e.DurationMs, &apiKeyID, &transport, &e.Success, &errorCode, &e.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan mcp tool audit: %w", err)
		}
		if summary.Valid {
			e.OutputSummary = &summary.String
		}
		if apiKeyID.Valid {
			e.APIKeyID = &apiKeyID.String
		}
		if transport.Valid {
			e.Transport = &transport.String
		}
		if errorCode.Valid {
			e.ErrorCode = &errorCode.String
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read mcp tool audit: %w", err)
	}
	return entries, total, nil
}

// MCPToolAuditStats aggregates the last 24 hours of audit rows. Upstream
// compares the ISO created_at strings against the database's datetime('now',
// '-1 day'); the cutoff is computed here in the same "YYYY-MM-DD HH:MM:SS"
// shape so the string comparison behaves identically.
func (r *Repo) MCPToolAuditStats() (*MCPToolAuditStats, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")

	stats := &MCPToolAuditStats{TopTools: []MCPToolTopTool{}}
	var avgDuration, successRate sql.NullFloat64
	err := r.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(AVG(duration_ms), 0),
		        COALESCE(AVG(CASE WHEN success = 1 THEN 100.0 ELSE 0 END), 0)
		 FROM mcp_tool_audit WHERE created_at >= ?`, cutoff,
	).Scan(&stats.TotalCalls, &avgDuration, &successRate)
	if err != nil {
		return nil, fmt.Errorf("mcp tool audit stats: %w", err)
	}
	stats.AvgDurationMs = avgDuration.Float64
	stats.SuccessRate = successRate.Float64

	rows, err := r.Query(
		`SELECT tool_name, COUNT(*) FROM mcp_tool_audit WHERE created_at >= ?
		 GROUP BY tool_name ORDER BY COUNT(*) DESC LIMIT 5`, cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("mcp tool audit top tools: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t MCPToolTopTool
		if err := rows.Scan(&t.Tool, &t.Count); err != nil {
			return nil, fmt.Errorf("scan mcp tool audit top tool: %w", err)
		}
		stats.TopTools = append(stats.TopTools, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read mcp tool audit top tools: %w", err)
	}
	return stats, nil
}
