package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/handlerutil"
)

// RollupUsageHistoryBeforeDate scans all usageHistory rows strictly older than
// cutoff, aggregates their metrics grouped by calendar day, and merges them
// into usageDaily. It guarantees historical metrics are safely captured in
// daily summaries BEFORE the retention purge deletes the raw rows.
//
// Two properties make this safe to run on every prune:
//
//   - It never double counts. The live request path writes usageDaily in real
//     time, so a day that already has a summary must NOT absorb its raw rows
//     again. The rollup backfills only the days the live path never
//     summarised — the days the gateway was offline.
//
//   - It never clobbers. The write goes through Repo.FoldUsageDaily, the same
//     transactional fold the live path uses, so a summary that landed between
//     the rollup's read and its write is merged rather than overwritten, and
//     the day row is locked for the duration of the merge.
//
// The date key is the process-local calendar day of the row's UTC timestamp.
// That is the anchor the live daily writer uses (time.Now().Format("2006-01-02"))
// and the anchor the chart reader buckets by (handlers.dayKeyFor), so a day's
// traffic lands in one bucket instead of being split across two.
//
// Returns the number of days actually written to usageDaily.
func (r *Repo) RollupUsageHistoryBeforeDate(ctx context.Context, cutoff string) (int, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("rollup usageHistory: nil repo")
	}

	rows, err := r.QueryContext(ctx, `
		SELECT timestamp, COALESCE(provider, ''), COALESCE(model, ''), COALESCE(connectionId, ''),
		       COALESCE(apiKey, ''), COALESCE(endpoint, ''), COALESCE(promptTokens, 0),
		       COALESCE(completionTokens, 0), COALESCE(cost, 0.0), COALESCE(status, 'ok'),
		       COALESCE(tokens, '{}')
		FROM usageHistory
		WHERE timestamp < ?
		ORDER BY timestamp ASC
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("query usageHistory before cutoff %s: %w", cutoff, err)
	}
	defer func() { _ = rows.Close() }()

	dailyMap := make(map[string]map[string]any)
	// skipDaily holds the date keys that already carry a live usageDaily
	// summary. The check is a pre-filter: it keeps the rollup from
	// aggregating millions of rows it would then discard, and the fold below
	// is the authoritative guard (it re-checks under the row lock).
	skipDaily := make(map[string]bool)

	for rows.Next() {
		var ts, provider, model, connID, apiKey, endpoint, status, tokensJSON string
		var prompt, compl int
		var cost float64
		if err := rows.Scan(&ts, &provider, &model, &connID, &apiKey, &endpoint, &prompt, &compl, &cost, &status, &tokensJSON); err != nil {
			return 0, fmt.Errorf("scan usageHistory for rollup: %w", err)
		}

		dateKey, ok := rollupDateKey(ts)
		if !ok {
			continue
		}
		if skipDaily[dateKey] {
			continue
		}
		agg := dailyMap[dateKey]
		if agg == nil {
			existingJSON, err := r.GetUsageDaily(dateKey)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("get daily usage %s before rollup: %w", dateKey, err)
			}
			if existingJSON != "" {
				skipDaily[dateKey] = true
				continue
			}
			agg = make(map[string]any)
			dailyMap[dateKey] = agg
		}

		cached := extractCachedTokens(tokensJSON)
		mergeDailyUsageItem(agg, provider, model, endpoint, connID, apiKey, prompt, compl, cached, cost, status)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate usageHistory for rollup: %w", err)
	}

	written := 0
	for dateKey, agg := range dailyMap {
		payload, err := json.Marshal(agg)
		if err != nil {
			return written, fmt.Errorf("marshal rollup daily %s: %w", dateKey, err)
		}
		ok, err := r.foldRollupDaily(dateKey, string(payload))
		if err != nil {
			return written, fmt.Errorf("save rollup daily %s: %w", dateKey, err)
		}
		if ok {
			written++
		}
	}

	return written, nil
}

// foldRollupDaily writes one day's rolled-up aggregate into usageDaily under
// the day row's lock, and reports whether the aggregate was written.
//
// It refuses to write when the day already carries a summary: the live path
// wrote that summary from the same traffic the raw rows describe, so adding
// the rows again would count them twice. The refusal happens inside the fold,
// under the same lock the live path's fold takes, so a summary that lands
// between the rollup's read and its write is seen here — the rollup backs off
// rather than erasing it.
func (r *Repo) foldRollupDaily(dateKey, payload string) (bool, error) {
	wrote := false
	err := r.FoldUsageDaily(dateKey, func(existing string) (string, error) {
		if rollupDailyHasSummary(existing) {
			// A live summary already covers this day: keep it verbatim.
			return existing, nil
		}
		wrote = true
		return payload, nil
	})
	if err != nil {
		return false, err
	}
	return wrote, nil
}

// rollupDailyHasSummary reports whether a stored daily blob already carries a
// summary. The empty base FoldUsageDaily writes for a day it has to create is
// "{}"; anything with content is a summary the live path (or an earlier
// rollup) already wrote.
func rollupDailyHasSummary(existing string) bool {
	existing = strings.TrimSpace(existing)
	if existing == "" || existing == "{}" {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(existing), &m); err != nil {
		// An unparseable blob is treated as no summary: the rollup replaces
		// it with a well-formed aggregate rather than leaving the day
		// unreadable.
		return false
	}
	return len(m) > 0
}

// rollupDateKey derives the local calendar day of a stored UTC RFC3339
// timestamp. The ledger stores RFC3339 with second precision, so the parse
// covers every row the writer produces; the prefix fallback covers a legacy
// row that carries only a date.
func rollupDateKey(ts string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err == nil {
		return parsed.In(time.Local).Format("2006-01-02"), true
	}
	if len(ts) >= 10 {
		return ts[:10], true
	}
	return "", false
}

// extractCachedTokens reads cached prompt tokens from the raw tokens JSON
// payload, matching translator.CachedTokensFromJSON precedence without
// introducing a package cycle (internal/translator imports internal/db).
func extractCachedTokens(raw string) int {
	if raw == "" {
		return 0
	}
	var usage struct {
		CachedTokens          *float64 `json:"cached_tokens"`
		CacheReadInputTokens  *float64 `json:"cache_read_input_tokens"`
		PromptEvalCachedCount *float64 `json:"prompt_eval_cached_count"`
		PromptTokensDetails   *struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return 0
	}
	var promptCached *float64
	if usage.PromptTokensDetails != nil {
		promptCached = usage.PromptTokensDetails.CachedTokens
	}
	var inputCached *float64
	if usage.InputTokensDetails != nil {
		inputCached = usage.InputTokensDetails.CachedTokens
	}
	for _, candidate := range []*float64{
		usage.CachedTokens,
		usage.CacheReadInputTokens,
		promptCached,
		inputCached,
		usage.PromptEvalCachedCount,
	} {
		if candidate != nil && *candidate >= 0 {
			return int(*candidate)
		}
	}
	return 0
}

func getMapIntVal(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		if n, ok := v.(float64); ok {
			return int(n)
		}
		if n, ok := v.(int); ok {
			return n
		}
	}
	return 0
}

func getMapFloatVal(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if n, ok := v.(float64); ok {
			return n
		}
		if n, ok := v.(int); ok {
			return float64(n)
		}
	}
	return 0
}

func getNestedMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if sub, ok := v.(map[string]any); ok {
			return sub
		}
	}
	return make(map[string]any)
}

// mergeDailyUsageItem folds one ledger row into the parsed daily aggregate.
//
// It mirrors the live path's two folds exactly: a successful row folds like
// chat.mergeDailyUsage, a failed one like chat.mergeDailyUsageError (requests
// and errors, no token movement, and no per-key or per-endpoint breakdown —
// a failed request consumed no tokens and is attributed to no key).
//
// apiKey arrives already masked, the way the ledger writer stores it.
// Masking it again is a no-op (the mask of a mask is the mask), so the
// per-key bucket carries the same key the live path wrote.
func mergeDailyUsageItem(data map[string]any, provider, model, endpoint, connectionID, apiKey string, promptTokens, completionTokens, cachedTokens int, cost float64, status string) {
	if !isSuccessStatus(status) {
		mergeDailyUsageErrorItem(data, provider, model)
		return
	}

	data["requests"] = getMapIntVal(data, "requests") + 1
	data["promptTokens"] = getMapIntVal(data, "promptTokens") + promptTokens
	data["completionTokens"] = getMapIntVal(data, "completionTokens") + completionTokens
	data["cachedTokens"] = getMapIntVal(data, "cachedTokens") + cachedTokens
	data["cost"] = getMapFloatVal(data, "cost") + cost

	data["totalPromptTokens"] = getMapIntVal(data, "totalPromptTokens") + promptTokens
	data["totalCompletionTokens"] = getMapIntVal(data, "totalCompletionTokens") + completionTokens
	data["totalRequests"] = getMapIntVal(data, "totalRequests") + 1

	// byProvider
	byProvider := getNestedMap(data, "byProvider")
	pp := getNestedMap(byProvider, provider)
	pp["requests"] = getMapIntVal(pp, "requests") + 1
	pp["promptTokens"] = getMapIntVal(pp, "promptTokens") + promptTokens
	pp["completionTokens"] = getMapIntVal(pp, "completionTokens") + completionTokens
	pp["cachedTokens"] = getMapIntVal(pp, "cachedTokens") + cachedTokens
	pp["cost"] = getMapFloatVal(pp, "cost") + cost
	byProvider[provider] = pp
	data["byProvider"] = byProvider

	// byModel
	modelKey := model + "|" + provider
	byModel := getNestedMap(data, "byModel")
	mm := getNestedMap(byModel, modelKey)
	mm["requests"] = getMapIntVal(mm, "requests") + 1
	mm["promptTokens"] = getMapIntVal(mm, "promptTokens") + promptTokens
	mm["completionTokens"] = getMapIntVal(mm, "completionTokens") + completionTokens
	mm["cachedTokens"] = getMapIntVal(mm, "cachedTokens") + cachedTokens
	mm["cost"] = getMapFloatVal(mm, "cost") + cost
	if mm["rawModel"] == nil {
		mm["rawModel"] = model
	}
	if mm["provider"] == nil {
		mm["provider"] = provider
	}
	byModel[modelKey] = mm
	data["byModel"] = byModel

	// byApiKey
	apiKeyVal := handlerutil.MaskAPIKey(apiKey)
	if apiKeyVal == "" {
		apiKeyVal = "local-no-key"
	}
	akKey := apiKeyVal + "|" + model + "|" + provider
	byApiKey := getNestedMap(data, "byApiKey")
	ak := getNestedMap(byApiKey, akKey)
	ak["requests"] = getMapIntVal(ak, "requests") + 1
	ak["promptTokens"] = getMapIntVal(ak, "promptTokens") + promptTokens
	ak["completionTokens"] = getMapIntVal(ak, "completionTokens") + completionTokens
	ak["cachedTokens"] = getMapIntVal(ak, "cachedTokens") + cachedTokens
	ak["cost"] = getMapFloatVal(ak, "cost") + cost
	ak["rawModel"] = model
	ak["provider"] = provider
	ak["apiKey"] = apiKeyVal
	byApiKey[akKey] = ak
	data["byApiKey"] = byApiKey

	// byEndpoint
	epKey := endpoint + "|" + model + "|" + provider
	byEndpoint := getNestedMap(data, "byEndpoint")
	ep := getNestedMap(byEndpoint, epKey)
	ep["requests"] = getMapIntVal(ep, "requests") + 1
	ep["promptTokens"] = getMapIntVal(ep, "promptTokens") + promptTokens
	ep["completionTokens"] = getMapIntVal(ep, "completionTokens") + completionTokens
	ep["cachedTokens"] = getMapIntVal(ep, "cachedTokens") + cachedTokens
	ep["cost"] = getMapFloatVal(ep, "cost") + cost
	if ep["endpoint"] == nil {
		ep["endpoint"] = endpoint
	}
	if ep["rawModel"] == nil {
		ep["rawModel"] = model
	}
	if ep["provider"] == nil {
		ep["provider"] = provider
	}
	byEndpoint[epKey] = ep
	data["byEndpoint"] = byEndpoint

	// byAccount
	if connectionID != "" {
		byAccount := getNestedMap(data, "byAccount")
		acc := getNestedMap(byAccount, connectionID)
		acc["requests"] = getMapIntVal(acc, "requests") + 1
		acc["promptTokens"] = getMapIntVal(acc, "promptTokens") + promptTokens
		acc["completionTokens"] = getMapIntVal(acc, "completionTokens") + completionTokens
		acc["cachedTokens"] = getMapIntVal(acc, "cachedTokens") + cachedTokens
		acc["cost"] = getMapFloatVal(acc, "cost") + cost
		if acc["rawModel"] == nil {
			acc["rawModel"] = model
		}
		if acc["provider"] == nil {
			acc["provider"] = provider
		}
		byAccount[connectionID] = acc
		data["byAccount"] = byAccount
	}

	// providers (legacy nested format)
	providers := getNestedMap(data, "providers")
	pd := getNestedMap(providers, provider)
	pd["promptTokens"] = getMapIntVal(pd, "promptTokens") + promptTokens
	pd["completionTokens"] = getMapIntVal(pd, "completionTokens") + completionTokens
	pd["requests"] = getMapIntVal(pd, "requests") + 1

	models := getNestedMap(pd, "models")
	md := getNestedMap(models, model)
	md["promptTokens"] = getMapIntVal(md, "promptTokens") + promptTokens
	md["completionTokens"] = getMapIntVal(md, "completionTokens") + completionTokens
	md["requests"] = getMapIntVal(md, "requests") + 1
	models[model] = md
	pd["models"] = models
	providers[provider] = pd
	data["providers"] = providers
}

// mergeDailyUsageErrorItem folds one failed ledger row into the parsed daily
// aggregate: the request and error counters plus the provider and model
// breakdowns, with no token movement and no per-key attribution.
func mergeDailyUsageErrorItem(data map[string]any, provider, model string) {
	data["requests"] = getMapIntVal(data, "requests") + 1
	data["totalRequests"] = getMapIntVal(data, "totalRequests") + 1
	data["errors"] = getMapIntVal(data, "errors") + 1

	byProvider := getNestedMap(data, "byProvider")
	pp := getNestedMap(byProvider, provider)
	pp["requests"] = getMapIntVal(pp, "requests") + 1
	pp["errors"] = getMapIntVal(pp, "errors") + 1
	byProvider[provider] = pp
	data["byProvider"] = byProvider

	modelKey := model + "|" + provider
	byModel := getNestedMap(data, "byModel")
	mm := getNestedMap(byModel, modelKey)
	mm["requests"] = getMapIntVal(mm, "requests") + 1
	mm["errors"] = getMapIntVal(mm, "errors") + 1
	if mm["rawModel"] == nil {
		mm["rawModel"] = model
	}
	if mm["provider"] == nil {
		mm["provider"] = provider
	}
	byModel[modelKey] = mm
	data["byModel"] = byModel
}

// isSuccessStatus reports whether a ledger row's status marks it as a
// successful request.
//
// The ledger carries two spellings for success: the chat writer stores
// "success" and the report writer stores "ok" (handlers.mapReportStatus).
// Everything else is a failure.
func isSuccessStatus(status string) bool {
	return status == "success" || status == "ok"
}
