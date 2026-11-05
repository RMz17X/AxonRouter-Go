package chat

import (
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// usage_daily.go is the daily aggregate's accumulation path (review
// cycle 1, F2). The aggregate used to ride the usage-write queue as a
// per-request job, and it was the queue's slowest job by far: a full
// read-modify-write of the day blob (~21 ms once the blob passes
// 400 KB) under a global mutex, once per request. At burst scale it
// set the drain rate for the whole queue, ledger inserts included.
//
// The aggregate is a fold OVER the ledger, which is the actual truth,
// so it does not need per-request persistence: request completions now
// append a delta to an in-memory list, and a flusher folds the whole
// list into the day blob as ONE read-modify-write per interval (and
// one per FlushUsageWrites call, so shutdown and the integration
// harness never wait out the ticker). A crash loses at most one
// interval of aggregate — the ledger rows it summarises are already
// durable. The fold itself is failure-atomic per date (council round
// 1, C-1): a fold whose base read fails is aborted and its deltas stay
// queued for the next flush, so a transient read error can never fold
// onto an empty base and replace the day's committed totals with one
// interval's worth — the failure mode this guard exists to prevent.

// dailyFlushInterval is the aggregate's freshness bound: the dashboard
// daily totals trail live traffic by at most this much.
const dailyFlushInterval = 3 * time.Second

// dailyUsageDelta is one request's contribution to the daily
// aggregate. failed selects the error fold (requests/errors only, no
// tokens) over the success fold.
type dailyUsageDelta struct {
	dateKey      string
	provider     string
	model        string
	endpoint     string
	connectionID string
	apiKey       string
	promptTokens int
	completion   int
	cachedTokens int
	cost         float64
	failed       bool
}

var dailyAccum struct {
	mu      sync.Mutex
	repo    *db.Repo
	pending []dailyUsageDelta
	started bool
}

// accumulateDailyUsage records one request's delta. In the harness's
// synchronous mode it lands immediately, exactly as the old queued job
// did once dequeued; in production it only appends to the pending list.
func accumulateDailyUsage(repo *db.Repo, d dailyUsageDelta) {
	if repo == nil {
		return
	}
	if d.dateKey == "" {
		d.dateKey = time.Now().UTC().Format("2006-01-02")
	}
	if usageSynchronous.Load() {
		applyDailyDeltas(repo, []dailyUsageDelta{d})
		return
	}
	dailyAccum.mu.Lock()
	dailyAccum.repo = repo
	dailyAccum.pending = append(dailyAccum.pending, d)
	if !dailyAccum.started {
		dailyAccum.started = true
		go dailyFlushLoop()
	}
	dailyAccum.mu.Unlock()
}

func dailyFlushLoop() {
	ticker := time.NewTicker(dailyFlushInterval)
	defer ticker.Stop()
	for range ticker.C {
		flushDailyUsage()
	}
}

// dailyPendingCap bounds the retained backlog when folds keep
// failing (a database outage): deltas are small structs, so the cap
// holds minutes of full-rate traffic, and crossing it drops the
// oldest with an Error log rather than growing without bound. The
// ledger is unaffected either way — only the derived aggregate lags.
const dailyPendingCap = 100_000

// flushDailyUsage folds every pending delta into the day blobs. It is
// called by the flush ticker, by FlushUsageWrites, and (indirectly, one
// delta at a time) by the synchronous path. Deltas whose fold did not
// land go back on the pending list for the next flush — swapping the
// list out before the fold and dropping it on failure was the other
// half of council round 1's C-1/F-4 loss path.
func flushDailyUsage() {
	dailyAccum.mu.Lock()
	pending := dailyAccum.pending
	repo := dailyAccum.repo
	dailyAccum.pending = nil
	dailyAccum.mu.Unlock()
	if len(pending) == 0 || repo == nil {
		return
	}
	unlanded := applyDailyDeltas(repo, pending)
	if len(unlanded) == 0 {
		return
	}
	dailyAccum.mu.Lock()
	dailyAccum.pending = append(unlanded, dailyAccum.pending...)
	over := len(dailyAccum.pending) - dailyPendingCap
	if over > 0 {
		dailyAccum.pending = dailyAccum.pending[over:]
	}
	dailyAccum.mu.Unlock()
	if over > 0 {
		log.Error("usage", "daily usage backlog over cap; oldest deltas dropped", "dropped", over)
	}
}

// applyDailyDeltas performs one read-modify-write per date present in
// deltas, under the same global mutex the one-shot upserts take, so a
// batch fold and a direct upsertDailyUsage call can never interleave
// into a lost update. It returns the deltas that did NOT land — a date
// whose base read failed (any error other than sql.ErrNoRows, which is
// the legitimate empty base), whose blob would not marshal, or whose
// upsert failed — so the caller can retain them for the next flush.
// A blob whose base read failed is never upserted: the upsert is a
// full-row replace, so folding onto a phantom empty base would
// destroy the day's committed totals (council round 1, C-1).
func applyDailyDeltas(repo *db.Repo, deltas []dailyUsageDelta) (unlanded []dailyUsageDelta) {
	dailyUsageMu.Lock()
	defer dailyUsageMu.Unlock()

	byDate := make(map[string][]dailyUsageDelta)
	var order []string
	for _, d := range deltas {
		if _, ok := byDate[d.dateKey]; !ok {
			order = append(order, d.dateKey)
		}
		byDate[d.dateKey] = append(byDate[d.dateKey], d)
	}
	for _, dateKey := range order {
		existing, err := repo.GetUsageDaily(dateKey)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Error("usage", "read daily usage failed; fold aborted, deltas retained", "date", dateKey, "error", err)
			unlanded = append(unlanded, byDate[dateKey]...)
			continue
		}
		data := parseDailyData(existing)
		for _, d := range byDate[dateKey] {
			if d.failed {
				mergeDailyUsageError(data, d.provider, d.model)
			} else {
				mergeDailyUsage(data, d.provider, d.model, d.endpoint, d.connectionID, d.apiKey, d.promptTokens, d.completion, d.cachedTokens, d.cost)
			}
		}
		dataJSON, err := json.Marshal(data)
		if err != nil {
			log.Error("usage", "marshal daily usage failed; deltas retained", "date", dateKey, "error", err)
			unlanded = append(unlanded, byDate[dateKey]...)
			continue
		}
		if err := repo.UpsertUsageDaily(dateKey, string(dataJSON)); err != nil {
			log.Error("usage", "upsert daily usage failed; deltas retained", "date", dateKey, "error", err)
			unlanded = append(unlanded, byDate[dateKey]...)
		}
	}
	return unlanded
}
