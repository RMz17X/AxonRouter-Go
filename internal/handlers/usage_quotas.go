package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"sort"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// usageQuotaItem is one entry of GET /api/usage/quotas: a connection's
// stored quota snapshot joined with its routing fields, AxonRouter's
// getBatchProviderQuotas row shape.
type usageQuotaItem struct {
	ConnectionID         string         `json:"connectionId"`
	Provider             string         `json:"provider"`
	Plan                 any            `json:"plan"`
	Quotas               map[string]any `json:"quotas"`
	RateLimits           any            `json:"rateLimits"`
	RemainingPct         *float64       `json:"remainingPct"`
	RawDosage            any            `json:"rawDosage"`
	ResetAt              any            `json:"resetAt"`
	UpdatedAt            string         `json:"updatedAt"`
	Name                 *string        `json:"name"`
	Email                *string        `json:"email"`
	Priority             *int           `json:"priority"`
	IsActive             bool           `json:"isActive"`
	TestStatus           string         `json:"testStatus"`
	LockedAllUntil       any            `json:"lockedAllUntil"`
	ProviderSpecificData map[string]any `json:"providerSpecificData"`
}

// HandleUsageQuotas serves GET /api/usage/quotas?provider= — the batch quota
// read the dashboard's quota tracker polls, one entry per connection of the
// provider that has a quota snapshot.
//
// AxonRouter persists snapshots in a dedicated table its quota checker
// writes; this build's equivalent record is the quotas object stored on the
// connection's data payload (the chat layer's rotation reads the same
// object), so connections without one have no snapshot and are excluded —
// the same inner join. Two AxonRouter fields have no recorded counterpart
// here and are emitted null rather than invented: rawDosage, and
// lockedAllUntil (lock state lives under rateLimitedUntil/modelLock_* data
// keys, a different mechanism). AxonRouter's cache-refresh and auto-heal
// side effects are runtime state writes, not part of the read, and are not
// ported.
func HandleUsageQuotas(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.URL.Query().Get("provider")
		if provider == "" {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "provider query parameter is required"})
			return
		}

		conns, err := repo.GetProviderConnections(provider, false)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch provider quotas"})
			return
		}

		items := make([]usageQuotaItem, 0, len(conns))
		for _, conn := range conns {
			var data map[string]any
			if conn.Data != "" {
				_ = json.Unmarshal([]byte(conn.Data), &data)
			}
			quotas, ok := data["quotas"].(map[string]any)
			if !ok {
				continue
			}

			item := usageQuotaItem{
				ConnectionID: conn.ID,
				Provider:     conn.Provider,
				Quotas:       quotas,
				UpdatedAt:    conn.UpdatedAt,
				Name:         conn.Name,
				Email:        conn.Email,
				Priority:     conn.Priority,
				IsActive:     conn.IsActive == 1,
				TestStatus:   "active",
			}
			if plan, ok := data["plan"].(string); ok && plan != "" {
				item.Plan = plan
			}
			if rateLimits, ok := data["rateLimits"]; ok {
				item.RateLimits = rateLimits
			}
			if status, ok := data["testStatus"].(string); ok && status != "" {
				item.TestStatus = status
			}
			if psd, ok := data["providerSpecificData"].(map[string]any); ok {
				item.ProviderSpecificData = psd
			} else {
				item.ProviderSpecificData = map[string]any{}
			}

			// The snapshot's remaining percentage and reset are the binding
			// (lowest) window's, the values AxonRouter's snapshot writer
			// stores denormalized on its rows.
			for _, windowVal := range quotas {
				window, ok := windowVal.(map[string]any)
				if !ok {
					continue
				}
				remaining, ok := window["remainingPercentage"].(float64)
				if !ok {
					continue
				}
				if item.RemainingPct == nil || remaining < *item.RemainingPct {
					pct := remaining
					item.RemainingPct = &pct
					item.ResetAt = nil
					if resetAt, ok := window["resetAt"].(string); ok && resetAt != "" {
						item.ResetAt = resetAt
					}
				}
			}

			items = append(items, item)
		}

		// AxonRouter's order: most exhausted first (nulls last), then
		// priority (nulls last), then freshest snapshot first.
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if (a.RemainingPct == nil) != (b.RemainingPct == nil) {
				return a.RemainingPct != nil
			}
			if a.RemainingPct != nil && b.RemainingPct != nil && *a.RemainingPct != *b.RemainingPct {
				return *a.RemainingPct < *b.RemainingPct
			}
			if (a.Priority == nil) != (b.Priority == nil) {
				return a.Priority != nil
			}
			if a.Priority != nil && b.Priority != nil && *a.Priority != *b.Priority {
				return *a.Priority < *b.Priority
			}
			return a.UpdatedAt > b.UpdatedAt
		})

		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"quotas": items})
	}
}
