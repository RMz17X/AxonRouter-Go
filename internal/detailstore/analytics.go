package detailstore

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"

	"axonrouter/proxy/internal/db"
)

// textAt walks a decoded payload along path and renders the value the
// way the old jsonb text extraction did: a string as itself, a number
// as its digits, a boolean as true/false, an object or array as its
// JSON text. A missing path (or a non-object along the way) reports
// not-found.
func textAt(root map[string]any, path ...string) (string, bool) {
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = m[key]
		if !ok || cur == nil {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

func firstText(root map[string]any, paths ...[]string) string {
	for _, path := range paths {
		if v, ok := textAt(root, path...); ok && v != "" {
			return v
		}
	}
	return ""
}

// sampleFields extracts the failure-sample chain values and the error
// category from one payload, mirroring the SQL COALESCE chains the
// analytics reads ran over the table: the payload's own error forms
// first, then the row's status column, then the literal defaults.
func sampleFields(data, status string) (category, sampleError, sampleStatus string) {
	var root map[string]any
	if data == "" || json.Unmarshal([]byte(data), &root) != nil {
		root = map[string]any{}
	}
	category = firstText(root, []string{"error_category"}, []string{"errorCategory"})
	sampleError = firstText(root,
		[]string{"error"},
		[]string{"response", "error"},
		[]string{"response", "message"},
		[]string{"providerResponse", "error"},
		[]string{"response", "error", "message"})
	if sampleError == "" {
		sampleError = status
	}
	if sampleError == "" {
		sampleError = "Unknown failure"
	}
	sampleStatus = firstText(root,
		[]string{"response", "status"},
		[]string{"providerResponse", "status"},
		[]string{"errorCode"},
		[]string{"status"})
	if sampleStatus == "" {
		sampleStatus = status
	}
	if sampleStatus == "" {
		sampleStatus = "500"
	}
	return category, sampleError, sampleStatus
}

// AnalyticsEvents returns the retained records inside the window
// (optionally narrowed to one provider and model), oldest first —
// the file store's form of the old GetAnalyticsEvents.
func AnalyticsEvents(since, until, provider, model string) ([]db.AnalyticsEventRow, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	recs := s.snapshot()
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].timestamp < recs[j].timestamp })
	out := make([]db.AnalyticsEventRow, 0)
	for _, lt := range recs {
		if lt.timestamp < since || lt.timestamp > until {
			continue
		}
		if provider != "" && lt.provider != provider {
			continue
		}
		if model != "" && lt.model != model {
			continue
		}
		out = append(out, db.AnalyticsEventRow{
			Timestamp: lt.timestamp, Provider: lt.provider, Model: lt.model,
			Success:      lt.status == "success",
			LatencyMs:    sql.NullFloat64{Float64: lt.latencyTotalRaw, Valid: lt.hasLatency},
			InputTokens:  sql.NullInt64{Int64: lt.promptTokens, Valid: lt.hasPromptTokens},
			OutputTokens: sql.NullInt64{Int64: lt.completionTokens, Valid: lt.hasCompletionTokens},
			ErrorText:    lt.sampleError, ErrorStatus: lt.sampleStatus,
			ErrorCategory: lt.errorCategory,
		})
	}
	return out, nil
}

func failureMatch(lt *light, f db.FailureFilter) bool {
	if lt.status == "success" {
		return false
	}
	if f.Provider != "" && lt.provider != f.Provider {
		return false
	}
	if f.Model != "" && lt.model != f.Model {
		return false
	}
	if f.From != "" && lt.timestamp < f.From {
		return false
	}
	if f.To != "" && lt.timestamp > f.To {
		return false
	}
	return true
}

// failureGroups folds the failed records under the filter into
// leaderboard rows: a failure count and the newest failure's sample
// error/status per group, most failures first, capped at 25 — the
// old queryFailureGroups semantics.
func failureGroups(f db.FailureFilter, byModel bool) ([]db.FailureGroupRow, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	type group struct {
		provider, model string
		count           int
		latest          *light
	}
	groups := make(map[string]*group)
	for _, lt := range s.snapshot() {
		if !failureMatch(lt, f) {
			continue
		}
		key := lt.provider
		model := ""
		if byModel {
			key += "\x00" + lt.model
			model = lt.model
		}
		g := groups[key]
		if g == nil {
			g = &group{provider: lt.provider, model: model}
			groups[key] = g
		}
		g.count++
		if g.latest == nil || lt.timestamp > g.latest.timestamp ||
			(lt.timestamp == g.latest.timestamp && lt.id > g.latest.id) {
			g.latest = lt
		}
	}
	out := make([]db.FailureGroupRow, 0, len(groups))
	for _, g := range groups {
		out = append(out, db.FailureGroupRow{
			Provider: g.provider, Model: g.model, FailureCount: g.count,
			LatestFailedAt: g.latest.timestamp, SampleError: g.latest.sampleError,
			SampleStatusCode: g.latest.sampleStatus,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FailureCount != out[j].FailureCount {
			return out[i].FailureCount > out[j].FailureCount
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	if len(out) > 25 {
		out = out[:25]
	}
	return out, nil
}

// FailureTopModels returns the 25 (provider, model) pairs with the
// most failed requests under the filter.
func FailureTopModels(f db.FailureFilter) ([]db.FailureGroupRow, error) {
	return failureGroups(f, true)
}

// FailureTopProviders returns the 25 providers with the most failed
// requests under the filter. Model is empty on these rows.
func FailureTopProviders(f db.FailureFilter) ([]db.FailureGroupRow, error) {
	return failureGroups(f, false)
}

// RecentFailurePayloads returns the stored payloads of the newest
// failed records under the filter, newest first.
func RecentFailurePayloads(f db.FailureFilter, limit int) ([]string, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	var failed []*light
	for _, lt := range s.snapshot() {
		if failureMatch(lt, f) {
			failed = append(failed, lt)
		}
	}
	sort.Slice(failed, func(i, j int) bool {
		if failed[i].timestamp != failed[j].timestamp {
			return failed[i].timestamp > failed[j].timestamp
		}
		return failed[i].id > failed[j].id
	})
	if limit >= 0 && len(failed) > limit {
		failed = failed[:limit]
	}
	out := make([]string, 0, len(failed))
	for _, lt := range failed {
		data, err := s.readPayload(lt)
		if err != nil {
			return nil, err
		}
		out = append(out, data)
	}
	return out, nil
}
