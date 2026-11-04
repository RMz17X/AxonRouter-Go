package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// Hidden combos — port of AxonRouter's /api/combos/hidden
// (src/app/api/combos/hidden/route.js). The hidden list lives in the kv
// table (see Repo.GetHiddenComboNames); hiding takes effect on the model
// catalog wherever the list is consulted.

// HandleGetHiddenCombos handles GET /api/combos/hidden.
// Returns the combo names currently hidden from /v1/models.
func (h *DashboardHandler) HandleGetHiddenCombos(w http.ResponseWriter, r *http.Request) {
	hidden, err := h.Repo.GetHiddenComboNames()
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "Failed to fetch hidden combos")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"hidden": hidden})
}

// HandleHideCombos handles POST /api/combos/hidden with body
// { names: ["fast-free", ...] }. Idempotent.
func (h *DashboardHandler) HandleHideCombos(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	defer r.Body.Close()

	var body struct {
		Names []any `json:"names"`
	}
	// Axon parses the body leniently (a parse failure reads as {}), so an
	// unreadable body lands on the same "names[] required" refusal as an
	// empty one rather than a distinct parse error.
	_ = json.Unmarshal(raw, &body)

	names := make([]string, 0, len(body.Names))
	for _, entry := range body.Names {
		if name, ok := entry.(string); ok && strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		writePlainError(w, http.StatusBadRequest, "names[] required")
		return
	}
	if err := h.Repo.HideCombos(names); err != nil {
		writePlainError(w, http.StatusInternalServerError, "Failed to hide combos")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// HandleUnhideCombos handles DELETE /api/combos/hidden?name=xxx.
// Without a name, every hidden combo is shown again.
func (h *DashboardHandler) HandleUnhideCombos(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	names := []string{}
	if name != "" {
		names = []string{name}
	}
	if err := h.Repo.UnhideCombos(names); err != nil {
		writePlainError(w, http.StatusInternalServerError, "Failed to unhide combos")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
