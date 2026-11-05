package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/combos/smart"
	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/models"
)

// Combo reorder — port of AxonRouter's POST /api/combos/reorder
// (src/app/api/combos/reorder/route.ts). The order persists in the kv table
// and GetCombos applies it (see Repo.ReorderCombos), so the response below
// is the same list the dashboard and /v1/models read afterwards.

// HandleReorderCombos handles POST /api/combos/reorder with body
// { comboIds: ["...", ...] } and answers { combos } in the new order.
func (h *DashboardHandler) HandleReorderCombos(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, "Failed to reorder combos")
		return
	}
	defer r.Body.Close()

	var body struct {
		ComboIDs []any `json:"comboIds"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		// Axon's handler funnels a body parse failure into its catch-all 500;
		// the JS parser's message is engine-specific, so the port answers
		// with the route's fallback text instead of inventing one.
		writePlainError(w, http.StatusInternalServerError, "Failed to reorder combos")
		return
	}

	ids, detail := validateComboIDs(body.ComboIDs)
	if detail != nil {
		writeValidationError(w, detail)
		return
	}

	combos, err := h.Repo.ReorderCombos(ids)
	if err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if combos == nil {
		combos = []*models.Combo{}
	}
	if err := h.enrichComboRules(r.Context(), combos); err != nil {
		writePlainError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"combos": combos})
}

// validateComboIDs applies Axon's reorderCombosSchema (an array of at least
// one non-empty string) and returns the first violation in Axon's
// validation-error shape, or the trimmed ids when the body is valid. Zod
// trims each id before checking its length, so the stored order carries the
// trimmed form too.
func validateComboIDs(raw []any) ([]string, map[string]any) {
	fail := func(field, message string) map[string]any {
		return map[string]any{
			"message": "Validation failed",
			"details": []map[string]string{{"field": field, "message": message}},
		}
	}
	if len(raw) == 0 {
		return nil, fail("comboIds", "comboIds must contain at least one combo id")
	}
	ids := make([]string, 0, len(raw))
	for i, entry := range raw {
		value, ok := entry.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fail(fmt.Sprintf("comboIds.%d", i), "combo id must be a non-empty string")
		}
		ids = append(ids, strings.TrimSpace(value))
	}
	return ids, nil
}

// writeValidationError answers with Axon's validation failure body:
// { error: { message: "Validation failed", details: [...] } }.
func writeValidationError(w http.ResponseWriter, detail map[string]any) {
	handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": detail})
}

// enrichComboRules fills MatchRules and AutoMembers on a combo list, the
// same enrichment HandleGetCombos performs, so a reorder response is shaped
// exactly like the list it replaces.
func (h *DashboardHandler) enrichComboRules(ctx context.Context, combos []*models.Combo) error {
	rulesByID, err := h.Repo.ComboRulesByID(ctx)
	if err != nil {
		return err
	}
	for _, combo := range combos {
		raw, ok := rulesByID[combo.ID]
		if !ok {
			continue
		}
		combo.MatchRules = raw
		rules, err := smart.ParseRules(raw)
		if err != nil {
			log.Warn("smart-combo", "stored rules unreadable", "combo", combo.Name, "error", err)
			continue
		}
		combo.AutoMembers = smart.AutoMembers(rules)
	}
	return nil
}
