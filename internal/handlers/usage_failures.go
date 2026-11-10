package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"strconv"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/detailstore"
	"axonrouter/proxy/internal/handlerutil"
)

type failureGroupPoint struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	FailureCount     int    `json:"failureCount"`
	LatestFailedAt   string `json:"latestFailedAt"`
	SampleError      string `json:"sampleError"`
	SampleStatusCode string `json:"sampleStatusCode"`
}

type failureProviderPoint struct {
	Provider         string `json:"provider"`
	FailureCount     int    `json:"failureCount"`
	LatestFailedAt   string `json:"latestFailedAt"`
	SampleError      string `json:"sampleError"`
	SampleStatusCode string `json:"sampleStatusCode"`
}

type failureAnalyticsResponse struct {
	TopModels      []failureGroupPoint    `json:"topModels"`
	TopProviders   []failureProviderPoint `json:"topProviders"`
	RecentFailures []any                  `json:"recentFailures"`
}

// HandleUsageAnalyticsFailures serves GET /api/usage/analytics/failures —
// AxonRouter's failure leaderboards over the request-details ledger:
// the 25 hardest-hit (provider, model) pairs and providers, each with the
// newest failure's sample error and status code, plus the newest failed
// requests' full detail payloads.
//
// AxonRouter's repository swallows every read error into empty sections,
// so this endpoint answers 200 with empty lists rather than a 500 — the
// same degradation is applied here, including for an unparseable time
// bound (its Date parsing throws inside the same guarded block).
func HandleUsageAnalyticsFailures(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 20
		if raw := q.Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				limit = n
			}
		}
		if limit < 1 {
			limit = 1
		}
		if limit > 100 {
			limit = 100
		}

		resp := failureAnalyticsResponse{
			TopModels:      make([]failureGroupPoint, 0),
			TopProviders:   make([]failureProviderPoint, 0),
			RecentFailures: make([]any, 0),
		}

		filter := db.FailureFilter{
			Provider: q.Get("provider"),
			Model:    q.Get("model"),
		}
		valid := true
		if raw := q.Get("timeFrom"); raw != "" {
			if t, ok := parseAnalyticsDate(raw); ok {
				filter.From = t.UTC().Format("2006-01-02T15:04:05.000Z")
			} else {
				valid = false
			}
		}
		if raw := q.Get("timeTo"); raw != "" {
			if t, ok := parseAnalyticsDate(raw); ok {
				filter.To = t.UTC().Format("2006-01-02T15:04:05.000Z")
			} else {
				valid = false
			}
		}

		if valid {
			if rows, err := detailstore.FailureTopModels(filter); err == nil {
				for _, row := range rows {
					resp.TopModels = append(resp.TopModels, failureGroupPoint{
						Provider:         row.Provider,
						Model:            row.Model,
						FailureCount:     row.FailureCount,
						LatestFailedAt:   row.LatestFailedAt,
						SampleError:      row.SampleError,
						SampleStatusCode: row.SampleStatusCode,
					})
				}
			}
			if rows, err := detailstore.FailureTopProviders(filter); err == nil {
				for _, row := range rows {
					resp.TopProviders = append(resp.TopProviders, failureProviderPoint{
						Provider:         row.Provider,
						FailureCount:     row.FailureCount,
						LatestFailedAt:   row.LatestFailedAt,
						SampleError:      row.SampleError,
						SampleStatusCode: row.SampleStatusCode,
					})
				}
			}
			if payloads, err := detailstore.RecentFailurePayloads(filter, limit); err == nil {
				for _, raw := range payloads {
					// AxonRouter's rowToDetail is the stored payload itself,
					// parsed; an unreadable payload reads as an empty object.
					var detail any
					if err := json.Unmarshal([]byte(raw), &detail); err != nil || detail == nil {
						detail = map[string]any{}
					}
					resp.RecentFailures = append(resp.RecentFailures, detail)
				}
			}
		}

		handlerutil.WriteJSON(w, http.StatusOK, resp)
	}
}
