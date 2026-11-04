package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/handlerutil"
)

// Pool health-check + auto-recovery routes — port of AxonRouter's
// src/app/api/proxy-pools/health-check/route.js (backed by
// src/lib/network/proxyHealthSweep.js) and recover/route.js (backed by
// src/lib/network/proxyAutoRecovery.js). The sweep records verdicts in
// each pool's testStatus and deliberately deactivates nothing; recovery
// re-probes unhealthy/degraded/dead pools and revives the ones that
// answer.

const (
	sweepDefaultBatch       = 200
	sweepDefaultConcurrency = 12
	sweepDefaultStaleMs     = 30 * 60 * 1000
	sweepTimeout            = 8 * time.Second
	recoveryCooldownMs      = 30 * 60 * 1000
	recoveryDefaultLimit    = 64
	recoveryConcurrency     = 8
	recoveryTimeout         = 15 * time.Second
)

type sweepSummary struct {
	Scanned int
	Active  int
	Failed  int
	Written int
	Skipped bool
	Reason  string
	Error   string
}

var (
	sweepMu           sync.Mutex
	sweepRunning      bool
	lastSweepSummary  *sweepSummary
	lastHealthCheckAt string
)

// shortError truncates a probe error for operator-facing storage
// (upstream shortError: whitespace-collapsed, capped at 180 chars).
func shortError(err string) string {
	s := strings.Join(strings.Fields(err), " ")
	if s == "" {
		return "probe failed"
	}
	if len(s) > 180 {
		return s[:177] + "..."
	}
	return s
}

func parsePoolTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// sweepNeedsProbe reports whether a pool is due a probe: never tested, or
// last tested at/before the staleness cutoff.
func sweepNeedsProbe(pool map[string]any, staleBefore time.Time) bool {
	status := handlerutil.GetString(pool, "testStatus")
	if status == "" || status == "unknown" {
		return true
	}
	at, ok := parsePoolTime(handlerutil.GetString(pool, "lastTestedAt"))
	if !ok {
		return true
	}
	return !at.After(staleBefore)
}

// sweepProxyPoolHealth runs one sweep pass (upstream sweepProxyPoolHealth;
// the cross-process cache lock collapses to the in-process guard here).
func (h *DashboardHandler) sweepProxyPoolHealth(ctx context.Context, staleMs int64, batch int) sweepSummary {
	sweepMu.Lock()
	if sweepRunning {
		sweepMu.Unlock()
		return sweepSummary{Skipped: true}
	}
	sweepRunning = true
	sweepMu.Unlock()

	summary := sweepSummary{}
	defer func() {
		sweepMu.Lock()
		cp := summary
		lastSweepSummary = &cp
		sweepRunning = false
		sweepMu.Unlock()
	}()

	if batch <= 0 {
		batch = sweepDefaultBatch
	}
	stale := staleMs
	if stale <= 0 {
		stale = sweepDefaultStaleMs
	}
	if stale < 1000 {
		stale = 1000
	}
	staleBefore := time.Now().Add(-time.Duration(stale) * time.Millisecond)

	pools, err := h.Repo.ListProxyPools()
	if err != nil {
		summary.Error = shortError(err.Error())
		return summary
	}
	var queue []map[string]any
	for _, p := range pools {
		active, _ := p["isActive"].(bool)
		if !active || poolPrimaryURLValue(p) == "" {
			continue
		}
		if sweepNeedsProbe(p, staleBefore) {
			queue = append(queue, p)
		}
	}
	stride := len(queue) / batch
	if stride < 1 {
		stride = 1
	}
	var selected []map[string]any
	for i := 0; i < len(queue) && len(selected) < batch; i += stride {
		selected = append(selected, queue[i])
	}
	summary.Scanned = len(selected)
	if len(selected) == 0 {
		return summary
	}

	var mu sync.Mutex
	var cursor int
	var wg sync.WaitGroup
	workers := sweepDefaultConcurrency
	if workers > len(selected) {
		workers = len(selected)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				idx := cursor
				cursor++
				mu.Unlock()
				if idx >= len(selected) {
					return
				}
				pool := selected[idx]
				res := testProxyPoolEntry(ctx, pool, sweepTimeout)
				verdictStatus := "failed"
				verdictError := ""
				if res.OK {
					verdictStatus = "active"
				} else {
					verdictError = res.Error
					if verdictError == "" {
						verdictError = fmt.Sprintf("probe status %d", res.Status)
					}
					verdictError = shortError(verdictError)
				}
				mu.Lock()
				if res.OK {
					summary.Active++
				} else {
					summary.Failed++
				}
				mu.Unlock()
				updates := map[string]any{
					"testStatus":   verdictStatus,
					"lastTestedAt": time.Now().UTC().Format(time.RFC3339),
				}
				if verdictError != "" {
					updates["lastError"] = verdictError
				}
				if err := h.Repo.UpdateProxyPool(handlerutil.GetString(pool, "id"), updates); err != nil {
					mu.Lock()
					summary.Error = shortError(err.Error())
					mu.Unlock()
				} else {
					mu.Lock()
					summary.Written++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return summary
}

// HandleGetProxyPoolsHealthCheck handles GET /api/proxy-pools/health-check.
func (h *DashboardHandler) HandleGetProxyPoolsHealthCheck(w http.ResponseWriter, r *http.Request) {
	sweepMu.Lock()
	summary := lastSweepSummary
	checkedAt := lastHealthCheckAt
	sweepMu.Unlock()
	var summaryOut any
	if summary != nil {
		summaryOut = map[string]any{
			"scanned": summary.Scanned,
			"active":  summary.Active,
			"failed":  summary.Failed,
			"written": summary.Written,
		}
	}
	var checkedOut any
	if checkedAt != "" {
		checkedOut = checkedAt
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"lastHealthCheckAt": checkedOut,
		"lastSummary":       summaryOut,
	})
}

// HandleProxyPoolsHealthCheck handles POST /api/proxy-pools/health-check:
// an immediate sweep over the whole active set.
func (h *DashboardHandler) HandleProxyPoolsHealthCheck(w http.ResponseWriter, r *http.Request) {
	summary := h.sweepProxyPoolHealth(r.Context(), 0, 10000)
	sweepMu.Lock()
	checkedAt := lastHealthCheckAt
	sweepMu.Unlock()
	if summary.Skipped {
		reason := summary.Reason
		if reason == "" {
			reason = "Health check already in progress"
		}
		var checkedOut any
		if checkedAt != "" {
			checkedOut = checkedAt
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"skipped":   true,
			"reason":    reason,
			"checkedAt": checkedOut,
		})
		return
	}
	nowISO := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sweepMu.Lock()
	lastHealthCheckAt = nowISO
	sweepMu.Unlock()
	var reason any
	if summary.Error != "" {
		reason = summary.Error
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"checkedAt": nowISO,
		"results":   []any{},
		"summary": map[string]any{
			"scanned": summary.Scanned,
			"active":  summary.Active,
			"failed":  summary.Failed,
			"written": summary.Written,
		},
		"skipped": false,
		"reason":  reason,
	})
}

// HandleRecoverProxyPools handles POST /api/proxy-pools/recover.
func (h *DashboardHandler) HandleRecoverProxyPools(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	force := query.Get("force") == "true"
	includeDead := query.Get("includeDead") == "true" || force
	_ = includeDead // upstream accepts it; dead pools are always probed
	var minCooldown int64 = recoveryCooldownMs
	if force {
		minCooldown = 0
	}
	result := h.autoRecoverUnhealthyProxyPools(r.Context(), minCooldown, recoveryDefaultLimit)
	out := map[string]any{
		"success":      true,
		"checked":      result.Checked,
		"recovered":    result.Recovered,
		"stillFailing": result.StillFailing,
		"markedDead":   result.MarkedDead,
		"results":      result.Results,
	}
	if result.Error != "" {
		out["error"] = result.Error
	}
	handlerutil.WriteJSON(w, http.StatusOK, out)
}

type recoveryResult struct {
	Checked      int
	Recovered    int
	StillFailing int
	MarkedDead   int
	Error        string
	Results      []any
}

// autoRecoverUnhealthyProxyPools ports upstream's recovery pass: probe
// every pool that is not currently healthy (oldest-tested first, per-pool
// cooldown respected) and apply computeProxyTestHealth to the verdict.
func (h *DashboardHandler) autoRecoverUnhealthyProxyPools(ctx context.Context, minCooldownMs int64, limit int) recoveryResult {
	result := recoveryResult{Results: []any{}}
	pools, err := h.Repo.ListProxyPools()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(pools) == 0 {
		return result
	}
	now := time.Now()
	var candidates []map[string]any
	for _, p := range pools {
		failures := 0
		if v, ok := p["consecutiveFailures"].(float64); ok {
			failures = int(v)
		}
		status := handlerutil.GetString(p, "testStatus")
		isActive, _ := p["isActive"].(bool)
		isDead := status == "dead" || failures >= proxyDeadThreshold
		isUnhealthy := !isActive || status == "unhealthy"
		isDegraded := status == "degraded" || failures > 0
		if !isUnhealthy && !isDegraded && !isDead {
			continue
		}
		if at, ok := parsePoolTime(handlerutil.GetString(p, "lastTestedAt")); ok {
			if now.Sub(at) < time.Duration(minCooldownMs)*time.Millisecond {
				continue
			}
		}
		candidates = append(candidates, p)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		at, _ := parsePoolTime(handlerutil.GetString(candidates[i], "lastTestedAt"))
		bt, _ := parsePoolTime(handlerutil.GetString(candidates[j], "lastTestedAt"))
		return at.Before(bt)
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	if len(candidates) == 0 {
		return result
	}
	result.Checked = len(candidates)

	// Indexed slots keep the results in candidate order, like upstream's
	// Promise.allSettled collection.
	slots := make([]any, len(candidates))
	var mu sync.Mutex
	for i := 0; i < len(candidates); i += recoveryConcurrency {
		end := i + recoveryConcurrency
		if end > len(candidates) {
			end = len(candidates)
		}
		var wg sync.WaitGroup
		for j := i; j < end; j++ {
			wg.Add(1)
			go func(idx int, pool map[string]any) {
				defer wg.Done()
				id := handlerutil.GetString(pool, "id")
				name := handlerutil.GetString(pool, "name")
				res := testProxyPoolEntry(ctx, pool, recoveryTimeout)
				health := computeProxyTestHealth(pool, res)
				if err := h.Repo.UpdateProxyPool(id, health); err != nil {
					mu.Lock()
					result.StillFailing++
					mu.Unlock()
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if res.OK {
					result.Recovered++
					slots[idx] = map[string]any{
						"id": id, "name": name, "recovered": true, "status": "active",
					}
					return
				}
				result.StillFailing++
				markedDead := health["testStatus"] == "dead"
				if markedDead {
					result.MarkedDead++
				}
				entry := map[string]any{
					"id":                  id,
					"name":                name,
					"recovered":           false,
					"markedDead":          markedDead,
					"status":              health["testStatus"],
					"consecutiveFailures": health["consecutiveFailures"],
				}
				if res.Error != "" {
					entry["error"] = res.Error
				}
				slots[idx] = entry
			}(j, candidates[j])
		}
		wg.Wait()
	}
	for _, entry := range slots {
		if entry != nil {
			result.Results = append(result.Results, entry)
		}
	}
	return result
}
