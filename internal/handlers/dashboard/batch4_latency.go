package dashboard

import (
	"net/http"
	"strconv"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/routinglatency"
)

// HandleGetRoutingLatency handles GET /api/routing/latency — port of
// AxonRouter's route.ts over src/lib/routingLatency.ts: percentile
// summary of the gateway's own routing times over a sliding window
// (?windowMs=, default 5 minutes), never cached.
func (h *DashboardHandler) HandleGetRoutingLatency(w http.ResponseWriter, r *http.Request) {
	var windowMs int64
	if raw := r.URL.Query().Get("windowMs"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			windowMs = n
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	handlerutil.WriteJSON(w, http.StatusOK, routinglatency.Summary(windowMs))
}
