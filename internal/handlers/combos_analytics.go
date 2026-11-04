package handlers

import (
	"net/http"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
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
// aggregate.
type combosAnalyticsResponse struct {
	Members []ComboAnalyticsMember `json:"members"`
	Combos  []any                  `json:"combos"`
}

// HandleCombosAnalytics serves GET /api/combos/analytics?period= — per-member
// combo routing aggregates from the request-details ledger. The window comes
// from detailsSince, the same resolver the Details tab uses, so a period means
// the same span on both surfaces; like the details list, an absent period (or
// `all`) reads the whole ledger.
func HandleCombosAnalytics(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since := detailsSince(r.URL.Query().Get("period"), time.Now())
		rows, err := repo.GetComboAnalyticsSince(r.Context(), since)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
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
			Members: members,
			Combos:  []any{},
		})
	}
}
