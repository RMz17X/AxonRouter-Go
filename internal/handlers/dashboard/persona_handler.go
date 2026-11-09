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

// Persona virtual models: named prompts on names of their own. A persona
// mints a NEW model name that routes to one target — a model id or a
// combo name — and always injects one preset from the library. These
// handlers own the CRUD plus the namespace discipline: the name must be
// unique across the whole model catalog (real models and combos), the
// model aliases, and the other personas, and the target may not be a
// persona, so resolution stays a single hop.

// validPersonaName mirrors the combo-name rule minus "/": a persona name
// is resolved on the bare-name path, where a slash would be read as a
// provider/model pair and the persona could never be reached.
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

// personaNamespace snapshots the three namespaces a persona name or
// target is checked against: every id the catalog publishes (real
// models, combos, and the personas themselves) and the persona names.
func (h *DashboardHandler) personaNamespace(r *http.Request) (catalog map[string]bool, personaNames map[string]bool, err error) {
	catalog = map[string]bool{}
	chatH := chat.NewChatHandler(h.Repo, nil)
	for _, m := range chatH.BuildModelsListResult(r.Context(), chat.ListModeCatalog).Models {
		if m.ID != "" {
			catalog[m.ID] = true
		}
	}
	personaNames = map[string]bool{}
	personas, err := h.Repo.ListPersonas()
	if err != nil {
		return nil, nil, err
	}
	for _, p := range personas {
		if p != nil && p.Name != "" {
			personaNames[p.Name] = true
		}
	}
	return catalog, personaNames, nil
}

// HandleSavePersona creates or replaces one persona definition.
// PUT /api/jailbreak/personas with {"name","target","presetId"}; the
// name is the identity, so a rename is a delete plus a create.
func (h *DashboardHandler) HandleSavePersona(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
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
	req.Target = strings.TrimSpace(req.Target)
	req.PresetID = strings.TrimSpace(req.PresetID)
	if req.Name == "" || len(req.Name) > 300 || !validPersonaName.MatchString(req.Name) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Persona name can only contain letters, numbers, -, _ and .")
		return
	}
	if req.Target == "" || len(req.Target) > 300 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Persona needs a target model or combo")
		return
	}
	if req.PresetID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Persona needs a preset")
		return
	}

	catalog, personaNames, err := h.personaNamespace(r)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The name must be free, or already this persona's own (an update).
	if catalog[req.Name] && !personaNames[req.Name] {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "That name is already used by a model or combo")
		return
	}
	if alias, err := h.Repo.GetModelAlias(req.Name); err == nil && alias != "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "That name is already used by a model alias")
		return
	}
	// The target must be a listed model or combo, never another persona
	// (resolution is one hop by design) and never the persona itself.
	if personaNames[req.Target] || req.Target == req.Name {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "A persona cannot target another persona")
		return
	}
	if !catalog[req.Target] {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Target is not a known model or combo")
		return
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
