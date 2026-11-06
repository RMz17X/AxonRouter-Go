package dashboard

import (
	"context"
	json "encoding/json/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/fetchgate"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/proxy"
)

// quotaFetchGate paces every live quota read this handler makes (issue #30).
//
// The quota tracker refreshes every visible connection in one tick, so a user
// with ten accounts behind one office IP fired ten quota reads within a few
// milliseconds. Google answered 429, and the chat path reads a 429 as real
// quota exhaustion — locking accounts whose tokens were still live, and taking
// the paid combos down with them. Upstream decolua/9router has no throttle here
// either, so this is a deliberate gap and not a parity regression.
//
// The floor is 250ms with up to 120ms of jitter on top: enough that a burst
// stops looking like a fleet sharing one egress IP, cheap enough that ten
// accounts still refresh inside a couple of seconds. Only the start of a
// request is paced, so a single account's manual refresh is never delayed.
var quotaFetchGate = fetchgate.New(250*time.Millisecond, 120*time.Millisecond)

// usageResponseTTL mirrors upstream's USAGE_RESPONSE_TTL_S (55s): the quota
// tracker polls every 60s per open tab, so one upstream read per connection
// per poll cycle is the ceiling however many tabs are open. Before this
// cache, every poll from every tab hit the provider live for every account —
// a standing burst of loadCodeAssist/fetchAvailableModels calls that made
// dashboard browsing itself look like automation to the upstream.
const usageResponseTTL = 55 * time.Second

// usageCacheMax bounds the response cache; entries are keyed by connection
// id, so the ceiling only needs to cover a large operator's connection list.
const usageCacheMax = 1024

type usageCacheEntry struct {
	body    []byte
	expires time.Time
}

var (
	usageCacheMu sync.Mutex
	usageCache   = make(map[string]usageCacheEntry)
)

func cachedUsageResponse(connID string) ([]byte, bool) {
	usageCacheMu.Lock()
	defer usageCacheMu.Unlock()
	entry, ok := usageCache[connID]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(usageCache, connID)
		return nil, false
	}
	return entry.body, true
}

func storeUsageResponse(connID string, body []byte) {
	usageCacheMu.Lock()
	defer usageCacheMu.Unlock()
	if len(usageCache) >= usageCacheMax {
		now := time.Now()
		for id, entry := range usageCache {
			if now.After(entry.expires) {
				delete(usageCache, id)
			}
		}
	}
	usageCache[connID] = usageCacheEntry{body: body, expires: time.Now().Add(usageResponseTTL)}
}

func writeRawUsageResponse(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeUsageResponse writes one live usage read and stores it in the short
// response cache for the next poll inside the TTL.
func (h *DashboardHandler) writeUsageResponse(w http.ResponseWriter, connID string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, payload)
		return
	}
	storeUsageResponse(connID, body)
	writeRawUsageResponse(w, body)
}

// providerStrategyProxyClient resolves the provider-level pool binding (the
// settings strategy the chat pipeline's ResolveProviderProxyPoolID reads)
// into a client for dashboard reads. probeHTTPClient covers per-connection
// pools and legacy proxy fields; this covers the binding an operator sets
// once per provider. Group/type/rotation targets stay chat-pipeline
// concerns — a plain pool id is the only shape honoured here, and a relay
// pool is not dialed as an HTTP proxy.
func (h *DashboardHandler) providerStrategyProxyClient(provider string) *http.Client {
	if h.Repo == nil {
		return nil
	}
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil || settings.ProviderStrategies == nil {
		return nil
	}
	var poolID string
	for _, key := range []string{provider, providers.ResolveAlias(provider)} {
		if strat, ok := settings.ProviderStrategies[key]; ok && strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
			poolID = strat.ProxyPoolID
			break
		}
	}
	if poolID == "" || strings.Contains(poolID, ":") {
		return nil
	}
	pool, err := h.Repo.GetProxyPool(poolID)
	if err != nil || pool == nil || !pool.IsActive || pool.IsEdgeRelay() {
		return nil
	}
	proxyURLStr := pool.NextURL()
	if proxyURLStr == "" {
		return nil
	}
	parsed, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil
	}
	tr := proxy.NewSafeTransport()
	tr.Proxy = http.ProxyURL(parsed)
	return &http.Client{Transport: proxy.NewFallbackTransport(tr), Timeout: usageHTTPTimeout}
}

// acquireQuotaSlot blocks until this request may talk to the provider. It
// returns false when the client gave up while queued — usually a dashboard
// that navigated away mid-refresh — in which case the caller abandons the
// fetch instead of spending an upstream request on a response nobody reads.
func acquireQuotaSlot(w http.ResponseWriter, r *http.Request) bool {
	if err := quotaFetchGate.Acquire(r.Context()); err != nil {
		if r.Context().Err() == nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		}
		return false
	}
	return true
}

// HandleGetConnectionUsage handles GET /api/usage/{connectionId}
//
// Upstream parity (src/app/api/usage/[connectionId]/route.js): an OAuth
// connection inside its expiry window is refreshed before the read, and a
// provider that reports an auth failure as a message rather than a status is
// force-refreshed and read once more. Both halves are what keep a stale token
// from surfacing as "quota API rejected the current token. Chat may still
// work." while chat traffic — refreshing on its own path — keeps working
// (#78 item 4).
func (h *DashboardHandler) HandleGetConnectionUsage(w http.ResponseWriter, r *http.Request) {
	connID := getURLParam(r, "connectionId")
	if connID == "" {
		connID = getURLParam(r, "id")
	}
	if connID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connectionId")
		return
	}

	conn, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	var data map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &data)
	}

	// Disabled accounts must never trigger OAuth token refresh or upstream quota
	// reads; return the stored error/reason directly.
	if conn.IsActive == 0 {
		msg := lastErrorText(data)
		if msg == "" {
			msg = "Account is disabled."
		}
		msg = enrichAppealMessage(msg)
		handlerutil.WriteJSON(w, http.StatusOK, usageResult{
			plan:    conn.Provider,
			message: msg,
		}.toResponse())
		return
	}

	// Short response cache (upstream route.js): served before proxy
	// resolution and token refresh, so a hit costs no upstream call at all.
	// ?force=1 is the caller's explicit re-read and skips the lookup.
	if r.URL.Query().Get("force") != "1" {
		if body, ok := cachedUsageResponse(connID); ok {
			writeRawUsageResponse(w, body)
			return
		}
	}

	// Live provider quota fetchers (ports of open-sse/services/usage/*.js).
	// Antigravity keeps its existing dedicated path below.
	if !acquireQuotaSlot(w, r) {
		return
	}

	// This connection's reads and refreshes leave through its own proxy when
	// it has one — per-connection assignment first (probeHTTPClient, the
	// same resolution the connection test uses), then the provider-level
	// pool binding. The fallback wrap mirrors upstream's strictProxy=false
	// for quota reads: a dead proxy degrades to direct instead of failing
	// the read. Relay pools are not dialed here (probeHTTPClient reports
	// them with an empty proxy URL) and keep the shared client.
	fetchCtx := r.Context()
	if client, proxyURL := h.probeHTTPClient(parseConnectionProbeData(data), data); client != nil && proxyURL != "" {
		wrapped := *client
		wrapped.Transport = proxy.NewFallbackTransport(client.Transport)
		fetchCtx = withUsageHTTPClient(fetchCtx, &wrapped)
	} else if client := h.providerStrategyProxyClient(conn.Provider); client != nil {
		fetchCtx = withUsageHTTPClient(fetchCtx, client)
	}

	creds := usageCredentials{raw: data, provider: conn.Provider}
	// A pre-read refresh only makes sense when the token is inside its expiry
	// window. When it is not, the single retry below is the one attempt: a
	// second call would repeat a request whose outcome is already known.
	preRead := usageRefreshOutcome{}
	if creds.needsRefresh() {
		preRead = h.runUsageRefresh(fetchCtx, conn, creds)
		if preRead.err != nil {
			log.Warn("usage", "credential refresh failed", "provider", conn.Provider, "conn", connID, "error", preRead.err)
		}
	}

	res, ok := fetchProviderUsage(fetchCtx, conn.Provider, data)
	// Upstream force-refreshes and retries exactly once here
	// (src/app/api/usage/[connectionId]/route.js): a stored access token can age
	// out while the refresh token is still good. That retry is only worth a call
	// when this request has not already had one and it failed — repeating a
	// rejected exchange just spent the call twice and logged the same failure
	// twice, which is what the double warning per account came from.
	if ok && !preRead.attempted && isAuthExpiredUsageMessage(res) {
		retry := h.runUsageRefresh(fetchCtx, conn, creds)
		if retry.err != nil {
			log.Warn("usage", "forced credential refresh failed", "provider", conn.Provider, "conn", connID, "error", retry.err)
		} else if retry.refreshed {
			if retried, retryOK := fetchProviderUsage(fetchCtx, conn.Provider, data); retryOK {
				h.writeUsageResponse(w, connID, retried.toResponse())
				return
			}
		}
	}
	if ok {
		h.writeUsageResponse(w, connID, res.toResponse())
		return
	}

	// Antigravity quota resolution (dashboard presentation with tier check +
	// weekly overlay — parity with open-sse/services/usage/google.js).
	if conn.Provider == "antigravity" && data != nil {
		accessToken, _ := data["accessToken"].(string)
		projectID, _ := data["projectId"].(string)
		if accessToken != "" {
			// The slot above is already held; taking a second one made every
			// Antigravity account queue for two consecutive 250ms gaps even
			// though it issues one burst of upstream reads.

			// The gate is paced on the request context, not this deadline, so a
			// long queue cannot expire a fetch that has not started yet; the
			// 30s budget covers the upstream call itself.
			ctx, cancel := context.WithTimeout(fetchCtx, 30*time.Second)
			defer cancel()

			res := fetchAntigravityDashboardUsage(ctx, accessToken, projectID, providers.AntigravityUserAgent())
			if res.message != "" {
				lowerMsg := strings.ToLower(res.message)
				if strings.Contains(lowerMsg, "disabled in this account") ||
					strings.Contains(lowerMsg, "terms of service") ||
					strings.Contains(lowerMsg, "tos_violation") ||
					strings.Contains(lowerMsg, "account disabled") {
					res.message = enrichAppealMessage(res.message)
					_ = h.Repo.DisableConnectionWithReason(conn.ID, http.StatusForbidden, res.message)
				} else if strings.Contains(lowerMsg, "validation_required") ||
					strings.Contains(lowerMsg, "verify your account") {
					_ = h.Repo.SetConnectionVerificationRequired(conn.ID, "https://antigravity.google", res.message)
				}
			}
			h.writeUsageResponse(w, connID, res.toResponse())
			return
		}
	}

	// Fallback for connections with rate limit or locks in data
	respQuotas := make(map[string]any)
	if data != nil {
		if rateLimitedUntil, ok := data["rateLimitedUntil"].(string); ok && rateLimitedUntil != "" {
			respQuotas["default"] = map[string]any{
				"remainingPercentage": 0,
				"resetAt":             rateLimitedUntil,
				"displayName":         conn.Provider,
			}
		}
		for k, v := range data {
			if len(k) > 10 && k[:10] == "modelLock_" {
				model := k[10:]
				if lockStr, ok := v.(string); ok && lockStr != "" {
					respQuotas[model] = map[string]any{
						"remainingPercentage": 0,
						"resetAt":             lockStr,
						"displayName":         model,
					}
				}
			}
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"plan":   conn.Provider,
		"quotas": respQuotas,
	})
}

// HandleGetUsageProviders handles GET /api/usage/providers
func (h *DashboardHandler) HandleGetUsageProviders(w http.ResponseWriter, r *http.Request) {
	provList, err := h.Repo.GetActiveProviders(r.Context())
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "Failed to fetch providers")
		return
	}

	seen := make(map[string]bool)
	type ProviderItem struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var list []ProviderItem
	for _, prov := range provList {
		if prov != "" && !seen[prov] {
			canonical := providers.ResolveAlias(prov)
			if cfg, ok := providers.KnownProviders[canonical]; ok && cfg.Hidden {
				continue
			}
			seen[prov] = true
			list = append(list, ProviderItem{
				ID:   prov,
				Name: prov,
			})
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"providers": list,
	})
}
