package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlers/chat"
	"axonrouter/proxy/internal/handlerutil"
)

// Personas: a label, one preset from the library, and the set of model
// ids / combo names that preset is bound to. A bound name wears its
// persona wherever it serves; a name wears at most one persona. These
// handlers own the CRUD plus that discipline: the preset must exist,
// every bound name must be a listed model or combo, and no bound name
// may already belong to another persona. The persona's own name is
// only a label — it is not requestable and shares no namespace with
// models, combos, or aliases.

// validPersonaName keeps the label URL- and shell-friendly; unlike the
// retired virtual-model name it never reaches the serving path.
var validPersonaName = regexp.MustCompile(`^[a-zA-Z0-9_.\-]+$`)

// HandleListPersonas returns every persona definition, ordered by name.
// GET /api/jailbreak/personas.
func (h *DashboardHandler) HandleListPersonas(w http.ResponseWriter, r *http.Request) {
	personas, err := h.Repo.ListPersonas()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if personas == nil {
		personas = []*db.PersonaDef{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"personas": personas})
}

// personaCatalog snapshots every id the catalog publishes — real
// models and combos — the set a binding may point at.
func (h *DashboardHandler) personaCatalog(r *http.Request) (map[string]bool, error) {
	catalog := map[string]bool{}
	chatH := chat.NewChatHandler(h.Repo, nil)
	for _, m := range chatH.BuildModelsListResult(r.Context(), chat.ListModeCatalog).Models {
		if m.ID != "" {
			catalog[m.ID] = true
		}
	}
	return catalog, nil
}

// HandleSavePersona creates or replaces one persona definition.
// PUT /api/jailbreak/personas with {"name","presetId","models"}; the
// name is the identity, so a rename is a delete plus a create.
func (h *DashboardHandler) HandleSavePersona(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req db.PersonaDef
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.PresetID = strings.TrimSpace(req.PresetID)
	if req.Name == "" || len(req.Name) > 300 || !validPersonaName.MatchString(req.Name) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Persona name can only contain letters, numbers, -, _ and .")
		return
	}
	if req.PresetID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Persona needs a preset")
		return
	}
	// Normalize the binding set: trimmed, non-empty, de-duplicated.
	bound := make([]string, 0, len(req.Models))
	seenBound := map[string]bool{}
	for _, m := range req.Models {
		m = strings.TrimSpace(m)
		if m == "" || seenBound[m] {
			continue
		}
		seenBound[m] = true
		bound = append(bound, m)
	}
	if len(bound) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Persona needs at least one bound model or combo")
		return
	}
	req.Models = bound
	req.Target = ""

	catalog, err := h.personaCatalog(r)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, m := range bound {
		if len(m) > 300 || !catalog[m] {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Bound name is not a known model or combo: "+m)
			return
		}
	}
	// One persona per bound name: a name another persona already wears
	// must be unbound there first. This persona's own current bindings
	// are fine — a save replaces its whole definition.
	personas, err := h.Repo.ListPersonas()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range personas {
		if p == nil || p.Name == req.Name {
			continue
		}
		for _, other := range p.BoundNames() {
			if seenBound[other] {
				handlerutil.WriteJSONError(w, http.StatusBadRequest, other+" is already bound to persona "+p.Name)
				return
			}
		}
	}
	presets, err := h.Repo.ListJailbreakPresets()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	presetKnown := false
	for _, p := range presets {
		if p != nil && p.ID == req.PresetID {
			presetKnown = true
			break
		}
	}
	if !presetKnown {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Preset not found")
		return
	}

	if err := h.Repo.SavePersona(&req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true, "persona": &req})
}

// HandleDeletePersona removes one persona definition.
// DELETE /api/jailbreak/personas with {"name":"..."} — the name travels
// in the body, the same shape the retired binding delete used, so no
// name ever needs URL-escaping.
func (h *DashboardHandler) HandleDeletePersona(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 300 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid persona name")
		return
	}
	if err := h.Repo.DeletePersona(req.Name); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
