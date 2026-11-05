package handlers

import (
	"net/http"
	"time"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// ComboAnalyticsMember is one resolved member of a combo over the window: the
// model a combo name resolved to on one provider, with its outcome counts.
// The shape mirrors AxonRouter's GET /api/combos/analytics members exactly —
// the Usage Overview's Combo Routing table renders it unchanged.
type ComboAnalyticsMember struct {
	ComboName   string  `json:"comboName"`
	Model       string  `json:"model"`
	Provider    string  `json:"provider"`
	Total       int     `json:"total"`
	Success     int     `json:"success"`
	Errors      int     `json:"errors"`
	AvgLatencyS float64 `json:"avgLatencyS"`
	LastSeen    string  `json:"lastSeen"`
}

// combosAnalyticsResponse is the endpoint payload. Combos is part of the
// AxonRouter contract (its client fetches it alongside the members) but no
// surface renders it, so it is always the empty array rather than an invented
// aggregate. Period and RoutingMetrics complete Axon's envelope
// ({period, routingMetrics, ...result}): Go has no routing-metrics
// service, so the latter is null — the same value Axon itself emits
// when its metrics read fails.
type combosAnalyticsResponse struct {
	Period         string                 `json:"period"`
	RoutingMetrics any                    `json:"routingMetrics"`
	Members        []ComboAnalyticsMember `json:"members"`
	Combos         []any                  `json:"combos"`
}

// HandleCombosAnalytics serves GET /api/combos/analytics?period= — per-member
// combo routing aggregates from the request-details ledger. The window comes
// from detailsSince, the same resolver the Details tab uses, so a period means
// the same span on both surfaces; like the details list, an absent period (or
// `all`) reads the whole ledger.
func HandleCombosAnalytics(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		period := r.URL.Query().Get("period")
		since := detailsSince(period, time.Now())
		rows, err := repo.GetComboAnalyticsSince(r.Context(), since)
		if err != nil {
			// Axon's flat shape for this route, including its details key.
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "Failed to fetch combo analytics",
				"details": err.Error(),
			})
			return
		}
		if period == "" {
			period = "today"
		}

		members := make([]ComboAnalyticsMember, 0, len(rows))
		for _, row := range rows {
			members = append(members, ComboAnalyticsMember{
				ComboName:   row.ComboName,
				Model:       row.Model,
				Provider:    row.Provider,
				Total:       row.Total,
				Success:     row.Success,
				Errors:      row.Total - row.Success,
				AvgLatencyS: row.AvgLatencyS,
				LastSeen:    row.LastSeen,
			})
		}

		handlerutil.WriteJSON(w, http.StatusOK, combosAnalyticsResponse{
			Period:         period,
			RoutingMetrics: nil,
			Members:        members,
			Combos:         []any{},
		})
	}
}
