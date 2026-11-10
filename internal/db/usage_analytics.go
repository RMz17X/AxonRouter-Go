package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Readers backing the AxonRouter-parity usage endpoints (GET
// /api/usage/analytics, /api/usage/history, /api/usage/analytics/failures,
// /api/usage/logs). AxonRouter's history reads aggregate a usage-events
// table with token columns this build does not keep; the same values live
// here in usageHistory columns plus the per-row tokens payload, so the
// totals expression below is the one translation the history readers share.
// The live GET /api/usage/analytics contract instead aggregates per-request
// events, which here live in the requestDetails ledger (see
// GetAnalyticsEvents).

// usageTotalTokensExpr renders AxonRouter's total-tokens rule (its
// COALESCE(total_tokens, tokens_input + tokens_output + tokens_cache_read +
// tokens_cache_creation + tokens_reasoning)) from the jsonb payload with
// the jsonexpr.go forms: a stored total wins when the row's tokens
// payload carries one, otherwise the parts are summed. Presence of
// total_tokens is tested on the text extraction because a stored JSON
// null must read as absent, and the guards collapse to the presence
// check (tokens is jsonb, valid by construction). The cache read count
// resolves the way GetUsageCacheHitTotalsSince resolves it —
// cached_tokens first, cache_read_input_tokens when the first is
// zero/absent.
func usageTotalTokensExpr() string {
	return `CASE
		WHEN ` + jsonValid("tokens") + ` AND ` + jsonTextAt("tokens", "total_tokens") + ` IS NOT NULL
			THEN ` + jsonIntAt("tokens", "total_tokens") + `
		ELSE COALESCE(promptTokens, 0) + COALESCE(completionTokens, 0)
			+ CASE WHEN ` + jsonValid("tokens") + ` THEN COALESCE(NULLIF(` + jsonIntAt("tokens", "cached_tokens") + `, 0), ` + jsonIntAt("tokens", "cache_read_input_tokens") + `, 0) ELSE 0 END
			+ CASE WHEN ` + jsonValid("tokens") + ` THEN COALESCE(` + jsonIntAt("tokens", "cache_creation_input_tokens") + `, 0) ELSE 0 END
			+ CASE WHEN ` + jsonValid("tokens") + ` THEN COALESCE(` + jsonIntAt("tokens", "reasoning_tokens") + `, 0) ELSE 0 END
	END`
}

// UsageWindowFilter is the row filter the analytics and history reads share:
// a timestamp window plus optional equality restrictions. A nil restriction
// slice means "unrestricted"; a non-nil empty slice matches nothing (a filter
// the caller resolved to zero candidates must not fall back to "all").
type UsageWindowFilter struct {
	Since         string
	Until         string
	Provider      string
	Model         string
	ConnectionIDs []string
	APIKeyValues  []string
}

func (f UsageWindowFilter) whereClause() (string, []any) {
	var clauses []string
	var args []any
	if f.Since != "" {
		clauses = append(clauses, "timestamp >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		clauses = append(clauses, "timestamp <= ?")
		args = append(args, f.Until)
	}
	if f.Provider != "" {
		clauses = append(clauses, "COALESCE(provider, '') = ?")
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		clauses = append(clauses, "COALESCE(model, '') = ?")
		args = append(args, f.Model)
	}
	if f.ConnectionIDs != nil {
		clause, inArgs := inClause("COALESCE(connectionId, '')", f.ConnectionIDs)
		clauses = append(clauses, clause)
		args = append(args, inArgs...)
	}
	if f.APIKeyValues != nil {
		clause, inArgs := inClause("COALESCE(apiKey, '')", f.APIKeyValues)
		clauses = append(clauses, clause)
		args = append(args, inArgs...)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func inClause(column string, values []string) (string, []any) {
	if len(values) == 0 {
		return "1 = 0", nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return column + " IN (" + placeholders + ")", args
}

// AnalyticsEventRow is one request of the live GET /api/usage/analytics
// contract. AxonRouter aggregates a dedicated analytics_events stream
// (timestamp, provider, model, success, latency_ms, input/output tokens,
// error_category); the same per-request record here is the requestDetails
// ledger: its status column is exactly 'success' or 'error', and its stored
// payload carries latency.total (ms), the token counts, and — for failures —
// the error text and status the failure sample expressions already resolve
// for the failure analytics.
type AnalyticsEventRow struct {
	Timestamp     string
	Provider      string
	Model         string
	Success       bool
	LatencyMs     sql.NullFloat64
	InputTokens   sql.NullInt64
	OutputTokens  sql.NullInt64
	ErrorText     string // sampled error message (failures)
	ErrorStatus   string // sampled status/code text (failures)
	ErrorCategory string // stored category when the payload carries one, else ""
}

// UsageHistoryGroupRow is one (provider, model, connection, api key,
// endpoint) aggregate of usageHistory, the raw material GET
// /api/usage/history folds into AxonRouter's five breakdown maps.
type UsageHistoryGroupRow struct {
	Provider         string
	Model            string
	ConnectionID     string
	APIKey           string
	Endpoint         string
	Requests         int
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Cost             float64
	LastUsed         string
}

// GetUsageHistoryGrouped aggregates the filtered window by every breakdown
// dimension at once, so the handler's five maps all fold from one scan —
// the same shape as AxonRouter's buildGroupedStats.
func (r *Repo) GetUsageHistoryGrouped(ctx context.Context, f UsageWindowFilter) ([]UsageHistoryGroupRow, error) {
	where, args := f.whereClause()
	rows, err := r.QueryContext(ctx, `
		SELECT COALESCE(provider, ''), COALESCE(model, ''), COALESCE(connectionId, ''),
			COALESCE(apiKey, ''), COALESCE(endpoint, ''),
			COUNT(*),
			COALESCE(SUM(COALESCE(promptTokens, 0)), 0),
			COALESCE(SUM(COALESCE(completionTokens, 0)), 0),
			COALESCE(SUM(`+usageTotalTokensExpr()+`), 0),
			COALESCE(SUM(COALESCE(cost, 0)), 0),
			COALESCE(MAX(timestamp), '')
		FROM usageHistory `+where+`
		GROUP BY COALESCE(provider, ''), COALESCE(model, ''), COALESCE(connectionId, ''),
			COALESCE(apiKey, ''), COALESCE(endpoint, '')`, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage history grouped: %w", err)
	}
	defer func() { _ = rows.Close() }()
	res := make([]UsageHistoryGroupRow, 0)
	for rows.Next() {
		var row UsageHistoryGroupRow
		if err := rows.Scan(&row.Provider, &row.Model, &row.ConnectionID, &row.APIKey,
			&row.Endpoint, &row.Requests, &row.PromptTokens, &row.CompletionTokens,
			&row.TotalTokens, &row.Cost, &row.LastUsed); err != nil {
			return nil, fmt.Errorf("scan usage history grouped: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage history grouped: %w", err)
	}
	return res, nil
}

// FailureFilter selects the failed request-details rows the failure
// analytics aggregate. From/To are timestamp bounds in the stored format.
type FailureFilter struct {
	Provider string
	Model    string
	From     string
	To       string
}

// FailureGroupRow is one failure-analytics leaderboard row: a provider (and
// model, for the model leaderboard) with its failure count and the newest
// failure's sample error and status code.
type FailureGroupRow struct {
	Provider         string
	Model            string
	FailureCount     int
	LatestFailedAt   string
	SampleError      string
	SampleStatusCode string
}

// UsageLogRow is one row of GET /api/usage/logs: AxonRouter reads a dedicated
// request-log table this build never had; usageHistory is the same record
// (one row per request) and is what this reader projects. Token counts stay
// nullable — AxonRouter emits null for a count the row never recorded, and
// rows the Next.js dashboard wrote into the shared database can carry NULLs.
type UsageLogRow struct {
	ID               int64
	Timestamp        string
	Provider         string
	Model            string
	ConnectionID     string
	Status           string
	PromptTokens     sql.NullInt64
	CompletionTokens sql.NullInt64
	Meta             string
}

// GetRecentUsageLogRows returns the newest usage history rows in log shape.
func (r *Repo) GetRecentUsageLogRows(ctx context.Context, limit int) ([]UsageLogRow, error) {
	// meta is a jsonb payload, so the '' default has to be produced from
	// the value's text form — a bare COALESCE(meta, '') asks the server
	// to parse '' as JSON.
	metaExpr := `COALESCE(meta::text, '')`
	rows, err := r.QueryContext(ctx, `
		SELECT id, timestamp, COALESCE(provider, ''), COALESCE(model, ''),
			COALESCE(connectionId, ''), COALESCE(status, ''),
			promptTokens, completionTokens, `+metaExpr+`
		FROM usageHistory
		ORDER BY timestamp DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent usage log rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	res := make([]UsageLogRow, 0)
	for rows.Next() {
		var row UsageLogRow
		if err := rows.Scan(&row.ID, &row.Timestamp, &row.Provider, &row.Model,
			&row.ConnectionID, &row.Status, &row.PromptTokens, &row.CompletionTokens,
			&row.Meta); err != nil {
			return nil, fmt.Errorf("scan recent usage log rows: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent usage log rows: %w", err)
	}
	return res, nil
}
