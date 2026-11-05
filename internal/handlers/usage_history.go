package handlers

import (
	"net/http"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/usagetracker"
)

// The wire buckets of GET /api/usage/history. AxonRouter's grouped stats
// carry exactly these fields per map (no cached tokens — its UsageBucket
// never had them), so each map gets its own item shape.
type historyProviderItem struct {
	Provider         string  `json:"provider"`
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	LastUsed         string  `json:"lastUsed"`
}

type historyModelItem struct {
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	LastUsed         string  `json:"lastUsed"`
}

type historyAccountItem struct {
	AccountName      *string `json:"accountName"`
	ConnectionID     *string `json:"connectionId"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	LastUsed         string  `json:"lastUsed"`
}

type historyAPIKeyItem struct {
	KeyName          string  `json:"keyName"`
	APIKeyID         *string `json:"apiKeyId"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	LastUsed         string  `json:"lastUsed"`
}

type historyEndpointItem struct {
	Endpoint         string  `json:"endpoint"`
	RawModel         string  `json:"rawModel"`
	Provider         string  `json:"provider"`
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	Cost             float64 `json:"cost"`
	LastUsed         string  `json:"lastUsed"`
}

// historyMinuteBucket is one of AxonRouter's last10Minutes buckets: full
// token/cost counters per minute, no timestamp (the position is the minute).
type historyMinuteBucket struct {
	Requests         int     `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	Cost             float64 `json:"cost"`
}

type usageHistoryResponse struct {
	TotalRequests         int                            `json:"totalRequests"`
	TotalPromptTokens     int64                          `json:"totalPromptTokens"`
	TotalCompletionTokens int64                          `json:"totalCompletionTokens"`
	TotalTokens           int64                          `json:"totalTokens"`
	TotalCost             float64                        `json:"totalCost"`
	ByProvider            map[string]historyProviderItem `json:"byProvider"`
	ByModel               map[string]historyModelItem    `json:"byModel"`
	ByAccount             map[string]historyAccountItem  `json:"byAccount"`
	ByAPIKey              map[string]historyAPIKeyItem   `json:"byApiKey"`
	ByEndpoint            map[string]historyEndpointItem `json:"byEndpoint"`
	Last10Minutes         []historyMinuteBucket          `json:"last10Minutes"`
	Pending               usagetracker.PendingState      `json:"pending"`
	ActiveRequests        []axonActiveRequest            `json:"activeRequests"`
	RecentRequests        []axonRecentRequest            `json:"recentRequests"`
	ErrorProvider         string                         `json:"errorProvider"`
}

// HandleUsageHistory serves GET /api/usage/history — AxonRouter's stats
// payload under its own route and period vocabulary (24h/7d/30d/60d/all,
// default 7d, anything else a 400). The aggregation reads usageHistory
// directly for every period; AxonRouter serves the day windows from a
// relational daily summary this build does not keep (its usageDaily rows
// are JSON blobs), and the totals agree because both fold the same rows.
func HandleUsageHistory(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		period := r.URL.Query().Get("period")
		if period == "" {
			period = "7d"
		}
		now := time.Now()
		filter := db.UsageWindowFilter{Until: now.UTC().Format("2006-01-02T15:04:05.000Z")}
		switch period {
		case "24h":
			filter.Since = now.Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
		case "7d", "30d", "60d":
			days := 7
			if period == "30d" {
				days = 30
			} else if period == "60d" {
				days = 60
			}
			// AxonRouter's day windows cover the last N local calendar
			// dates including today.
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
			filter.Since = start.UTC().Format("2006-01-02T15:04:05.000Z")
		case "all":
		default:
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid period"})
			return
		}

		fail := func() {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch usage stats"})
		}

		groups, err := repo.GetUsageHistoryGrouped(r.Context(), filter)
		if err != nil {
			fail()
			return
		}

		// Account names resolve from the connections table for the unique connection IDs in groups:
		connMap := make(map[string]string)
		var connIDs []string
		connIDSet := make(map[string]struct{})
		for _, g := range groups {
			if g.ConnectionID != "" {
				if _, ok := connIDSet[g.ConnectionID]; !ok {
					connIDSet[g.ConnectionID] = struct{}{}
					connIDs = append(connIDs, g.ConnectionID)
				}
			}
		}
		if len(connIDs) > 0 {
			if m, err := repo.GetConnectionNames(r.Context(), connIDs); err == nil {
				connMap = m
			}
		}
		// API-key names resolve against both the stored form and its mask,
		// like the stats handler: rows this build wrote hold the masked key,
		// rows the Next.js dashboard wrote can hold the full one.
		keyNames := make(map[string]string)
		if keys, err := repo.GetApiKeys(); err == nil {
			for _, k := range keys {
				if k.Name == nil || *k.Name == "" {
					continue
				}
				keyNames[k.Key] = *k.Name
				if m := maskAPIKeyForStats(k.Key); m != "" {
					keyNames[m] = *k.Name
				}
			}
		}

		resp := usageHistoryResponse{
			ByProvider: make(map[string]historyProviderItem),
			ByModel:    make(map[string]historyModelItem),
			ByAccount:  make(map[string]historyAccountItem),
			ByAPIKey:   make(map[string]historyAPIKeyItem),
			ByEndpoint: make(map[string]historyEndpointItem),
		}
		for _, g := range groups {
			resp.TotalRequests += g.Requests
			resp.TotalPromptTokens += g.PromptTokens
			resp.TotalCompletionTokens += g.CompletionTokens
			resp.TotalTokens += g.TotalTokens
			resp.TotalCost += g.Cost

			provider := g.Provider
			if provider == "" {
				provider = "unknown"
			}
			rawModel := g.Model
			if rawModel == "" {
				rawModel = "unknown"
			}
			endpoint := g.Endpoint
			if endpoint == "" {
				endpoint = "Unknown"
			}

			p := resp.ByProvider[provider]
			p.Provider = provider
			p.Requests += g.Requests
			p.PromptTokens += g.PromptTokens
			p.CompletionTokens += g.CompletionTokens
			p.TotalTokens += g.TotalTokens
			p.Cost += g.Cost
			if g.LastUsed > p.LastUsed {
				p.LastUsed = g.LastUsed
			}
			resp.ByProvider[provider] = p

			modelKey := rawModel + "|" + provider
			m := resp.ByModel[modelKey]
			m.RawModel = rawModel
			m.Provider = provider
			m.Requests += g.Requests
			m.PromptTokens += g.PromptTokens
			m.CompletionTokens += g.CompletionTokens
			m.TotalTokens += g.TotalTokens
			m.Cost += g.Cost
			if g.LastUsed > m.LastUsed {
				m.LastUsed = g.LastUsed
			}
			resp.ByModel[modelKey] = m

			if g.ConnectionID != "" {
				accountKey := g.ConnectionID + "|" + rawModel + "|" + provider
				a := resp.ByAccount[accountKey]
				if a.ConnectionID == nil {
					id := g.ConnectionID
					a.ConnectionID = &id
					if name, ok := connMap[g.ConnectionID]; ok {
						a.AccountName = &name
					}
					a.RawModel = rawModel
					a.Provider = provider
				}
				a.Requests += g.Requests
				a.PromptTokens += g.PromptTokens
				a.CompletionTokens += g.CompletionTokens
				a.TotalTokens += g.TotalTokens
				a.Cost += g.Cost
				if g.LastUsed > a.LastUsed {
					a.LastUsed = g.LastUsed
				}
				resp.ByAccount[accountKey] = a
			}

			// AxonRouter groups every row here: a row with neither a key id
			// nor a key name lands in the "Unknown Key" bucket.
			keyIdentity := maskAPIKeyForStats(g.APIKey)
			if keyIdentity == "" {
				keyIdentity = "Unknown Key"
			}
			keyName := keyNames[g.APIKey]
			if keyName == "" {
				keyName = keyIdentity
			}
			apiKeyKey := keyIdentity + "|" + rawModel + "|" + provider
			k := resp.ByAPIKey[apiKeyKey]
			k.KeyName = keyName
			k.RawModel = rawModel
			k.Provider = provider
			k.Requests += g.Requests
			k.PromptTokens += g.PromptTokens
			k.CompletionTokens += g.CompletionTokens
			k.TotalTokens += g.TotalTokens
			k.Cost += g.Cost
			if g.LastUsed > k.LastUsed {
				k.LastUsed = g.LastUsed
			}
			resp.ByAPIKey[apiKeyKey] = k

			endpointKey := endpoint + "|" + rawModel + "|" + provider
			e := resp.ByEndpoint[endpointKey]
			e.Endpoint = endpoint
			e.RawModel = rawModel
			e.Provider = provider
			e.Requests += g.Requests
			e.PromptTokens += g.PromptTokens
			e.CompletionTokens += g.CompletionTokens
			e.TotalTokens += g.TotalTokens
			e.Cost += g.Cost
			if g.LastUsed > e.LastUsed {
				e.LastUsed = g.LastUsed
			}
			resp.ByEndpoint[endpointKey] = e
		}

		buckets, err := historyLast10Buckets(repo, now)
		if err != nil {
			fail()
			return
		}
		resp.Last10Minutes = buckets

		state := usagetracker.GetTracker().GetActiveState(repo)
		resp.Pending = state.Pending
		resp.ActiveRequests = axonActiveRequests(state.ActiveRequests)
		resp.ErrorProvider = state.ErrorProvider

		recent, err := axonRecentRequests(repo, 20)
		if err != nil {
			fail()
			return
		}
		resp.RecentRequests = recent

		handlerutil.WriteJSON(w, http.StatusOK, resp)
	}
}

// historyLast10Buckets folds the last ten minutes of history into
// per-minute counters, oldest first — AxonRouter's last10Minutes for the
// stats payload (ten full buckets; the 30-bucket {timestamp, requests}
// series /api/usage/stats serves is a different field of a different route).
func historyLast10Buckets(repo *db.Repo, now time.Time) ([]historyMinuteBucket, error) {
	end := now.UTC().Truncate(time.Minute)
	start := end.Add(-9 * time.Minute)
	rows, err := repo.GetUsageHistorySince(start.Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return nil, err
	}
	buckets := make([]historyMinuteBucket, 10)
	for _, row := range rows {
		ts, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil || ts.After(now) {
			continue
		}
		idx := int(ts.UTC().Truncate(time.Minute).Sub(start) / time.Minute)
		if idx < 0 || idx >= len(buckets) {
			continue
		}
		b := &buckets[idx]
		b.Requests++
		b.PromptTokens += int64(row.PromptTokens)
		b.CompletionTokens += int64(row.CompletionTokens)
		b.TotalTokens += int64(row.PromptTokens) + int64(row.CompletionTokens)
		b.Cost += row.Cost
	}
	return buckets, nil
}
