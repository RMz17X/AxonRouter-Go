package oauth

import (
	"context"
	json "encoding/json/v2"
	"log"
	rand "math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
	internalproxy "axonrouter/proxy/internal/proxy"
)

// Refresh when expiry is within 30 minutes (upstream BACKGROUND_REFRESH_LEAD_MS parity).
const (
	backgroundRefreshLead     = 30 * time.Minute
	backgroundRefreshInterval = 5 * time.Minute
	backgroundInitialDelay    = 10 * time.Second
	backgroundNormalDelay     = 1500 * time.Millisecond
	backgroundSensitiveDelay  = 12 * time.Second
	// defaultBackgroundExpiresIn is used when a token endpoint omits
	// expiresIn, so the refreshed token is not stamped as already-expired.
	defaultBackgroundExpiresIn = 3600
)

// sensitiveProviders mirrors upstream SENSITIVE_PROVIDERS: Google Cloud accounts
// get a longer gap between refreshes to avoid bursting the token endpoint.
var sensitiveProviders = map[string]bool{
	"antigravity": true,
	"gemini-cli":  true,
}

var (
	bgMu      sync.Mutex
	bgStarted bool
)

// ConnectionNeedingRefresh mirrors upstream selectConnectionsNeedingRefresh:
// an active OAuth connection with a refreshToken whose access token expires
// within the lead window.
type ConnectionNeedingRefresh struct {
	ID                   string
	Provider             string
	RefreshToken         string
	AccessToken          string
	ProviderSpecificData map[string]string
	// Proxy assignment, carried so the refresh leaves from the connection's
	// own egress (see backgroundRefreshClient).
	ProxyPoolID            string
	ConnectionProxyEnabled bool
	ConnectionProxyURL     string
}

// SelectConnectionsNeedingRefresh is the pure selection step, exported for tests.
func SelectConnectionsNeedingRefresh(conns []*models.ProviderConnection, now time.Time) []ConnectionNeedingRefresh {
	var out []ConnectionNeedingRefresh
	for _, c := range conns {
		if c == nil {
			continue
		}
		var data struct {
			AccessToken            string         `json:"accessToken"`
			RefreshToken           string         `json:"refreshToken"`
			ExpiresAt              string         `json:"expiresAt"`
			Status                 string         `json:"status"`
			TestStatus             string         `json:"testStatus"`
			DisabledAt             any            `json:"disabledAt"`
			ConnectionProxyEnabled bool           `json:"connectionProxyEnabled"`
			ConnectionProxyURL     string         `json:"connectionProxyUrl"`
			PSD                    map[string]any `json:"providerSpecificData"`
		}
		if c.Data != "" {
			_ = json.Unmarshal([]byte(c.Data), &data)
		}
		if strings.EqualFold(data.Status, "disabled") ||
			strings.EqualFold(data.Status, "validation_required") ||
			strings.EqualFold(data.TestStatus, "disabled") ||
			data.DisabledAt != nil {
			continue
		}
		if data.PSD != nil {
			if blocked, _ := data.PSD["refreshBlocked"].(bool); blocked {
				continue
			}
		}
		if !strings.EqualFold(strings.ReplaceAll(c.AuthType, "_", ""), "oauth") {
			continue
		}
		if strings.TrimSpace(data.RefreshToken) == "" {
			continue
		}
		// A grant the provider already rejected is not retried on every tick:
		// the account stays parked until its cooldown expires, or until it is
		// re-logged in and the row is rewritten.
		if until, ok := db.ConnectionOAuthLockUntil(c.Data); ok && until.After(now) {
			continue
		}
		exp, err := time.Parse(time.RFC3339, strings.TrimSpace(data.ExpiresAt))
		if err != nil {
			continue
		}
		if exp.Sub(now) < backgroundRefreshLead {
			psd := StringMap(data.PSD)
			out = append(out, ConnectionNeedingRefresh{
				ID:                     c.ID,
				Provider:               c.Provider,
				RefreshToken:           data.RefreshToken,
				AccessToken:            data.AccessToken,
				ProviderSpecificData:   psd,
				ProxyPoolID:            psd["proxyPoolId"],
				ConnectionProxyEnabled: data.ConnectionProxyEnabled,
				ConnectionProxyURL:     data.ConnectionProxyURL,
			})
		}
	}
	return out
}

// StartBackgroundRefresh launches the proactive OAuth token refresh loop
// (upstream backgroundTokenRefresh.js parity). Safe to call multiple times.
// Fail-open: per-connection and per-tick failures are logged, never fatal.
func StartBackgroundRefresh(ctx context.Context, repo *db.Repo) {
	bgMu.Lock()
	if bgStarted {
		bgMu.Unlock()
		return
	}
	bgStarted = true
	bgMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backgroundInitialDelay):
		}

		ticker := time.NewTicker(backgroundRefreshInterval)
		defer ticker.Stop()

		runBackgroundRefreshTick(ctx, repo)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runBackgroundRefreshTick(ctx, repo)
			}
		}
	}()
}

// runBackgroundRefreshTick refreshes every due connection, sequentially with
// inter-account delays (upstream parity: Google accounts spaced 12s + jitter).
func runBackgroundRefreshTick(ctx context.Context, repo *db.Repo) {
	if repo == nil {
		return
	}
	conns, err := repo.GetActiveOAuthConnections(ctx)
	if err != nil {
		log.Printf("[BG_TOKEN_REFRESH] load active oauth connections failed: %v", err)
		return
	}

	due := SelectConnectionsNeedingRefresh(conns, time.Now())
	// Randomize the order per tick: a stable DB order would refresh the
	// fleet in the same sequence every cycle, a cadence no set of
	// independent real clients produces.
	rand.Shuffle(len(due), func(i, j int) { due[i], due[j] = due[j], due[i] })
	for i, c := range due {
		refreshBackgroundConnection(ctx, repo, c)
		if i < len(due)-1 {
			delay := backgroundNormalDelay
			if sensitiveProviders[c.Provider] {
				delay = backgroundSensitiveDelay
			}
			// Jitter the gap by +0–50%: fixed spacing marches the whole
			// fleet in lockstep, which is itself a detectable pattern.
			delay += time.Duration(rand.Int64N(int64(delay / 2)))
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}
}

// bgProxyClients caches one client per resolved proxy URL. The set of URLs
// is operator-configured and small, so the cache needs no eviction.
var bgProxyClients sync.Map // string -> *http.Client

func backgroundProxyClient(proxyURLStr string) (*http.Client, bool) {
	if cached, ok := bgProxyClients.Load(proxyURLStr); ok {
		return cached.(*http.Client), true
	}
	parsed, err := url.Parse(proxyURLStr)
	if err != nil || parsed.Host == "" {
		return nil, false
	}
	client := &http.Client{
		Transport: internalproxy.NewSafeTransportForProxy(parsed.String()),
		Timeout:   15 * time.Second,
	}
	actual, _ := bgProxyClients.LoadOrStore(proxyURLStr, client)
	return actual.(*http.Client), true
}

// backgroundPoolClient resolves a pool binding to a proxied client. A plain
// pool id reads the pool directly; a group:/type: target takes the first
// active candidate. Relay pools cannot dial a token endpoint as an HTTP
// proxy, so they decline and the caller falls back.
func backgroundPoolClient(repo *db.Repo, poolID string) (*http.Client, bool) {
	var pool *db.ProxyPool
	if strings.HasPrefix(poolID, "group:") || strings.HasPrefix(poolID, "type:") {
		if candidates := repo.ActivePoolsByTarget(poolID); len(candidates) > 0 {
			pool = candidates[0]
		}
	} else if p, err := repo.GetProxyPool(poolID); err == nil {
		pool = p
	}
	if pool == nil || !pool.IsActive || pool.IsEdgeRelay() {
		return nil, false
	}
	proxyURLStr := pool.NextURL()
	if proxyURLStr == "" {
		return nil, false
	}
	return backgroundProxyClient(proxyURLStr)
}

// backgroundRefreshClient resolves the egress one connection's background
// refresh must use: the connection's own proxy assignment — per-connection
// pool, then the provider-level strategy binding (the one the chat
// pipeline's ResolveProviderProxyPoolID reads), then the legacy
// per-connection proxy fields — and only then the host's direct egress.
//
// The refresh used to dial with a bare client, so every account on an
// instance refreshed from the server's own IP on the same five-minute
// cadence: accounts an operator had deliberately separated by proxy
// collapsed back into one egress cluster for all background traffic, and
// one flag swept the cluster. Upstream wraps the same calls in the
// connection's proxy context (tokenRefresh.js).
func backgroundRefreshClient(repo *db.Repo, c ConnectionNeedingRefresh) *http.Client {
	direct := &http.Client{Timeout: 15 * time.Second}
	if repo == nil {
		return direct
	}
	poolID := c.ProxyPoolID
	if poolID == "" {
		if settings, err := repo.GetSettings(); err == nil && settings != nil && settings.ProviderStrategies != nil {
			for _, key := range []string{c.Provider, providers.ResolveAlias(c.Provider)} {
				if strat, ok := settings.ProviderStrategies[key]; ok && strat.ProxyPoolID != "" && strat.ProxyPoolID != "__none__" {
					poolID = strat.ProxyPoolID
					break
				}
			}
		}
	}
	if poolID != "" {
		if client, ok := backgroundPoolClient(repo, poolID); ok {
			return client
		}
	}
	if c.ConnectionProxyEnabled && c.ConnectionProxyURL != "" {
		if client, ok := backgroundProxyClient(c.ConnectionProxyURL); ok {
			return client
		}
	}
	return direct
}

// refreshBackgroundConnection refreshes one due connection and persists the
// result back to the row (fail-open: errors only logged).
func refreshBackgroundConnection(ctx context.Context, repo *db.Repo, c ConnectionNeedingRefresh) {
	if repo != nil {
		if latest, err := repo.GetProviderConnectionByID(c.ID); err != nil || latest == nil || latest.IsActive == 0 {
			return
		}
	}
	result, err := Refresh(ctx, &Params{
		Client:               backgroundRefreshClient(repo, c),
		Provider:             c.Provider,
		RefreshToken:         c.RefreshToken,
		AccessToken:          c.AccessToken,
		ProviderSpecificData: c.ProviderSpecificData,
	})
	if err != nil || result == nil || result.AccessToken == "" {
		log.Printf("[BG_TOKEN_REFRESH] refresh failed conn=%s provider=%s: %v", c.ID, c.Provider, err)
		// Record the rejection so the tick, and the request path, stop
		// hammering the token endpoint for a grant only a re-login fixes.
		if providers.IsRefreshGrantDead(err) {
			if until, failures, lockErr := repo.RecordConnectionOAuthFailure(c.ID, http.StatusUnauthorized, err.Error()); lockErr != nil {
				log.Printf("[BG_TOKEN_REFRESH] park failed conn=%s: %v", c.ID, lockErr)
			} else {
				_ = repo.SetConnectionStatus(c.ID, false)
				log.Printf("[BG_TOKEN_REFRESH] parked and disabled conn=%s until=%s failures=%d", c.ID, until.Format(time.RFC3339), failures)
			}
		}
		return
	}

	conn, err := repo.GetProviderConnectionByID(c.ID)
	if err != nil || conn == nil {
		log.Printf("[BG_TOKEN_REFRESH] read row failed conn=%s: %v", c.ID, err)
		return
	}
	var existing map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &existing)
	}
	if existing == nil {
		existing = make(map[string]any)
	}
	// A provider that omits expiresIn yields ExpiresIn=0; feeding that to
	// BuildConnectionUpdate stamps expiresAt=now, so the next tick re-selects
	// this connection forever. Fall back to a conservative window.
	refreshed := result
	if refreshed.ExpiresIn <= 0 {
		refreshed = &TokenResult{
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
			ExpiresIn:    defaultBackgroundExpiresIn,
			Scope:        result.Scope,
			ProjectID:    result.ProjectID,
		}
	}
	prevAccess, _ := existing["accessToken"].(string)
	SyncMirroredAPIKey(existing, c.Provider, prevAccess, refreshed.AccessToken)
	for k, v := range BuildConnectionUpdate(refreshed) {
		existing[k] = v
	}
	if result.ProjectID != "" {
		existing["projectId"] = result.ProjectID
	}
	merged, err := json.Marshal(existing)
	if err != nil {
		log.Printf("[BG_TOKEN_REFRESH] marshal failed conn=%s: %v", c.ID, err)
		return
	}
	if err := repo.UpdateConnectionData(c.ID, string(merged)); err != nil {
		log.Printf("[BG_TOKEN_REFRESH] persist failed conn=%s: %v", c.ID, err)
		return
	}
	log.Printf("[BG_TOKEN_REFRESH] refreshed conn=%s provider=%s", c.ID, c.Provider)
}
