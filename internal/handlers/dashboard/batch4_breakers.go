package dashboard

import (
	"net/http"
	"sort"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/resilience"
)

// Circuit breakers — port of AxonRouter's src/app/api/circuit-breakers/
// route.ts (GET + DELETE) over the gateway's live breaker registry
// (open-sse/services/circuitBreaker.ts upstream; internal/resilience
// here). Upstream keys breakers per connection; this gateway's breakers
// are per provider, so the Axon `connectionId` field carries the Go
// breaker key — the registry the request path actually consults, not a
// second estimate.

// breakerStateName maps the Go breaker states onto upstream's vocabulary
// (closed | open | half-open). Go's DEGRADED state passes every request,
// which is upstream's closed behaviour, so it reports as "closed".
func breakerStateName(s resilience.State) string {
	switch s {
	case resilience.StateOpen:
		return "open"
	case resilience.StateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

func epochMsOrNil(rfc3339 string) any {
	if rfc3339 == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return nil
	}
	return t.UnixMilli()
}

// HandleGetCircuitBreakers handles GET /api/circuit-breakers.
func (h *DashboardHandler) HandleGetCircuitBreakers(w http.ResponseWriter, r *http.Request) {
	statuses := resilience.AllBreakerStatuses()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	breakers := make([]any, 0, len(statuses))
	for _, st := range statuses {
		breakers = append(breakers, map[string]any{
			"connectionId":  st.Name,
			"state":         breakerStateName(st.State),
			"failureCount":  st.FailureCount,
			"lastFailureAt": epochMsOrNil(st.LastFailureAt),
			"openedAt":      epochMsOrNil(st.OpenedAt),
			"retryAfterMs":  st.RetryAfterMs,
		})
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"breakers": breakers})
}

// HandleResetCircuitBreakers handles DELETE /api/circuit-breakers.
func (h *DashboardHandler) HandleResetCircuitBreakers(w http.ResponseWriter, r *http.Request) {
	resilience.ResetAllBreakers()
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
