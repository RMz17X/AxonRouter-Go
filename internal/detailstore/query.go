package detailstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"axonrouter/proxy/internal/db"
)

// ErrNotFound marks a ByID miss — the record was never written or has
// aged out of retention — so handlers can answer 404 rather than 500.
var ErrNotFound = errors.New("detailstore: request detail not found")

// project parses one record's payload once and keeps every field the
// read paths used to extract in SQL. The extractions mirror the old
// jsonb expressions exactly — same defaults, same NULLIF chain on the
// proxy, same truncating integer forms — so a page served from the
// index is indistinguishable from one served from the table.
func project(rec db.RequestDetailRecord, file int, offset, length int64) *light {
	lt := &light{
		id: rec.ID, timestamp: rec.Timestamp, provider: rec.Provider,
		model: rec.Model, connectionID: rec.ConnectionID, status: rec.Status,
		reqKey: rec.ID, tokensJSON: "{}", proxy: "direct", isStream: true,
		file: file, offset: offset, length: length,
	}
	var pv payloadView
	if rec.Data == "" || json.Unmarshal([]byte(rec.Data), &pv) != nil {
		return lt
	}
	if pv.ReqID != "" {
		lt.reqKey = pv.ReqID
	}
	if len(pv.Latency) > 0 {
		var lat struct {
			TTFT  *float64 `json:"ttft"`
			Total *float64 `json:"total"`
		}
		if json.Unmarshal(pv.Latency, &lat) == nil {
			if lat.TTFT != nil {
				lt.latencyTTFT = int64(*lat.TTFT)
			}
			if lat.Total != nil {
				lt.latencyTotal = int64(*lat.Total)
				lt.latencyTotalRaw = *lat.Total
				lt.hasLatency = true
			}
		}
	}
	if len(pv.Tokens) > 0 {
		lt.tokensJSON = string(pv.Tokens)
		var tok struct {
			Prompt     *float64 `json:"prompt_tokens"`
			Completion *float64 `json:"completion_tokens"`
			Output     *float64 `json:"output_tokens"`
		}
		if json.Unmarshal(pv.Tokens, &tok) == nil {
			if tok.Prompt != nil {
				lt.promptTokens = int64(*tok.Prompt)
				lt.hasPromptTokens = true
			}
			if tok.Completion != nil {
				lt.completionTokens = int64(*tok.Completion)
				lt.hasCompletionTokens = true
			}
			if tok.Output != nil {
				lt.outputTokens = int64(*tok.Output)
			}
		}
	}
	lt.account = pv.Account
	if pv.Cost != nil {
		lt.cost = *pv.Cost
	}
	lt.errorCategory, lt.sampleError, lt.sampleStatus = sampleFields(rec.Data, lt.status)
	lt.combo = pv.Combo
	lt.protocol = pv.Protocol
	switch {
	case pv.LoopWarning != nil:
		lt.loopWarning = *pv.LoopWarning
	case len(pv.Anomalies) > 0 && pv.Anomalies[0].Message != nil:
		lt.loopWarning = *pv.Anomalies[0].Message
	}
	lt.proxy = proxyOf(pv)
	if pv.IsStream != nil {
		lt.isStream = *pv.IsStream
	}
	lt.apiKeyName = pv.APIKeyName
	lt.clientAPIKey = pv.ClientAPIKey
	lt.clientIP = pv.ClientIP
	if len(pv.SmartDecision) > 0 {
		lt.smartDecisionJSON = string(pv.SmartDecision)
	}
	return lt
}

type payloadView struct {
	ReqID       string          `json:"reqId"`
	Latency     json.RawMessage `json:"latency"`
	Tokens      json.RawMessage `json:"tokens"`
	Account     string          `json:"account"`
	Cost        *float64        `json:"cost"`
	Combo       string          `json:"combo"`
	Protocol    string          `json:"protocol"`
	LoopWarning *string         `json:"loopWarning"`
	Anomalies   []struct {
		Message *string `json:"message"`
	} `json:"anomalies"`
	Proxy         string          `json:"proxy"`
	Egress        json.RawMessage `json:"egress"`
	IsStream      *bool           `json:"isStream"`
	APIKeyName    string          `json:"apiKeyName"`
	ClientAPIKey  string          `json:"clientApiKey"`
	ClientIP      string          `json:"clientIp"`
	SmartDecision json.RawMessage `json:"smartDecision"`
}

// proxyOf walks the old read's fallback chain: the recorded proxy,
// then the egress string, then the egress object's pool identifiers,
// then its target, then "direct".
func proxyOf(pv payloadView) string {
	if pv.Proxy != "" {
		return pv.Proxy
	}
	if len(pv.Egress) > 0 {
		var asString string
		if json.Unmarshal(pv.Egress, &asString) == nil {
			if asString != "" {
				return asString
			}
		} else {
			var eg struct {
				PoolName string `json:"poolName"`
				PoolID   string `json:"poolId"`
				PoolIDUp string `json:"PoolID"`
				Target   string `json:"target"`
			}
			if json.Unmarshal(pv.Egress, &eg) == nil {
				for _, v := range []string{eg.PoolName, eg.PoolID, eg.PoolIDUp, eg.Target} {
					if v != "" {
						return v
					}
				}
			}
		}
	}
	return "direct"
}

// snapshot returns the retained records in arrival order.
func (s *store) snapshot() []*light {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*light, len(s.records))
	copy(out, s.records)
	return out
}

func inWindow(ts, since string) bool {
	return since == "" || ts >= since
}

// representative picks, per request key, the row the grouped reads
// speak for: the success row when one exists, else the latest.
func representatives(recs []*light) map[string]*light {
	better := func(a, b *light) bool {
		as, bs := a.status == "success", b.status == "success"
		if as != bs {
			return as
		}
		if a.timestamp != b.timestamp {
			return a.timestamp > b.timestamp
		}
		return a.id > b.id
	}
	out := make(map[string]*light)
	for _, rec := range recs {
		if cur, ok := out[rec.reqKey]; !ok || better(rec, cur) {
			out[rec.reqKey] = rec
		}
	}
	return out
}

func (lt *light) listRow() db.RequestDetailListRow {
	return db.RequestDetailListRow{
		ID: lt.id, Timestamp: lt.timestamp, Provider: lt.provider,
		Model: lt.model, ConnectionID: lt.connectionID, Status: lt.status,
		LatencyTTFT: lt.latencyTTFT, LatencyTotal: lt.latencyTotal,
		TokensJSON: lt.tokensJSON, Account: lt.account, Cost: lt.cost,
		Combo: lt.combo, Protocol: lt.protocol, LoopWarning: lt.loopWarning,
		Proxy: lt.proxy, IsStream: lt.isStream, APIKeyName: lt.apiKeyName,
		ClientAPIKey: lt.clientAPIKey, ClientIP: lt.clientIP,
		SmartDecisionJSON: lt.smartDecisionJSON,
	}
}

// Page returns one page of request-collapsed detail rows and the
// total request count, newest first — the file-store form of the old
// GetRequestDetailsPaged, with the same since floor and the same
// representative rule.
func Page(limit, offset int, since string) ([]db.RequestDetailListRow, int, error) {
	s, err := ready()
	if err != nil {
		return nil, 0, err
	}
	recs := s.snapshot()
	windowed := recs[:0]
	for _, rec := range recs {
		if inWindow(rec.timestamp, since) {
			windowed = append(windowed, rec)
		}
	}
	reps := representatives(windowed)
	rows := make([]*light, 0, len(reps))
	for _, rec := range reps {
		rows = append(rows, rec)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].timestamp != rows[j].timestamp {
			return rows[i].timestamp > rows[j].timestamp
		}
		return rows[i].id > rows[j].id
	})
	total := len(rows)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	out := make([]db.RequestDetailListRow, 0, end-offset)
	for _, rec := range rows[offset:end] {
		out = append(out, rec.listRow())
	}
	return out, total, nil
}

// readPayload fetches one record's stored payload from its file
// offset.
func (s *store) readPayload(lt *light) (string, error) {
	f, err := os.Open(filePath(s.dir, lt.file))
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, lt.length)
	if _, err := f.ReadAt(buf, lt.offset); err != nil {
		return "", err
	}
	var env envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return "", err
	}
	return env.Data, nil
}

// ByID returns the full stored payload for one record — the
// inspector's read — fetched from the record's file offset.
func ByID(id string) (string, error) {
	s, err := ready()
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("get request detail: empty id")
	}
	s.mu.RLock()
	lt, ok := s.byID[id]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("get request detail %s: %w", id, ErrNotFound)
	}
	data, err := s.readPayload(lt)
	if err != nil {
		return "", fmt.Errorf("get request detail %s: %w", id, err)
	}
	return data, nil
}

// ErrorCountSince counts requests that failed in the window: a
// request counts when any of its rows errored and none succeeded.
func ErrorCountSince(since string) (int, error) {
	s, err := ready()
	if err != nil {
		return 0, err
	}
	type agg struct{ hasError, hasSuccess bool }
	groups := make(map[string]*agg)
	for _, rec := range s.snapshot() {
		if !inWindow(rec.timestamp, since) {
			continue
		}
		if rec.status != "error" && rec.status != "success" {
			continue
		}
		g := groups[rec.reqKey]
		if g == nil {
			g = &agg{}
			groups[rec.reqKey] = g
		}
		if rec.status == "error" {
			g.hasError = true
		} else {
			g.hasSuccess = true
		}
	}
	count := 0
	for _, g := range groups {
		if g.hasError && !g.hasSuccess {
			count++
		}
	}
	return count, nil
}

// ErrorCountByProviderSince counts, per provider, the requests that
// failed on that provider in the window and never succeeded anywhere.
func ErrorCountByProviderSince(since string) (map[string]int, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	succeeded := make(map[string]bool)
	type pair struct{ provider, key string }
	failed := make(map[pair]bool)
	for _, rec := range s.snapshot() {
		if !inWindow(rec.timestamp, since) {
			continue
		}
		switch rec.status {
		case "success":
			succeeded[rec.reqKey] = true
		case "error":
			failed[pair{rec.provider, rec.reqKey}] = true
		}
	}
	out := make(map[string]int)
	for p := range failed {
		if !succeeded[p.key] {
			out[p.provider]++
		}
	}
	return out, nil
}

// ComboAnalyticsSince aggregates representative rows per request by
// (combo, model, provider) over the window, as the old SQL read did.
func ComboAnalyticsSince(since string) ([]db.ComboAnalyticsRow, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	recs := s.snapshot()
	windowed := recs[:0]
	for _, rec := range recs {
		if rec.combo != "" && inWindow(rec.timestamp, since) {
			windowed = append(windowed, rec)
		}
	}
	type key struct{ combo, model, provider string }
	type agg struct {
		total, success int
		latencySum     float64
		lastSeen       string
	}
	groups := make(map[key]*agg)
	for _, rec := range representatives(windowed) {
		k := key{rec.combo, rec.model, rec.provider}
		g := groups[k]
		if g == nil {
			g = &agg{}
			groups[k] = g
		}
		g.total++
		if rec.status == "success" {
			g.success++
		}
		g.latencySum += rec.latencyTotalRaw
		if rec.timestamp > g.lastSeen {
			g.lastSeen = rec.timestamp
		}
	}
	out := make([]db.ComboAnalyticsRow, 0, len(groups))
	for k, g := range groups {
		row := db.ComboAnalyticsRow{
			ComboName: k.combo, Model: k.model, Provider: k.provider,
			Total: g.total, Success: g.success, LastSeen: g.lastSeen,
		}
		if g.total > 0 {
			row.AvgLatencyS = (g.latencySum / float64(g.total)) / 1000.0
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ComboName != out[j].ComboName {
			return out[i].ComboName < out[j].ComboName
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}

// Perf returns the latency observation of every retained record — the
// node-health table's source, as the old ring read was.
func Perf() ([]db.RequestDetailPerfRow, error) {
	s, err := ready()
	if err != nil {
		return nil, err
	}
	recs := s.snapshot()
	out := make([]db.RequestDetailPerfRow, 0, len(recs))
	for _, rec := range recs {
		out = append(out, db.RequestDetailPerfRow{
			Provider: rec.provider, Model: rec.model,
			TTFTMs: rec.latencyTTFT, TotalMs: rec.latencyTotal,
			CompletionTokens: rec.completionTokens, OutputTokens: rec.outputTokens,
		})
	}
	return out, nil
}

// ModelLatency folds Perf into one mean-latency row per
// provider/model pair — the same fold the SQL-era read ran, kept
// beside the read it summarises so the two cannot drift.
func ModelLatency() ([]db.ModelLatencyAgg, error) {
	rows, err := Perf()
	if err != nil {
		return nil, err
	}
	type acc struct {
		total, count    int64
		ttft, ttftCount int64
	}
	byKey := make(map[string]*acc, len(rows))
	keyOf := func(provider, model string) string { return provider + "|" + model }
	for _, row := range rows {
		if row.TotalMs <= 0 {
			continue
		}
		k := keyOf(row.Provider, row.Model)
		a := byKey[k]
		if a == nil {
			a = &acc{}
			byKey[k] = a
		}
		a.total += row.TotalMs
		a.count++
		if row.TTFTMs > 0 {
			a.ttft += row.TTFTMs
			a.ttftCount++
		}
	}
	out := make([]db.ModelLatencyAgg, 0, len(byKey))
	for k, a := range byKey {
		provider, model := k, ""
		for i := range len(k) {
			if k[i] == '|' {
				provider, model = k[:i], k[i+1:]
				break
			}
		}
		agg := db.ModelLatencyAgg{Provider: provider, Model: model, Samples: a.count}
		if a.count > 0 {
			agg.TotalMs = a.total / a.count
		}
		if a.ttftCount > 0 {
			agg.TTFTMs = a.ttft / a.ttftCount
		}
		out = append(out, agg)
	}
	return out, nil
}
