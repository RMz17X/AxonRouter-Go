package handlers

import (
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// GET /api/usage/analytics follows AxonRouter's LIVE route.js contract: the
// summary its analyticsRepo computes over its per-request analytics_events
// stream. The same per-request record here is the requestDetails ledger, so
// the handler folds ledger rows (db.GetAnalyticsEvents) into the summary in
// Go — that is what reproduces the Postgres PERCENTILE_CONT and DATE_BIN
// semantics exactly rather than approximating them in the database.

const analyticsMinSampleThreshold = 30

var analyticsBuckets = map[string]time.Duration{
	"1 minute":  time.Minute,
	"5 minutes": 5 * time.Minute,
	"1 hour":    time.Hour,
	"1 day":     24 * time.Hour,
}

var analyticsErrorCategories = map[string]bool{
	"upstream": true, "rate_limit": true, "auth": true, "timeout": true,
	"cancelled": true, "stream": true, "internal": true, "unknown": true,
}

var analyticsTimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?(?:Z|[+-]\d{2}:\d{2})$`)

type analyticsFilters struct {
	start         time.Time
	end           time.Time
	bucket        string
	bucketDur     time.Duration
	provider      string
	model         string
	errorCategory string
}

// validateAnalyticsFilters ports AxonRouter's validateAnalyticsFilters: the
// same defaults (last 7 days), the same checks in the same order, and the
// same messages, so a rejected request reads identically on both builds.
// now is truncated to milliseconds, matching Date.now().
func validateAnalyticsFilters(raw map[string]string, now time.Time) (analyticsFilters, string) {
	now = now.Truncate(time.Millisecond)
	parseDate := func(key string, fallback time.Time) (time.Time, bool) {
		value, present := raw[key]
		if !present {
			return fallback, true
		}
		if !analyticsTimePattern.MatchString(value) {
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	end, ok := parseDate("timeTo", now)
	if !ok {
		return analyticsFilters{}, "Time filters must be ISO 8601 timestamps with timezone"
	}
	start, ok := parseDate("timeFrom", end.Add(-7*24*time.Hour))
	if !ok {
		return analyticsFilters{}, "Time filters must be ISO 8601 timestamps with timezone"
	}
	if !start.Before(end) || end.Sub(start) > 90*24*time.Hour || end.After(now.Add(time.Minute)) {
		return analyticsFilters{}, "Invalid time range (maximum 90 days)"
	}
	bucket := raw["timeBucket"]
	if bucket == "" {
		bucket = "1 hour"
	}
	bucketDur, known := analyticsBuckets[bucket]
	if !known {
		return analyticsFilters{}, "Invalid or overly granular timeBucket (maximum 2200 buckets)"
	}
	diffMs := end.Sub(start).Milliseconds()
	if (diffMs+bucketDur.Milliseconds()-1)/bucketDur.Milliseconds() > 2200 {
		return analyticsFilters{}, "Invalid or overly granular timeBucket (maximum 2200 buckets)"
	}
	errorCategory := raw["errorCategory"]
	if errorCategory != "" && !analyticsErrorCategories[errorCategory] {
		return analyticsFilters{}, "Invalid errorCategory"
	}
	provider, errMsg := validateAnalyticsDimension(raw["provider"], 64, "provider")
	if errMsg != "" {
		return analyticsFilters{}, errMsg
	}
	model, errMsg := validateAnalyticsDimension(raw["model"], 256, "model")
	if errMsg != "" {
		return analyticsFilters{}, errMsg
	}
	return analyticsFilters{
		start: start, end: end, bucket: bucket, bucketDur: bucketDur,
		provider: provider, model: model, errorCategory: errorCategory,
	}, ""
}

// validateAnalyticsDimension ports AxonRouter's validateFilterDimension for
// a query-string value (always a string here; absent and empty both mean
// "no filter", because the route only forwards non-empty values).
func validateAnalyticsDimension(value string, max int, name string) (string, string) {
	if value == "" {
		return "", ""
	}
	if strings.TrimSpace(value) != value {
		return "", "Invalid " + name + ": must not contain leading or trailing whitespace"
	}
	if utf8.RuneCountInString(value) > max {
		return "", "Invalid " + name + ": maximum " + strconv.Itoa(max) + " characters exceeded"
	}
	for _, c := range value {
		if c < 0x20 || c == 0x7f {
			return "", "Invalid " + name + ": contains illegal control characters"
		}
	}
	return value, ""
}

// categorizeAnalyticsError ports AxonRouter's categorizeError: the numeric
// status rules first (NaN — an absent or non-numeric status — matches none
// of them), then the message keyword chain in its original order.
func categorizeAnalyticsError(message string, status float64) string {
	if status == 401 || status == 403 {
		return "auth"
	}
	if status == 429 {
		return "rate_limit"
	}
	if status == 408 || status == 504 {
		return "timeout"
	}
	if status >= 500 {
		return "upstream"
	}
	msg := strings.ToLower(message)
	switch {
	case strings.Contains(msg, "abort") || strings.Contains(msg, "cancel"):
		return "cancelled"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, "stream") || strings.Contains(msg, "transform") || strings.Contains(msg, "pipe"):
		return "stream"
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "key"):
		return "auth"
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "quota") || strings.Contains(msg, "429"):
		return "rate_limit"
	case strings.Contains(msg, "upstream") || strings.Contains(msg, "500") || strings.Contains(msg, "502") || strings.Contains(msg, "503"):
		return "upstream"
	}
	return "unknown"
}

// parseAnalyticsDate accepts the date forms the usage endpoints' clients
// send: full RFC3339 instants and bare calendar dates (UTC midnight, like
// JS Date). Shared with the failures handler.
func parseAnalyticsDate(raw string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// percentileCont reproduces Postgres PERCENTILE_CONT: linear interpolation
// between the two closest ranks of the sorted samples. Nil for no samples.
func percentileCont(sorted []float64, p float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	if len(sorted) == 1 {
		v := sorted[0]
		return &v
	}
	idx := p * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	v := sorted[lo] + (sorted[hi]-sorted[lo])*(idx-float64(lo))
	return &v
}

func roundAnalytics(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

func roundedPtr(v *float64, places int) *float64 {
	if v == nil {
		return nil
	}
	r := roundAnalytics(*v, places)
	return &r
}

type analyticsMeta struct {
	TimeFrom           string  `json:"timeFrom"`
	TimeTo             string  `json:"timeTo"`
	Provider           *string `json:"provider"`
	Model              *string `json:"model"`
	ErrorCategory      *string `json:"errorCategory"`
	TimeBucket         string  `json:"timeBucket"`
	MinSampleThreshold int     `json:"minSampleThreshold"`
}

type analyticsSummary struct {
	TotalEvents            int      `json:"totalEvents"`
	SuccessCount           int      `json:"successCount"`
	FailureCount           int      `json:"failureCount"`
	SuccessRate            float64  `json:"successRate"`
	P50LatencyMs           *float64 `json:"p50LatencyMs"`
	P95LatencyMs           *float64 `json:"p95LatencyMs"`
	TotalInputTokens       int64    `json:"totalInputTokens"`
	TotalOutputTokens      int64    `json:"totalOutputTokens"`
	RecommendationEligible bool     `json:"recommendationEligible"`
}

// The byModel and timeline rows ride the wire with the Postgres column
// names, exactly as AxonRouter returns its raw query rows.
type analyticsModelRow struct {
	Provider               string   `json:"provider"`
	Model                  string   `json:"model"`
	Count                  int      `json:"count"`
	SuccessCount           int      `json:"success_count"`
	FailureCount           int      `json:"failure_count"`
	SuccessRate            float64  `json:"success_rate"`
	P50LatencyMs           *float64 `json:"p50_latency_ms"`
	LatencySamples         int      `json:"latency_samples"`
	P95LatencyMs           *float64 `json:"p95_latency_ms"`
	TotalInputTokens       *int64   `json:"total_input_tokens"`
	TotalOutputTokens      *int64   `json:"total_output_tokens"`
	RecommendationEligible bool     `json:"recommendation_eligible"`
}

type analyticsProviderRow struct {
	Provider          string         `json:"provider"`
	Count             int            `json:"count"`
	SuccessCount      int            `json:"successCount"`
	FailureCount      int            `json:"failureCount"`
	SuccessRate       float64        `json:"successRate"`
	P50LatencyMs      *float64       `json:"p50LatencyMs"`
	P95LatencyMs      *float64       `json:"p95LatencyMs"`
	TotalInputTokens  int64          `json:"totalInputTokens"`
	TotalOutputTokens int64          `json:"totalOutputTokens"`
	ErrorBreakdown    map[string]int `json:"errorBreakdown"`
}

type analyticsErrorRow struct {
	ErrorCategory string `json:"error_category"`
	Count         int    `json:"count"`
}

type analyticsTimelineRow struct {
	Bucket            string   `json:"bucket"`
	Count             int      `json:"count"`
	SuccessCount      int      `json:"success_count"`
	FailureCount      int      `json:"failure_count"`
	P50LatencyMs      *float64 `json:"p50_latency_ms"`
	LatencySamples    int      `json:"latency_samples"`
	P95LatencyMs      *float64 `json:"p95_latency_ms"`
	TotalInputTokens  *int64   `json:"total_input_tokens"`
	TotalOutputTokens *int64   `json:"total_output_tokens"`
}

type analyticsResponse struct {
	Meta              analyticsMeta          `json:"meta"`
	Summary           analyticsSummary       `json:"summary"`
	ByModel           []analyticsModelRow    `json:"byModel"`
	ByProvider        []analyticsProviderRow `json:"byProvider"`
	ErrorDistribution []analyticsErrorRow    `json:"errorDistribution"`
	Timeline          []analyticsTimelineRow `json:"timeline"`
}

// analyticsAgg accumulates one group of events (overall, a model, a
// provider, or a timeline bucket). Token sums keep the Postgres SUM
// semantics the raw rows expose: a group whose every event lacks a count
// sums to null, which the summary and byProvider mappings coerce to 0.
type analyticsAgg struct {
	count, success, failure int
	latencies               []float64
	inSum, outSum           int64
	inSeen, outSeen         bool
}

func (a *analyticsAgg) add(ev analyticsEvent) {
	a.count++
	if ev.success {
		a.success++
		if ev.latency != nil {
			a.latencies = append(a.latencies, *ev.latency)
		}
	} else {
		a.failure++
	}
	if ev.inTok != nil {
		a.inSum += *ev.inTok
		a.inSeen = true
	}
	if ev.outTok != nil {
		a.outSum += *ev.outTok
		a.outSeen = true
	}
}

func (a *analyticsAgg) successRate() float64 {
	if a.count == 0 {
		return 0
	}
	return roundAnalytics(float64(a.success)/float64(a.count)*100, 2)
}

func (a *analyticsAgg) p50() *float64 {
	sort.Float64s(a.latencies)
	return percentileCont(a.latencies, 0.50)
}

func (a *analyticsAgg) p95() *float64 {
	return percentileCont(a.latencies, 0.95)
}

func (a *analyticsAgg) inTotal() *int64 {
	if !a.inSeen {
		return nil
	}
	v := a.inSum
	return &v
}

func (a *analyticsAgg) outTotal() *int64 {
	if !a.outSeen {
		return nil
	}
	v := a.outSum
	return &v
}

type analyticsEvent struct {
	ts       time.Time
	tsOK     bool
	provider string
	model    string
	success  bool
	latency  *float64
	inTok    *int64
	outTok   *int64
	category string // failures only; "" for successes
}

// HandleUsageAnalytics serves GET /api/usage/analytics.
func HandleUsageAnalytics(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		raw := make(map[string]string)
		for _, key := range []string{"timeFrom", "timeTo", "provider", "model", "timeBucket", "errorCategory"} {
			if v := q.Get(key); v != "" {
				raw[key] = v
			}
		}
		filters, errMsg := validateAnalyticsFilters(raw, time.Now())
		if errMsg != "" {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
			return
		}

		rows, err := repo.GetAnalyticsEvents(r.Context(),
			filters.start.UTC().Format("2006-01-02T15:04:05.000Z"),
			filters.end.UTC().Format("2006-01-02T15:04:05.000Z"),
			filters.provider, filters.model)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch analytics"})
			return
		}

		events := make([]analyticsEvent, 0, len(rows))
		for _, row := range rows {
			ev := analyticsEvent{provider: row.Provider, model: row.Model, success: row.Success}
			if row.LatencyMs.Valid {
				v := row.LatencyMs.Float64
				ev.latency = &v
			}
			if row.InputTokens.Valid {
				v := row.InputTokens.Int64
				ev.inTok = &v
			}
			if row.OutputTokens.Valid {
				v := row.OutputTokens.Int64
				ev.outTok = &v
			}
			if ts, perr := time.Parse(time.RFC3339Nano, row.Timestamp); perr == nil {
				ev.ts, ev.tsOK = ts, true
				// The SQL window compares stored strings; re-check the
				// exact instants so a row on a boundary counts the way
				// AxonRouter's timestamp comparison counts it.
				if ts.Before(filters.start) || ts.After(filters.end) {
					continue
				}
			}
			if !ev.success {
				if analyticsErrorCategories[row.ErrorCategory] {
					ev.category = row.ErrorCategory
				} else {
					// Number(status) in AxonRouter: a non-numeric status
					// is NaN and matches none of the numeric rules.
					status := math.NaN()
					if s := strings.TrimSpace(row.ErrorStatus); s != "" {
						if v, perr := strconv.ParseFloat(s, 64); perr == nil {
							status = v
						}
					}
					ev.category = categorizeAnalyticsError(row.ErrorText, status)
				}
				// AxonRouter applies the category as a WHERE clause, so a
				// filtered summary counts only that category's failures.
				if filters.errorCategory != "" && ev.category != filters.errorCategory {
					continue
				}
			} else if filters.errorCategory != "" {
				// Successes carry no category, so a category filter
				// excludes them, exactly as the SQL predicate does.
				continue
			}
			events = append(events, ev)
		}

		overall := &analyticsAgg{}
		modelAggs := make(map[string]*analyticsAgg)
		providerAggs := make(map[string]*analyticsAgg)
		providerErrors := make(map[string]map[string]int)
		errorDist := make(map[string]int)
		bucketAggs := make(map[time.Time]*analyticsAgg)
		origin := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) // DATE_BIN origin in AxonRouter's timeline query
		for _, ev := range events {
			overall.add(ev)
			key := ev.provider + "\x00" + ev.model
			agg := modelAggs[key]
			if agg == nil {
				agg = &analyticsAgg{}
				modelAggs[key] = agg
			}
			agg.add(ev)
			pagg := providerAggs[ev.provider]
			if pagg == nil {
				pagg = &analyticsAgg{}
				providerAggs[ev.provider] = pagg
			}
			pagg.add(ev)
			if !ev.success {
				errorDist[ev.category]++
				byCat := providerErrors[ev.provider]
				if byCat == nil {
					byCat = make(map[string]int)
					providerErrors[ev.provider] = byCat
				}
				byCat[ev.category]++
			}
			if ev.tsOK {
				steps := ev.ts.Sub(origin) / filters.bucketDur
				bucketStart := origin.Add(steps * filters.bucketDur)
				bagg := bucketAggs[bucketStart]
				if bagg == nil {
					bagg = &analyticsAgg{}
					bucketAggs[bucketStart] = bagg
				}
				bagg.add(ev)
			}
		}

		resp := analyticsResponse{
			ByModel:           make([]analyticsModelRow, 0, len(modelAggs)),
			ByProvider:        make([]analyticsProviderRow, 0),
			ErrorDistribution: make([]analyticsErrorRow, 0, len(errorDist)),
			Timeline:          make([]analyticsTimelineRow, 0, len(bucketAggs)),
		}
		resp.Meta = analyticsMeta{
			TimeFrom:           filters.start.UTC().Format("2006-01-02T15:04:05.000Z"),
			TimeTo:             filters.end.UTC().Format("2006-01-02T15:04:05.000Z"),
			TimeBucket:         filters.bucket,
			MinSampleThreshold: analyticsMinSampleThreshold,
		}
		if filters.provider != "" {
			resp.Meta.Provider = &filters.provider
		}
		if filters.model != "" {
			resp.Meta.Model = &filters.model
		}
		if filters.errorCategory != "" {
			resp.Meta.ErrorCategory = &filters.errorCategory
		}
		resp.Summary = analyticsSummary{
			TotalEvents:            overall.count,
			SuccessCount:           overall.success,
			FailureCount:           overall.failure,
			SuccessRate:            overall.successRate(),
			P50LatencyMs:           roundedPtr(overall.p50(), 2),
			P95LatencyMs:           roundedPtr(overall.p95(), 2),
			RecommendationEligible: overall.count >= analyticsMinSampleThreshold,
		}
		if v := overall.inTotal(); v != nil {
			resp.Summary.TotalInputTokens = *v
		}
		if v := overall.outTotal(); v != nil {
			resp.Summary.TotalOutputTokens = *v
		}

		type modelKey struct{ provider, model string }
		modelRows := make([]struct {
			key modelKey
			agg *analyticsAgg
		}, 0, len(modelAggs))
		for key, agg := range modelAggs {
			parts := strings.SplitN(key, "\x00", 2)
			modelRows = append(modelRows, struct {
				key modelKey
				agg *analyticsAgg
			}{modelKey{parts[0], parts[1]}, agg})
		}
		sort.Slice(modelRows, func(i, j int) bool {
			if modelRows[i].agg.count != modelRows[j].agg.count {
				return modelRows[i].agg.count > modelRows[j].agg.count
			}
			if modelRows[i].key.provider != modelRows[j].key.provider {
				return modelRows[i].key.provider < modelRows[j].key.provider
			}
			return modelRows[i].key.model < modelRows[j].key.model
		})
		for _, mr := range modelRows {
			resp.ByModel = append(resp.ByModel, analyticsModelRow{
				Provider:               mr.key.provider,
				Model:                  mr.key.model,
				Count:                  mr.agg.count,
				SuccessCount:           mr.agg.success,
				FailureCount:           mr.agg.failure,
				SuccessRate:            mr.agg.successRate(),
				P50LatencyMs:           mr.agg.p50(),
				LatencySamples:         len(mr.agg.latencies),
				P95LatencyMs:           mr.agg.p95(),
				TotalInputTokens:       mr.agg.inTotal(),
				TotalOutputTokens:      mr.agg.outTotal(),
				RecommendationEligible: mr.agg.count >= analyticsMinSampleThreshold,
			})
		}

		providerNames := make([]string, 0, len(providerAggs))
		for name := range providerAggs {
			providerNames = append(providerNames, name)
		}
		sort.Slice(providerNames, func(i, j int) bool {
			a, b := providerAggs[providerNames[i]], providerAggs[providerNames[j]]
			if a.count != b.count {
				return a.count > b.count
			}
			return providerNames[i] < providerNames[j]
		})
		if len(providerNames) > 20 {
			providerNames = providerNames[:20]
		}
		for _, name := range providerNames {
			agg := providerAggs[name]
			breakdown := providerErrors[name]
			if breakdown == nil {
				breakdown = make(map[string]int)
			}
			row := analyticsProviderRow{
				Provider:       name,
				Count:          agg.count,
				SuccessCount:   agg.success,
				FailureCount:   agg.failure,
				SuccessRate:    agg.successRate(),
				P50LatencyMs:   roundedPtr(agg.p50(), 0),
				P95LatencyMs:   roundedPtr(agg.p95(), 0),
				ErrorBreakdown: breakdown,
			}
			if v := agg.inTotal(); v != nil {
				row.TotalInputTokens = *v
			}
			if v := agg.outTotal(); v != nil {
				row.TotalOutputTokens = *v
			}
			resp.ByProvider = append(resp.ByProvider, row)
		}

		for cat, count := range errorDist {
			resp.ErrorDistribution = append(resp.ErrorDistribution, analyticsErrorRow{ErrorCategory: cat, Count: count})
		}
		sort.Slice(resp.ErrorDistribution, func(i, j int) bool {
			if resp.ErrorDistribution[i].Count != resp.ErrorDistribution[j].Count {
				return resp.ErrorDistribution[i].Count > resp.ErrorDistribution[j].Count
			}
			return resp.ErrorDistribution[i].ErrorCategory < resp.ErrorDistribution[j].ErrorCategory
		})

		bucketStarts := make([]time.Time, 0, len(bucketAggs))
		for start := range bucketAggs {
			bucketStarts = append(bucketStarts, start)
		}
		sort.Slice(bucketStarts, func(i, j int) bool { return bucketStarts[i].Before(bucketStarts[j]) })
		for _, start := range bucketStarts {
			agg := bucketAggs[start]
			resp.Timeline = append(resp.Timeline, analyticsTimelineRow{
				Bucket:            start.UTC().Format("2006-01-02T15:04:05.000Z"),
				Count:             agg.count,
				SuccessCount:      agg.success,
				FailureCount:      agg.failure,
				P50LatencyMs:      agg.p50(),
				LatencySamples:    len(agg.latencies),
				P95LatencyMs:      agg.p95(),
				TotalInputTokens:  agg.inTotal(),
				TotalOutputTokens: agg.outTotal(),
			})
		}

		handlerutil.WriteJSON(w, http.StatusOK, resp)
	}
}
