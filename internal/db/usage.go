package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// usageHistoryColumns is the projection every usageHistory reader shares.
const usageHistoryColumns = `SELECT timestamp, COALESCE(provider, ''), COALESCE(model, ''), COALESCE(connectionId, ''),
	       COALESCE(apiKey, ''), COALESCE(endpoint, ''), COALESCE(promptTokens, 0),
	       COALESCE(completionTokens, 0), COALESCE(cost, 0.0), COALESCE(status, 'ok'), COALESCE(tokens, '{}')
		FROM usageHistory`

// The three usageHistory reads are named constants because
// usage_query_plan_test.go runs EXPLAIN QUERY PLAN against them. The plan is the
// contract: ordering by rowid forces a full scan that grows with the table and
// runs on the dashboard's 5-second poll, while `timestamp` is covered by
// idx_uh_ts. Keeping the SQL here means that test pins what these functions
// execute rather than a copy that could drift (#138).
const (
	usageHistorySinceQuery = usageHistoryColumns + `
		WHERE timestamp >= ?
		ORDER BY timestamp DESC
	`
	usageHistoryBetweenQuery = usageHistoryColumns + `
		WHERE timestamp > ? AND timestamp <= ?
		ORDER BY timestamp ASC
	`
	usageHistoryRecentQuery = usageHistoryColumns + `
		ORDER BY timestamp DESC
		LIMIT ?
	`
)

// GetUsageDaily returns the daily usage JSON data for a given date key.
func (r *Repo) GetUsageDaily(dateKey string) (string, error) {
	var data string
	err := r.QueryRow(`SELECT data FROM usageDaily WHERE dateKey = ?`, dateKey).Scan(&data)
	if err != nil {
		return "", fmt.Errorf("get daily usage %s: %w", dateKey, err)
	}
	return data, nil
}

// InsertUsageHistory logs a single request's token usage to the usageHistory table.
//
// clientKey is the CLIENT key that sent the request, masked, and it is what a
// per-key token budget is measured from (#365). It is a trailing variadic so the
// call sites that have no client attribution (migrations, fixtures) keep
// compiling unchanged: only the live request path knows the caller.
func (r *Repo) InsertUsageHistory(provider, model, connectionID, apiKey, endpoint string, promptTokens, completionTokens int, cost float64, status string, totalTokens int, meta string, tokensJSON string, clientKey ...string) error {
	attributedTo := ""
	if len(clientKey) > 0 {
		attributedTo = clientKey[0]
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	_, err := r.Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp, provider, model, connectionID, apiKey, attributedTo, endpoint, promptTokens, completionTokens, cost, status, tokensJSON, meta,
	)
	if err != nil {
		return fmt.Errorf("insert usage history: %w", err)
	}
	return nil
}

// UsageHistoryRecord is one usageHistory row in insert form. It carries
// exactly the columns InsertUsageHistory writes; the batch writer below
// exists because the usage ledger's hot path produces rows in bursts the
// one-statement-per-row form cannot drain (review F2).
type UsageHistoryRecord struct {
	Timestamp        string // RFC3339; stamped by the caller at completion time
	Provider         string
	Model            string
	ConnectionID     string
	APIKey           string // already masked by the caller
	ClientKey        string // masked client key attribution (#365); "" when unknown
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	Status           string
	Tokens           string
	Meta             string
}

// usageInsertChunk bounds one multi-row INSERT's row count: 200 rows ×
// 13 columns = 2,600 bound parameters, far under PostgreSQL's 65,535
// limit, and one statement stays a single WAL record set.
const usageInsertChunk = 200

// InsertUsageHistoryBatch writes recs as chunked multi-row INSERTs — one
// statement (one round trip, one commit) per chunk instead of one per
// row. This is the drain-rate fix for the usage writer: a burst of
// completions becomes a handful of statements the worker lands faster
// than requests can produce them.
func (r *Repo) InsertUsageHistoryBatch(recs []UsageHistoryRecord) error {
	for start := 0; start < len(recs); start += usageInsertChunk {
		chunk := recs[start:min(start+usageInsertChunk, len(recs))]
		var sb strings.Builder
		sb.WriteString(`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, clientKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta) VALUES `)
		args := make([]any, 0, len(chunk)*13)
		for i, rec := range chunk {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
			args = append(args, rec.Timestamp, rec.Provider, rec.Model, rec.ConnectionID, rec.APIKey, rec.ClientKey, rec.Endpoint, rec.PromptTokens, rec.CompletionTokens, rec.Cost, rec.Status, rec.Tokens, rec.Meta)
		}
		if _, err := r.Exec(sb.String(), args...); err != nil {
			return fmt.Errorf("insert usage history batch: %w", err)
		}
	}
	return nil
}

// RequestDetailRecord is one requestDetails row in insert form.
type RequestDetailRecord struct {
	ID           string
	Timestamp    string // millisecond RFC3339, the single-row writer's format
	Provider     string
	Model        string
	ConnectionID string
	Status       string
	Data         string
}

// SeedRequestDetails exports the newest `limit` rows of the legacy
// requestDetails table in ascending timestamp order. It exists for
// exactly one purpose: the file-backed detail store's first boot
// imports these rows so the Request Details page survives the storage
// move. Nothing writes to the table any more, and once a deployment's
// store has files this read never runs again.
func (r *Repo) SeedRequestDetails(limit int) ([]RequestDetailRecord, error) {
	rows, err := r.Query(`SELECT id, timestamp, COALESCE(provider, ''), COALESCE(model, ''),
		COALESCE(connectionId, ''), COALESCE(status, ''), COALESCE(data::text, '')
		FROM (SELECT * FROM requestDetails ORDER BY timestamp DESC, id DESC LIMIT ?) recent
		ORDER BY timestamp ASC, id ASC`, limit)
	if err != nil {
		return nil, fmt.Errorf("seed request details: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var res []RequestDetailRecord
	for rows.Next() {
		var rec RequestDetailRecord
		if err := rows.Scan(&rec.ID, &rec.Timestamp, &rec.Provider, &rec.Model,
			&rec.ConnectionID, &rec.Status, &rec.Data); err != nil {
			return nil, fmt.Errorf("scan seed request detail: %w", err)
		}
		res = append(res, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate seed request details: %w", err)
	}
	return res, nil
}

// UpsertUsageDaily inserts or replaces a daily usage aggregation record.
// The data parameter should be a JSON string matching the axonrouter-go daily aggregation format.
// NOTE: this is a bare full-row replace of a pre-merged JSON blob. The
// chat layer's fold does NOT use it: FoldUsageDaily below serialises the
// merge across processes inside one transaction. Kept for direct
// writers that own the whole row.
func (r *Repo) UpsertUsageDaily(dateKey string, data string) error {
	// The full-row replace: usageDaily keys on dateKey, so the conflict
	// target is the key itself and DO UPDATE SET is the atomic replace
	// (the merge still happened in-process, per the note above).
	query := `INSERT INTO usageDaily (dateKey, data) VALUES (?, ?)
	 ON CONFLICT (dateKey) DO UPDATE SET data = EXCLUDED.data`
	_, err := r.Exec(query, dateKey, data)
	if err != nil {
		return fmt.Errorf("upsert daily usage %s: %w", dateKey, err)
	}
	return nil
}

// Stage sentinels for FoldUsageDaily. The fold's caller (the chat layer)
// classifies failures by stage to keep the round-1 failure-atomic
// contract's log lines: a read-stage failure aborts the fold with the
// deltas retained, a merge-stage failure likewise, and a write-stage
// failure likewise — each stage has its own line.
var (
	ErrDailyFoldRead  = errors.New("read daily usage")
	ErrDailyFoldMerge = errors.New("merge daily usage")
	ErrDailyFoldWrite = errors.New("write daily usage")
)

// FoldUsageDaily folds one date's pending deltas into the stored daily
// aggregate atomically ACROSS processes (council round 3, C-6/B-1).
// The whole read-modify-write runs in ONE transaction: the day row is
// inserted empty when absent (the old ErrNoRows case — an empty base,
// not an error), then read back FOR UPDATE, so a second process folding
// the same day blocks on the row lock and merges onto this fold's
// committed result instead of erasing it with a stale full-row write.
// fold receives the stored blob ("" only when the row held nothing) and
// returns the merged blob; it must be pure (no database access — the
// transaction holds one pooled connection while it runs). Any failure
// rolls the transaction back and leaves the stored aggregate untouched,
// so the caller can retain its deltas for the next flush.
func (r *Repo) FoldUsageDaily(dateKey string, fold func(existing string) (string, error)) error {
	ctx := context.Background()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDailyFoldRead, fmt.Errorf("fold daily usage %s: %w", dateKey, err))
	}
	defer func() { _ = tx.Rollback() }()

	// Ensure the day row exists so the locking read below always has a
	// row to lock.
	if _, err := tx.ExecContext(ctx, r.Rebind(
		`INSERT INTO usageDaily (dateKey, data) VALUES (?, ?) ON CONFLICT (dateKey) DO NOTHING`),
		dateKey, "{}"); err != nil {
		return fmt.Errorf("%w: %w", ErrDailyFoldRead, fmt.Errorf("fold daily usage %s: %w", dateKey, err))
	}
	var existing string
	if err := tx.QueryRowContext(ctx, r.Rebind(
		`SELECT data FROM usageDaily WHERE dateKey = ? FOR UPDATE`), dateKey).Scan(&existing); err != nil {
		return fmt.Errorf("%w: %w", ErrDailyFoldRead, fmt.Errorf("fold daily usage %s: %w", dateKey, err))
	}
	merged, err := fold(existing)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDailyFoldMerge, err)
	}
	if _, err := tx.ExecContext(ctx, r.Rebind(
		`UPDATE usageDaily SET data = ? WHERE dateKey = ?`), merged, dateKey); err != nil {
		return fmt.Errorf("%w: %w", ErrDailyFoldWrite, fmt.Errorf("fold daily usage %s: %w", dateKey, err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", ErrDailyFoldWrite, fmt.Errorf("fold daily usage %s: commit: %w", dateKey, err))
	}
	return nil
}

// UsageHistoryRow represents a record from the usageHistory table.
type UsageHistoryRow struct {
	Timestamp        string
	Provider         string
	Model            string
	ConnectionID     string
	APIKey           string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	Status           string
	Tokens           string
}

// GetUsageDailyRecent returns the most recent daily usage records up to limit.

func (r *Repo) GetUsageDailyRecent(limit int) ([]string, error) {
	rows, err := r.Query(`SELECT data FROM usageDaily ORDER BY dateKey DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent usageDaily: %w", err)
	}
	defer rows.Close()

	var res []string
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan usageDaily recent: %w", err)
		}
		res = append(res, data)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageDaily recent: %w", err)
	}
	return res, nil
}

// UsageDailyRow is one usageDaily row: the local date key plus its aggregate
// payload. The chart endpoint needs the key (the existing
// GetUsageDailyRecent drops it), so this keeps both.
type UsageDailyRow struct {
	DateKey string
	Data    string
}

// GetUsageDailyRange returns the most recent daily usage rows up to limit,
// newest first, each with its dateKey.
func (r *Repo) GetUsageDailyRange(limit int) ([]UsageDailyRow, error) {
	rows, err := r.Query(`SELECT dateKey, data FROM usageDaily ORDER BY dateKey DESC LIMIT ?`, limit) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("query usageDaily range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var res []UsageDailyRow
	for rows.Next() {
		var row UsageDailyRow
		if err := rows.Scan(&row.DateKey, &row.Data); err != nil {
			return nil, fmt.Errorf("scan usageDaily range: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageDaily range: %w", err)
	}
	return res, nil
}

// UsageGroupRow is one provider (or provider+model) aggregate over a time
// window, used by the usage dashboard's node-health table.
type UsageGroupRow struct {
	Provider         string
	Model            string
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	LastUsed         string
}

const usageGroupStats = `
	COUNT(*) AS requests,
	SUM(CASE WHEN status NOT IN ('ok', 'success') THEN 1 ELSE 0 END) AS errors,
	COALESCE(SUM(promptTokens), 0) AS promptTokens,
	COALESCE(SUM(completionTokens), 0) AS completionTokens,
	COALESCE(SUM(cost), 0) AS cost,
	MAX(timestamp) AS lastUsed
`

func scanUsageGroupRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, op string) ([]UsageGroupRow, error) {
	var res []UsageGroupRow
	for rows.Next() {
		var row UsageGroupRow
		if err := rows.Scan(
			&row.Provider, &row.Model, &row.Requests, &row.Errors,
			&row.PromptTokens, &row.CompletionTokens, &row.Cost, &row.LastUsed,
		); err != nil {
			return nil, fmt.Errorf("scan usage group %s: %w", op, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage group %s: %w", op, err)
	}
	return res, nil
}

// GetUsageGroupByProviderSince aggregates usageHistory per provider since the
// cutoff. The model column is a constant empty string: provider rows have no
// single model, and the shared scanner expects both key columns.
func (r *Repo) GetUsageGroupByProviderSince(since string) ([]UsageGroupRow, error) {
	rows, err := r.Query( //nolint:noctx,rowserrcheck
		`SELECT COALESCE(provider, ''), '', `+usageGroupStats+`
		   FROM usageHistory WHERE timestamp >= ? GROUP BY COALESCE(provider, '')`, since)
	if err != nil {
		return nil, fmt.Errorf("query usage group by provider: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanUsageGroupRows(rows, "provider")
}

// GetUsageGroupByModelSince aggregates usageHistory per provider+model since
// the cutoff.
func (r *Repo) GetUsageGroupByModelSince(since string) ([]UsageGroupRow, error) {
	rows, err := r.Query( //nolint:noctx,rowserrcheck
		`SELECT COALESCE(provider, ''), COALESCE(model, ''), `+usageGroupStats+`
		   FROM usageHistory WHERE timestamp >= ? GROUP BY COALESCE(provider, ''), COALESCE(model, '')`, since)
	if err != nil {
		return nil, fmt.Errorf("query usage group by model: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanUsageGroupRows(rows, "model")
}

// FoldUsageGroupByProvider collapses model-level group rows into the
// provider-level rows GetUsageGroupByProviderSince would have queried.
//
// It exists so the dashboard can answer both tables from one scan of the
// window. The two reads were separate GROUP BY passes over the same rows, and
// on a 305k-row ledger they measured 22ms and 37ms — the fold below costs 33µs
// and the sums are identical, because a provider's total is by definition the
// sum of its models' totals. A model row that carries the provider's own name
// (the "no model" bucket) is just another addend.
//
// The order of the result follows first appearance in rows, so callers that
// sort afterwards are unaffected; callers that do not must not depend on it.
func FoldUsageGroupByProvider(rows []UsageGroupRow) []UsageGroupRow {
	index := make(map[string]int, len(rows))
	out := make([]UsageGroupRow, 0, len(rows))
	for _, row := range rows {
		i, seen := index[row.Provider]
		if !seen {
			index[row.Provider] = len(out)
			out = append(out, UsageGroupRow{Provider: row.Provider})
			i = len(out) - 1
		}
		agg := &out[i]
		agg.Requests += row.Requests
		agg.Errors += row.Errors
		agg.PromptTokens += row.PromptTokens
		agg.CompletionTokens += row.CompletionTokens
		agg.Cost += row.Cost
		if row.LastUsed > agg.LastUsed {
			agg.LastUsed = row.LastUsed
		}
	}
	return out
}

// TopUsageGroupByTokens returns the provider/model row that consumed the most
// tokens, folding the prompt and completion columns the caller already has.
//
// GetUsageTopModelSince answers the same question with its own GROUP BY, which
// was a third pass over the window for one row. Given the model-level rows the
// dashboard already read, the answer is a single linear scan with no query at
// all. Ties break on provider then model, matching the ORDER BY the SQL used.
func TopUsageGroupByTokens(rows []UsageGroupRow) (provider, model string, tokens int64, ok bool) {
	for _, row := range rows {
		total := int64(row.PromptTokens) + int64(row.CompletionTokens)
		if !ok || total > tokens || (total == tokens && (row.Provider < provider ||
			(row.Provider == provider && row.Model < model))) {
			provider, model, tokens, ok = row.Provider, row.Model, total, true
		}
	}
	return provider, model, tokens, ok
}

// GetUsageTopModelSince returns the model that consumed the most tokens since
// the cutoff, with the provider it ran on. The aggregate runs in SQL: reading
// the window's rows into the process to fold them there cost a transfer of
// every row in the window for two numbers.
func (r *Repo) GetUsageTopModelSince(since string) (provider, model string, tokens int64, ok bool, err error) {
	err = r.QueryRow( //nolint:noctx
		`SELECT COALESCE(provider, ''), COALESCE(model, ''),
		        SUM(COALESCE(promptTokens, 0) + COALESCE(completionTokens, 0))
		   FROM usageHistory WHERE timestamp >= ?
		  GROUP BY COALESCE(provider, ''), COALESCE(model, '')
		  ORDER BY 3 DESC, 1, 2
		  LIMIT 1`, since).
		Scan(&provider, &model, &tokens)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, false, nil
	}
	if err != nil {
		return "", "", 0, false, fmt.Errorf("query usage top model since %s: %w", since, err)
	}
	return provider, model, tokens, true, nil
}

// cacheHitTokenExpr renders one token extraction of
// GetUsageCacheHitTotalsSince: tokens is jsonb, valid by construction
// with absence as the same NULL, so the extraction is the bare
// truncating integer read with a COALESCE-to-zero.
func cacheHitTokenExpr(field string) string {
	return "COALESCE(" + jsonIntAt("tokens", field) + ", 0)"
}

// GetUsageCacheHitTotalsSince returns the cached and total input tokens of the
// window, as the cache-hit rate counts them.
//
// The counters live in the row's tokens JSON, so the read has to reach the
// row; it does that in SQL instead of in the process, because the process
// version shipped every row of the window — including the JSON payload — into
// memory to fold it there.
//
// The rules mirror the ones the row-level fold used:
//   - cached tokens are `cached_tokens`, and `cache_read_input_tokens` when
//     the first is zero;
//   - the input is the row's own promptTokens, and the JSON's prompt_tokens
//     when that column is zero;
//   - a request whose whole input is a cache read with no write is a
//     distorted import and stays out of both sums;
//   - the denominator is max(input, cached + writes), so a cache-exclusive
//     row counts its cached part.
func (r *Repo) GetUsageCacheHitTotalsSince(since string) (hit, total int64, err error) {
	err = r.QueryRow( //nolint:noctx
		`WITH raw AS (
		   SELECT COALESCE(promptTokens, 0) AS prompt_column,
		          `+cacheHitTokenExpr("cached_tokens")+` AS cached_primary,
		          `+cacheHitTokenExpr("cache_read_input_tokens")+` AS cached_read,
		          `+cacheHitTokenExpr("cache_creation_input_tokens")+` AS creation,
		          `+cacheHitTokenExpr("prompt_tokens")+` AS prompt_json
		     FROM usageHistory WHERE timestamp >= ?
		 ), resolved AS (
		   SELECT CASE WHEN cached_primary != 0 THEN cached_primary ELSE cached_read END AS cached,
		          CASE WHEN prompt_column != 0 THEN prompt_column ELSE prompt_json END AS prompt,
		          creation
		     FROM raw
		 ), counted AS (
		   SELECT cached, prompt, creation
		     FROM resolved
		    WHERE cached > 0 AND prompt > 0 AND NOT (cached = prompt AND creation <= 0)
		 )
		 SELECT COALESCE(SUM(cached), 0),
		        COALESCE(SUM(MAX(prompt, cached + creation)), 0)
		   FROM counted`, since).
		Scan(&hit, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("query usage cache hit totals since %s: %w", since, err)
	}
	return hit, total, nil
}

// RequestDetailPerfRow is one latency observation from the requestDetails
// ring. The numbers are read out of the stored payload by SQL, so the read
// never ships the payload itself: a stored request carries the request
// messages and the response body, and the ring holds thousands of them, so
// fetching every payload made the node-health table scale with the size of
// the stored traffic rather than with the number of rows.
type RequestDetailPerfRow struct {
	Provider         string
	Model            string
	TTFTMs           int64
	TotalMs          int64
	CompletionTokens int64
	OutputTokens     int64
}

// ModelLatencyAgg is one model's mean responsiveness over the retained window.
// Either average may be zero — a model whose responses never reported a
// first-token time has a total but no TTFT — so a consumer must read the
// Samples count alongside the numbers rather than treating zero as "fast".
type ModelLatencyAgg struct {
	Provider string
	Model    string
	TTFTMs   int64
	TotalMs  int64
	Samples  int64
}

// UsageChartRow is one usageHistory row as the chart needs it: the bucket
// timestamp, the model the family split is derived from, and the token and
// cost totals. The chart reads no other column — in particular not the tokens
// JSON — so it gets its own projection instead of the wide one the stats
// readers share.
type UsageChartRow struct {
	Timestamp        string
	Model            string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
}

// usageChartSinceQuery is the chart's projection of the ledger. The order is
// the caller's business, so the read carries no ORDER BY: the window is
// scanned through idx_uh_ts, and sorting every row of it afterwards would
// cost a sort of the whole window for nothing.
const usageChartSinceQuery = `SELECT timestamp, COALESCE(model, ''),
	       COALESCE(promptTokens, 0), COALESCE(completionTokens, 0), COALESCE(cost, 0.0)
	FROM usageHistory
	WHERE timestamp >= ?`

// GetUsageHistoryChartSince returns the chart projection of the usage history
// since the cutoff.
func (r *Repo) GetUsageHistoryChartSince(cutoff string) ([]UsageChartRow, error) {
	rows, err := r.Query(usageChartSinceQuery, cutoff) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("query usageHistory chart since %s: %w", cutoff, err)
	}
	defer func() { _ = rows.Close() }()
	var res []UsageChartRow
	for rows.Next() {
		var row UsageChartRow
		if err := rows.Scan(&row.Timestamp, &row.Model, &row.PromptTokens,
			&row.CompletionTokens, &row.Cost); err != nil {
			return nil, fmt.Errorf("scan usageHistory chart since %s: %w", cutoff, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory chart since %s: %w", cutoff, err)
	}
	return res, nil
}

// GetUsageHistorySince returns usage history records since the cutoff timestamp.
func (r *Repo) GetUsageHistorySince(cutoff string) ([]UsageHistoryRow, error) {
	rows, err := r.Query(usageHistorySinceQuery, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query usageHistory since %s: %w", cutoff, err)
	}
	defer rows.Close()

	var res []UsageHistoryRow
	for rows.Next() {
		var row UsageHistoryRow
		if err := rows.Scan(
			&row.Timestamp, &row.Provider, &row.Model, &row.ConnectionID,
			&row.APIKey, &row.Endpoint, &row.PromptTokens, &row.CompletionTokens,
			&row.Cost, &row.Status, &row.Tokens,
		); err != nil {
			return nil, fmt.Errorf("scan usageHistory since %s: %w", cutoff, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory since %s: %w", cutoff, err)
	}
	return res, nil
}

// GetUsageHistoryTimestampsSince returns just the timestamps of usageHistory
// rows at or after the cutoff, oldest first. It backs the stats response's
// per-minute Request Stream series: the fold into minute buckets happens in
// the handler (the same parse-and-bucket pattern usage_chart.go uses), so the
// read stays a single timestamp-column projection answered by idx_uh_ts
// instead of dragging the token payload of every row in the window.
func (r *Repo) GetUsageHistoryTimestampsSince(since string) ([]string, error) {
	rows, err := r.Query(`SELECT timestamp FROM usageHistory WHERE timestamp >= ? ORDER BY timestamp ASC`, since) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("query usageHistory timestamps since %s: %w", since, err)
	}
	defer func() { _ = rows.Close() }()
	var res []string
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			return nil, fmt.Errorf("scan usageHistory timestamps since %s: %w", since, err)
		}
		res = append(res, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory timestamps since %s: %w", since, err)
	}
	return res, nil
}

// GetUsageHistoryWatermark returns the timestamp of the newest usageHistory row.
// It rides idx_uh_ts as a covering-index seek, so it stays O(log n) no matter
// how large the table grows — that is what makes a cached aggregate safe to
// refresh from a delta instead of re-reading the whole window.
func (r *Repo) GetUsageHistoryWatermark() (string, error) {
	var ts string
	if err := r.QueryRow(`SELECT COALESCE(MAX(timestamp), '') FROM usageHistory`).Scan(&ts); err != nil {
		return "", fmt.Errorf("read usageHistory watermark: %w", err)
	}
	return ts, nil
}

// GetUsageHistoryBetween returns rows in (since, until] — the delta a cached
// window aggregate has yet to fold in. The bounds are exclusive on the low end
// so a row already folded in at the watermark is never counted twice.
func (r *Repo) GetUsageHistoryBetween(since, until string) ([]UsageHistoryRow, error) {
	rows, err := r.Query(usageHistoryBetweenQuery, since, until)
	if err != nil {
		return nil, fmt.Errorf("query usageHistory between %s and %s: %w", since, until, err)
	}
	defer rows.Close()

	var res []UsageHistoryRow
	for rows.Next() {
		var row UsageHistoryRow
		if err := rows.Scan(&row.Timestamp, &row.Provider, &row.Model, &row.ConnectionID,
			&row.APIKey, &row.Endpoint, &row.PromptTokens, &row.CompletionTokens,
			&row.Cost, &row.Status, &row.Tokens); err != nil {
			return nil, fmt.Errorf("scan usageHistory between %s and %s: %w", since, until, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory between %s and %s: %w", since, until, err)
	}
	return res, nil
}

// GetRecentUsageHistory returns the latest N usage history records.
func (r *Repo) GetRecentUsageHistory(limit int) ([]UsageHistoryRow, error) {
	rows, err := r.Query(usageHistoryRecentQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent usageHistory: %w", err)
	}
	defer rows.Close()

	var res []UsageHistoryRow
	for rows.Next() {
		var row UsageHistoryRow
		if err := rows.Scan(
			&row.Timestamp, &row.Provider, &row.Model, &row.ConnectionID,
			&row.APIKey, &row.Endpoint, &row.PromptTokens, &row.CompletionTokens,
			&row.Cost, &row.Status, &row.Tokens,
		); err != nil {
			return nil, fmt.Errorf("scan recent usageHistory: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent usageHistory: %w", err)
	}
	return res, nil
}

// RequestDetailListRow is one row of the Details table: every value the table
// renders, and nothing else.
//
// The stored payload also carries the request messages and the response body —
// up to MaxLoggedMessages × MaxMessageContentLen plus MaxResponseContentLen
// characters, which measured ~20 KB per row. A page of 20 is 400 KB that the
// table never reads: it shows timestamps, provider, model, latency and token
// counts, and only the View action wants the text. So the list query selects
// those values out of the JSON and the payload stays behind
// GetRequestDetailByID until a row is actually opened.
type RequestDetailListRow struct {
	ID           string
	Timestamp    string
	Provider     string
	Model        string
	ConnectionID string
	Status       string
	LatencyTTFT  int64
	LatencyTotal int64
	TokensJSON   string
	Account      string
	Cost         float64
	Combo        string
	Protocol     string
	LoopWarning  string
	Proxy        string
	// IsStream is the mode the client asked for, extracted from the stored
	// payload. Rows written before the payload carried the flag read as
	// true — upstream's own default for an unrecorded mode
	// (chatCore/requestDetail.js: isStream ?? true).
	IsStream bool
	// The gateway caller behind the request: the client API key's name and
	// masked form, and the client IP the entry points resolved. All three
	// are empty on rows written before the payload recorded them, and on
	// keyless calls.
	APIKeyName   string
	ClientAPIKey string
	ClientIP     string
	// SmartDecisionJSON is the smart router's recorded decision
	// sub-document, verbatim; empty when the request was not routed by a
	// smart combo (or predates the recording).
	SmartDecisionJSON string
}

// ComboAnalyticsRow is one (combo, resolved model, provider) aggregate of the
// request-details ledger, backing GET /api/combos/analytics. Errors are not
// carried: the handler derives them as Total - Success, matching how the
// column vocabulary works — detail records are only ever written with
// "success" or "error", so every non-success row is an error row.
type ComboAnalyticsRow struct {
	ComboName   string
	Model       string
	Provider    string
	Total       int
	Success     int
	AvgLatencyS float64
	LastSeen    string
}
