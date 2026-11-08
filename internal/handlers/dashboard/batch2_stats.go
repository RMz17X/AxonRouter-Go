package dashboard

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

// GET /api/providers/stats — port of AxonRouter's providers/stats route +
// connectionsRepo.getProviderSummaryStats. Connections are classified with
// the same predicates as AxonRouter's SQL fragments, translated to this
// codebase's data-blob storage:
//   - PERMANENT_UNAVAILABLE: bad test status (unavailable/error/expired/
//     invalid/disabled), a disabledAt marker, a refreshBlocked PSD flag, or
//     a fatal last error (banned/suspended/revoked/invalid grant/...).
//   - FUTURE_ACCOUNT_LOCK: lockedAllUntil, a "__all" model lock, or
//     rateLimitedUntil still in the future.
//   - FUTURE_MODEL_LOCK: any other live per-model lock.
//   - exhausted: testStatus "exhausted" — for antigravity only when the
//     stored quota snapshot shows both the gemini and claude families at
//     zero remaining.

type providerSummaryStat struct {
	Total       int  `json:"total"`
	Connected   int  `json:"connected"`
	Exhausted   int  `json:"exhausted"`
	Error       int  `json:"error"`
	AllDisabled bool `json:"allDisabled"`
	LastErrorAt any  `json:"lastErrorAt"`
}

var badTestStatuses = map[string]bool{
	"unavailable": true, "error": true, "expired": true, "invalid": true, "disabled": true,
}

// antigravityBothFamiliesExhausted mirrors ANTIGRAVITY_BOTH_FAMILIES_EXHAUSTED_SQL
// over the stored quotas object: each family has at least one window and
// no window in it reports remainingPercentage > 0.
func antigravityBothFamiliesExhausted(quotas map[string]any) bool {
	familyZero := func(prefix string) bool {
		seen := false
		for name, raw := range quotas {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			seen = true
			window, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if pct, ok := window["remainingPercentage"].(float64); ok && pct > 0 {
				return false
			}
		}
		return seen
	}
	return familyZero("gemini") && familyZero("claude")
}

type connClass struct {
	permanent   bool
	accountLock bool
	modelLock   bool
	exhausted   bool
}

func classifyConnection(c *models.ProviderConnection, now time.Time) connClass {
	data := connDataMap(c)
	testStatus := dataStr(data, "testStatus")
	if testStatus == "" {
		testStatus = "active"
	}
	locks := activeModelLocks(data, now)
	_, accountLockUntil := futureTime(data, "lockedAllUntil", now)
	_, rateLimited := futureTime(data, "rateLimitedUntil", now)
	_, allLock := locks["__all"]
	modelLock := false
	for model := range locks {
		if model != "__all" {
			modelLock = true
			break
		}
	}
	permanent := badTestStatuses[testStatus] ||
		data["disabledAt"] != nil ||
		refreshBlocked(dataMap(data, "providerSpecificData")) ||
		fatalConnectionError.MatchString(lastErrorText(data))
	exhausted := false
	if c.IsActive == 1 && testStatus == "exhausted" {
		if c.Provider != "antigravity" {
			exhausted = true
		} else if antigravityBothFamiliesExhausted(dataMap(data, "quotas")) {
			exhausted = true
		}
	}
	return connClass{
		permanent:   permanent,
		accountLock: accountLockUntil || rateLimited || allLock,
		modelLock:   modelLock,
		exhausted:   exhausted,
	}
}

func computeProviderSummaryStats(conns []*models.ProviderConnection) map[string]map[string]*providerSummaryStat {
	now := time.Now()
	stats := map[string]map[string]*providerSummaryStat{}
	disabled := map[string]map[string]int{}
	lastErrAt := map[string]map[string]time.Time{}
	for _, c := range conns {
		if c == nil {
			continue
		}
		byAuth, ok := stats[c.Provider]
		if !ok {
			byAuth = map[string]*providerSummaryStat{}
			stats[c.Provider] = byAuth
			disabled[c.Provider] = map[string]int{}
			lastErrAt[c.Provider] = map[string]time.Time{}
		}
		stat, ok := byAuth[c.AuthType]
		if !ok {
			stat = &providerSummaryStat{}
			byAuth[c.AuthType] = stat
		}
		stat.Total++
		data := connDataMap(c)
		if t, ok := dataTimeField(data, "lastErrorAt"); ok {
			if cur, seen := lastErrAt[c.Provider][c.AuthType]; !seen || t.After(cur) {
				lastErrAt[c.Provider][c.AuthType] = t
				stat.LastErrorAt = data["lastErrorAt"]
			}
		}
		if c.IsActive != 1 {
			disabled[c.Provider][c.AuthType]++
			continue
		}
		cls := classifyConnection(c, now)
		isAnti := c.Provider == "antigravity"
		switch {
		case cls.exhausted:
			stat.Exhausted++
		case cls.permanent || cls.accountLock || (!isAnti && cls.modelLock):
			stat.Error++
		default:
			stat.Connected++
		}
	}
	for provider, byAuth := range stats {
		for authType, stat := range byAuth {
			stat.AllDisabled = stat.Total > 0 && disabled[provider][authType] == stat.Total
		}
	}
	return stats
}

// The summary is cached for 10 minutes like requested by operator;
// mutations to connections invalidate it immediately.
var (
	summaryStatsCache   map[string]map[string]*providerSummaryStat
	summaryStatsExpires time.Time
	summaryStatsMu      sync.Mutex
)

// InvalidateProviderStatsCache clears the cached provider stats so the next request computes fresh stats.
func InvalidateProviderStatsCache() {
	summaryStatsMu.Lock()
	summaryStatsCache = nil
	summaryStatsMu.Unlock()
}

func (h *DashboardHandler) HandleProviderStats(w http.ResponseWriter, r *http.Request) {
	summaryStatsMu.Lock()
	if summaryStatsCache != nil && time.Now().Before(summaryStatsExpires) {
		cached := summaryStatsCache
		summaryStatsMu.Unlock()
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"stats": cached})
		return
	}
	summaryStatsMu.Unlock()

	// High-performance direct SQL aggregate:
	// Running a GROUP BY in PostgreSQL takes ~20ms instead of transferring 170k rows over TCP.
	query := `
		SELECT provider, authType,
		       COUNT(*) as total,
		       COUNT(CASE WHEN isActive = 1 THEN 1 END) as connected,
		       COUNT(CASE WHEN isActive = 0 THEN 1 END) as disabled
		FROM providerConnections
		GROUP BY provider, authType
	`
	rows, err := h.Repo.QueryContext(r.Context(), query)
	if err == nil {
		defer func() { _ = rows.Close() }()
		stats := make(map[string]map[string]*providerSummaryStat)
		for rows.Next() {
			var provider, authType string
			var total, connected, disabled int
			if scanErr := rows.Scan(&provider, &authType, &total, &connected, &disabled); scanErr == nil {
				if _, ok := stats[provider]; !ok {
					stats[provider] = make(map[string]*providerSummaryStat)
				}
				stats[provider][authType] = &providerSummaryStat{
					Total:       total,
					Connected:   connected,
					AllDisabled: total > 0 && disabled == total,
				}
			}
		}
		if rows.Err() == nil && len(stats) > 0 {
			summaryStatsMu.Lock()
			summaryStatsCache = stats
			summaryStatsExpires = time.Now().Add(10 * time.Minute)
			summaryStatsMu.Unlock()

			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"stats": stats})
			return
		}
	}

	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch provider stats"})
		return
	}
	stats := computeProviderSummaryStats(conns)

	summaryStatsMu.Lock()
	summaryStatsCache = stats
	summaryStatsExpires = time.Now().Add(10 * time.Minute)
	summaryStatsMu.Unlock()

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

// ---------------------------------------------------------------------------
// GET /api/providers/{id}/proxy-stats — port of AxonRouter's
// [id]/proxy-stats route + analyticsRepo.getProviderProxyStats, with the
// per-attempt analytics_events stream replaced by this codebase's
// per-request ledger (db.GetAnalyticsEvents — the batch-1 precedent).
// Group mode resolves custom proxy groups (batch 4 store) by name, then
// id: a group with poolIds owns its membership by id set and contributes
// its sticky settings; otherwise the group/type label match applies —
// AxonRouter's own fallback — with isSticky=false / stickyLimit=3.
// ---------------------------------------------------------------------------

type windowStats struct {
	Total       int
	Success     int
	Failure     int
	Cancelled   int
	SuccessRate float64
}

func (s windowStats) toMap() map[string]any {
	m := map[string]any{
		"total":       s.Total,
		"success":     s.Success,
		"failure":     s.Failure,
		"successRate": s.SuccessRate,
	}
	if s.Total > 0 {
		m["cancelled"] = s.Cancelled
	}
	return m
}

func summarizeEvents(rows []db.AnalyticsEventRow) windowStats {
	stats := windowStats{SuccessRate: 100}
	for _, row := range rows {
		stats.Total++
		if row.Success {
			stats.Success++
		} else if row.ErrorCategory == "cancelled" {
			stats.Cancelled++
		} else {
			stats.Failure++
		}
	}
	if denom := stats.Total - stats.Cancelled; denom > 0 {
		stats.SuccessRate = math.Round(float64(stats.Success)/float64(denom)*1000) / 10
	}
	return stats
}

// providerStrategyRaw reads one provider's strategy object out of the raw
// settings (the typed settings struct drops proxyGroup/fallbackStrategy),
// with getProviderStrategy's canonical-then-alias key resolution.
func providerStrategyRaw(settings map[string]any, providerID string) map[string]any {
	strategies, _ := settings["providerStrategies"].(map[string]any)
	if strategies == nil {
		return map[string]any{}
	}
	if direct, ok := strategies[providerID].(map[string]any); ok {
		return direct
	}
	for key, value := range strategies {
		if key == providerID {
			continue
		}
		if providers.ResolveAlias(key) == providerID {
			if m, ok := value.(map[string]any); ok {
				return m
			}
		}
	}
	return map[string]any{}
}

func poolHealthCounts(pools []map[string]any, degradedStrict bool) (active, degraded, unhealthy int) {
	failures := func(p map[string]any) float64 {
		if v, ok := p["consecutiveFailures"].(float64); ok {
			return v
		}
		return 0
	}
	for _, p := range pools {
		isActive, _ := p["isActive"].(bool)
		testStatus, _ := p["testStatus"].(string)
		f := failures(p)
		if isActive && testStatus != "unhealthy" && testStatus != "dead" && f < 3 {
			active++
		}
		if degradedStrict {
			if isActive && testStatus == "degraded" && f > 0 && f < 3 {
				degraded++
			}
			if !isActive || testStatus == "unhealthy" || testStatus == "dead" || f >= 3 {
				unhealthy++
			}
		} else {
			if isActive && testStatus == "degraded" {
				degraded++
			}
			if !isActive || f >= 3 {
				unhealthy++
			}
		}
	}
	return active, degraded, unhealthy
}

func (h *DashboardHandler) HandleProviderProxyStats(w http.ResponseWriter, r *http.Request) {
	requestedID := getURLParam(r, "id")
	if requestedID == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Provider ID is required"})
		return
	}
	providerID := providers.ResolveAlias(requestedID)

	settings, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal error"})
		return
	}
	strategy := providerStrategyRaw(settings, providerID)
	proxyGroup := dataStr(strategy, "proxyGroup")
	proxyPoolID := dataStr(strategy, "proxyPoolId")
	rotateStrategy := dataStr(strategy, "rotateStrategy")

	routingMode := "direct"
	switch {
	case proxyGroup != "":
		routingMode = "group"
	case rotateStrategy != "" && rotateStrategy != "none":
		routingMode = "all"
	case proxyPoolID != "" && proxyPoolID != "__none__":
		routingMode = "single"
	}
	proxyConfig := map[string]any{
		"mode":                     routingMode,
		"proxyGroup":               nil,
		"proxyPoolId":              nil,
		"rotateStrategy":           "none",
		"fallbackStrategy":         nil,
		"autoAppliedToNewAccounts": true,
	}
	if proxyGroup != "" {
		proxyConfig["proxyGroup"] = proxyGroup
	}
	if proxyPoolID != "" {
		proxyConfig["proxyPoolId"] = proxyPoolID
	}
	if rotateStrategy != "" {
		proxyConfig["rotateStrategy"] = rotateStrategy
	}
	if fb := dataStr(strategy, "fallbackStrategy"); fb != "" {
		proxyConfig["fallbackStrategy"] = fb
	}

	// Window stats from the request ledger (errors are swallowed into the
	// zero defaults, as in AxonRouter's repo function).
	now := time.Now().UTC()
	stats24h := windowStats{SuccessRate: 100}
	stats1h := windowStats{SuccessRate: 100}
	errorBreakdown := make([]map[string]any, 0)
	fmtBound := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z") }
	if rows, err := h.Repo.GetAnalyticsEvents(r.Context(),
		fmtBound(now.Add(-24*time.Hour)), fmtBound(now), providerID, ""); err == nil {
		stats24h = summarizeEvents(rows)
		counts := map[string]int{}
		for _, row := range rows {
			if !row.Success {
				category := row.ErrorCategory
				if category == "" {
					category = "unknown"
				}
				counts[category]++
			}
		}
		type catCount struct {
			category string
			count    int
		}
		sorted := make([]catCount, 0, len(counts))
		for category, count := range counts {
			sorted = append(sorted, catCount{category, count})
		}
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].count != sorted[j].count {
				return sorted[i].count > sorted[j].count
			}
			return sorted[i].category < sorted[j].category
		})
		for i, cc := range sorted {
			if i >= 5 {
				break
			}
			errorBreakdown = append(errorBreakdown, map[string]any{"category": cc.category, "count": cc.count})
		}
	}
	if rows, err := h.Repo.GetAnalyticsEvents(r.Context(),
		fmtBound(now.Add(-time.Hour)), fmtBound(now), providerID, ""); err == nil {
		stats1h = summarizeEvents(rows)
	}

	// Held as any: a typed nil map would marshal as {} under
	// encoding/json/v2, where the contract wants null.
	var groupHealth any
	var singlePoolHealth any
	pools, poolsErr := h.Repo.ListProxyPools()
	if poolsErr == nil {
		switch routingMode {
		case "group":
			var customGroup *db.ProxyGroup
			if g, err := h.Repo.GetProxyGroupByName(proxyGroup); err == nil && g != nil {
				customGroup = g
			} else if g, err := h.Repo.GetProxyGroupByID(proxyGroup); err == nil && g != nil {
				customGroup = g
			}
			matching := make([]map[string]any, 0)
			if customGroup != nil && len(customGroup.PoolIDs) > 0 {
				idSet := make(map[string]bool, len(customGroup.PoolIDs))
				for _, pid := range customGroup.PoolIDs {
					idSet[pid] = true
				}
				for _, p := range pools {
					if idSet[handlerutil.GetString(p, "id")] {
						matching = append(matching, p)
					}
				}
			} else {
				for _, p := range pools {
					group, _ := p["group"].(string)
					typ, _ := p["type"].(string)
					if strings.EqualFold(group, proxyGroup) || strings.EqualFold(typ, proxyGroup) {
						matching = append(matching, p)
					}
				}
			}
			active, degraded, unhealthy := poolHealthCounts(matching, true)
			healthRate := 0
			if len(matching) > 0 {
				healthRate = int(math.Round(float64(active) / float64(len(matching)) * 100))
			}
			isSticky, stickyLimit := false, 3
			if customGroup != nil {
				isSticky = customGroup.IsSticky
				if customGroup.StickyLimit > 0 {
					stickyLimit = customGroup.StickyLimit
				}
			}
			groupHealth = map[string]any{
				"name":           proxyGroup,
				"totalPools":     len(matching),
				"activePools":    active,
				"degradedPools":  degraded,
				"unhealthyPools": unhealthy,
				"healthRate":     healthRate,
				"isSticky":       isSticky,
				"stickyLimit":    stickyLimit,
			}
		case "all":
			active, degraded, unhealthy := poolHealthCounts(pools, false)
			healthRate := 0
			if len(pools) > 0 {
				healthRate = int(math.Round(float64(active) / float64(len(pools)) * 100))
			}
			groupHealth = map[string]any{
				"name":           "All Active Pools",
				"totalPools":     len(pools),
				"activePools":    active,
				"degradedPools":  degraded,
				"unhealthyPools": unhealthy,
				"healthRate":     healthRate,
			}
		case "single":
			for _, p := range pools {
				if idStr, _ := p["id"].(string); idStr == proxyPoolID {
					failures := 0
					if v, ok := p["consecutiveFailures"].(float64); ok {
						failures = int(v)
					}
					singlePoolHealth = map[string]any{
						"id":                  p["id"],
						"name":                p["name"],
						"type":                p["type"],
						"isActive":            p["isActive"],
						"testStatus":          p["testStatus"],
						"consecutiveFailures": failures,
						"lastTestedAt":        p["lastTestedAt"],
						"lastError":           p["lastError"],
					}
					break
				}
			}
		}
	}

	connectionCount := 0
	if conns, err := h.Repo.GetProviderConnections(providerID, false); err == nil {
		connectionCount = len(conns)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"providerId":       providerID,
		"proxyConfig":      proxyConfig,
		"stats24h":         stats24h.toMap(),
		"stats1h":          stats1h.toMap(),
		"errorBreakdown":   errorBreakdown,
		"groupHealth":      groupHealth,
		"singlePoolHealth": singlePoolHealth,
		"connectionCount":  connectionCount,
	})
}
