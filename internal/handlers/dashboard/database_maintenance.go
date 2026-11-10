package dashboard

import (
	"net/http"
	"time"

	"axonrouter/proxy/internal/detailstore"
	"axonrouter/proxy/internal/handlerutil"
)

// HandleGetDatabaseStats handles GET /api/settings/database/stats.
func (h *DashboardHandler) HandleGetDatabaseStats(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}
	stats, err := h.Repo.GetDatabaseStats(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to get database stats: "+err.Error())
		return
	}
	// Request details live in the file store now; its count replaces
	// the table count the stats read used to take.
	stats.RequestDetailsCount = detailstore.Count()
	handlerutil.WriteJSON(w, http.StatusOK, stats)
}

// HandlePruneDatabase handles POST /api/settings/database/prune.
func (h *DashboardHandler) HandlePruneDatabase(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}
	retention := 7 * 24 * time.Hour
	maxRows := 0
	if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
		if settings.RetentionDays > 0 {
			retention = time.Duration(settings.RetentionDays) * 24 * time.Hour
		}
		if settings.MaxRequestRecords > 0 {
			maxRows = settings.MaxRequestRecords
		}
	}
	// The prune target is the file-backed request-details store; the
	// response keeps the old by-age/by-size split.
	byAge, bySize, err := detailstore.Prune(retention, maxRows)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to prune database: "+err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"pruned":  byAge + bySize,
		"byAge":   byAge,
		"bySize":  bySize,
	})
}

// HandleVacuumDatabase handles POST /api/settings/database/vacuum.
func (h *DashboardHandler) HandleVacuumDatabase(w http.ResponseWriter, r *http.Request) {
	if h.Repo == nil {
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "database repo unavailable")
		return
	}
	if err := h.Repo.VacuumDatabase(r.Context()); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to vacuum database: "+err.Error())
		return
	}
	stats, _ := h.Repo.GetDatabaseStats(r.Context())
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"databaseSizeBytes": stats.DatabaseSizeBytes,
		"reclaimableBytes":  stats.ReclaimableBytes,
	})
}
