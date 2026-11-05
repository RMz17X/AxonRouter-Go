package dashboard

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"axonrouter/proxy/internal/clisetup"
	"axonrouter/proxy/internal/db"
	"axonrouter/proxy/internal/handlerutil"
)

// OpenCode sync HTTP surface (batch 5). Ports of Axon's
// src/app/api/opencode/** routes:
//   - preferences GET/PATCH and bundle/preview and sync/tokens
//     GET/POST sit behind dashboard auth (Axon: requireManagementAuth)
//     and are registered in the dashboard group.
//   - sync/tokens/{id} PATCH/DELETE carry no management auth in the
//     .ts source — ported as-is, registered outside the group.
//   - sync/bundle and sync/version authenticate with the ocs_ sync
//     token itself (401 when no record matches).
//
// State lives in the kv table under scope "opencodeSync"
// (preferences / tokens keys), the Go counterpart of Axon's
// localDb opencodeSync domain.

const ocKVScope = "opencodeSync"

// OpenCodeSyncHandler serves the OpenCode sync routes.
type OpenCodeSyncHandler struct {
	repo   *db.Repo
	mu     sync.Mutex
	client *http.Client
}

// NewOpenCodeSyncHandler returns an OpenCodeSyncHandler.
func NewOpenCodeSyncHandler(repo *db.Repo) *OpenCodeSyncHandler {
	return &OpenCodeSyncHandler{
		repo: repo,
		client: &http.Client{Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
		}},
	}
}

// --- storage -----------------------------------------------------------

func (h *OpenCodeSyncHandler) loadPreferencesRaw() (any, error) {
	kv, err := h.repo.GetKVScope(ocKVScope)
	if err != nil {
		return nil, err
	}
	raw, ok := kv["preferences"]
	if !ok || raw == "" {
		return nil, nil
	}
	v, err := clisetup.ParseOrderedJSON([]byte(raw))
	if err != nil {
		return nil, nil
	}
	return v, nil
}

func (h *OpenCodeSyncHandler) savePreferences(prefs *clisetup.JObj) error {
	return h.repo.SetKV(ocKVScope, "preferences", clisetup.StringifyJSON(prefs))
}

func (h *OpenCodeSyncHandler) listTokens() ([]*clisetup.JObj, error) {
	kv, err := h.repo.GetKVScope(ocKVScope)
	if err != nil {
		return nil, err
	}
	raw, ok := kv["tokens"]
	if !ok || raw == "" {
		return []*clisetup.JObj{}, nil
	}
	v, err := clisetup.ParseOrderedJSON([]byte(raw))
	if err != nil {
		return []*clisetup.JObj{}, nil
	}
	arr, _ := v.([]any)
	out := []*clisetup.JObj{}
	for _, item := range arr {
		if rec, ok := ocPlain(item); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// mutateTokens applies fn to the stored token list under the handler
// mutex and persists the result — Axon's mutateOpenCodeTokens.
func (h *OpenCodeSyncHandler) mutateTokens(fn func([]*clisetup.JObj) []*clisetup.JObj) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, err := h.listTokens()
	if err != nil {
		return err
	}
	next := fn(current)
	arr := make([]any, len(next))
	for i, rec := range next {
		arr[i] = rec
	}
	return h.repo.SetKV(ocKVScope, "tokens", clisetup.StringifyJSON(arr))
}

func (h *OpenCodeSyncHandler) touchToken(id string) error {
	return h.mutateTokens(func(tokens []*clisetup.JObj) []*clisetup.JObj {
		for _, rec := range tokens {
			if rec.Get("id") == id {
				now := ocNowISO()
				rec.Set("lastUsedAt", now)
				rec.Set("updatedAt", now)
			}
		}
		return tokens
	})
}

// --- shared response helpers -------------------------------------------

func ocError(w http.ResponseWriter, status int, msg string) {
	handlerutil.WriteJSON(w, status, map[string]any{"error": msg})
}

var (
	ocInvalidPrefixRE = regexp.MustCompile(`^Invalid\b`)
	ocOnlyValidRE     = regexp.MustCompile(`only valid`)
	ocRequiredRE      = regexp.MustCompile(`required`)
	ocCannotUpdateRE  = regexp.MustCompile(`cannot be updated`)
)

// --- preferences ---------------------------------------------------------

func (h *OpenCodeSyncHandler) GetPreferences(w http.ResponseWriter, r *http.Request) {
	raw, err := h.loadPreferencesRaw()
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to load OpenCode preferences")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, clisetup.NewJObj(
		"preferences", ocSanitizePreferences(raw),
	))
}

func (h *OpenCodeSyncHandler) PatchPreferences(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode preferences")
		return
	}
	payload, parseErr := clisetup.ParseOrderedJSON(body)
	if parseErr != nil {
		// Axon treats a body SyntaxError as a validation failure.
		ocError(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	payloadObj, isObj := ocPlain(payload)
	if !isObj {
		ocError(w, http.StatusBadRequest, "Invalid preferences payload")
		return
	}

	current, err := h.loadPreferencesRaw()
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode preferences")
		return
	}
	merged := clisetup.NewJObj()
	if curObj, ok := ocPlain(current); ok {
		for _, k := range curObj.Keys() {
			merged.Set(k, curObj.Get(k))
		}
	}
	for _, k := range payloadObj.Keys() {
		merged.Set(k, payloadObj.Get(k))
	}

	validated, err := ocValidatePreferences(merged)
	if err != nil {
		if ocIsValidationError(err) || ocInvalidPrefixRE.MatchString(err.Error()) || ocOnlyValidRE.MatchString(err.Error()) {
			ocError(w, http.StatusBadRequest, err.Error())
			return
		}
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode preferences")
		return
	}
	validated.Set("updatedAt", ocNowISO())
	if err := h.savePreferences(validated); err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode preferences")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, clisetup.NewJObj(
		"preferences", ocSanitizePreferences(validated),
	))
}

// --- bundle preview ------------------------------------------------------

func (h *OpenCodeSyncHandler) GetBundlePreview(w http.ResponseWriter, r *http.Request) {
	raw, err := h.loadPreferencesRaw()
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to generate OpenCode bundle preview")
		return
	}
	catalog := ocLoadCatalog(h.repo, h.client)
	bundle, err := ocBuildBundle(raw, catalog)
	if err != nil {
		if ocIsValidationError(err) {
			ocError(w, http.StatusBadRequest, err.Error())
			return
		}
		ocError(w, http.StatusInternalServerError, "Failed to generate OpenCode bundle preview")
		return
	}
	resp := ocPublicResponse(bundle)
	resp.Set("catalogModels", ocCatalogModels(catalog))
	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// --- tokens (management) ---------------------------------------------------

func (h *OpenCodeSyncHandler) GetTokens(w http.ResponseWriter, r *http.Request) {
	records, err := h.listTokens()
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to load OpenCode sync tokens")
		return
	}
	out := []any{}
	for _, rec := range records {
		if pub := ocPublicTokenRecord(rec); pub != nil {
			out = append(out, pub)
		}
	}
	handlerutil.WriteJSON(w, http.StatusOK, clisetup.NewJObj("tokens", out))
}

func (h *OpenCodeSyncHandler) PostTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to create OpenCode sync token")
		return
	}
	payload, parseErr := clisetup.ParseOrderedJSON(body)
	if parseErr != nil {
		ocError(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	if payload == nil {
		// JS destructuring a null payload throws a TypeError, which
		// the route does not classify as validation → 500.
		ocError(w, http.StatusInternalServerError, "Failed to create OpenCode sync token")
		return
	}
	var name, metadata any
	if obj, ok := ocPlain(payload); ok {
		name = obj.Get("name")
		metadata = obj.Get("metadata")
	}
	token, record, err := ocCreateSyncToken(name, metadata)
	if err != nil {
		if ocInvalidPrefixRE.MatchString(err.Error()) || ocRequiredRE.MatchString(err.Error()) {
			ocError(w, http.StatusBadRequest, err.Error())
			return
		}
		ocError(w, http.StatusInternalServerError, "Failed to create OpenCode sync token")
		return
	}
	if err := h.mutateTokens(func(tokens []*clisetup.JObj) []*clisetup.JObj {
		return append(tokens, record)
	}); err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to create OpenCode sync token")
		return
	}
	handlerutil.WriteJSON(w, http.StatusCreated, clisetup.NewJObj(
		"token", token,
		"record", ocPublicTokenRecord(record),
	))
}

// --- tokens/{id} (no management auth in the .ts source) ---------------------

func (h *OpenCodeSyncHandler) PatchToken(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		ocError(w, http.StatusBadRequest, "Token id is required")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode sync token")
		return
	}
	payload, parseErr := clisetup.ParseOrderedJSON(body)
	if parseErr != nil {
		ocError(w, http.StatusBadRequest, parseErr.Error())
		return
	}
	updates, err := ocNormalizeTokenPatch(payload)
	if err != nil {
		if ocInvalidPrefixRE.MatchString(err.Error()) || ocRequiredRE.MatchString(err.Error()) || ocCannotUpdateRE.MatchString(err.Error()) {
			ocError(w, http.StatusBadRequest, err.Error())
			return
		}
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode sync token")
		return
	}

	var next *clisetup.JObj
	mutErr := h.mutateTokens(func(tokens []*clisetup.JObj) []*clisetup.JObj {
		for i, rec := range tokens {
			if rec.Get("id") == id {
				merged := clisetup.NewJObj()
				for _, k := range rec.Keys() {
					merged.Set(k, rec.Get(k))
				}
				for _, k := range updates.Keys() {
					merged.Set(k, updates.Get(k))
				}
				merged.Set("updatedAt", ocNowISO())
				tokens[i] = merged
				next = merged
			}
		}
		return tokens
	})
	if mutErr != nil {
		ocError(w, http.StatusInternalServerError, "Failed to update OpenCode sync token")
		return
	}
	if next == nil {
		ocError(w, http.StatusNotFound, "Token not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, clisetup.NewJObj(
		"record", ocPublicTokenRecord(next),
	))
}

func (h *OpenCodeSyncHandler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		ocError(w, http.StatusBadRequest, "Token id is required")
		return
	}
	removed := false
	err := h.mutateTokens(func(tokens []*clisetup.JObj) []*clisetup.JObj {
		kept := []*clisetup.JObj{}
		for _, rec := range tokens {
			if rec.Get("id") == id {
				removed = true
				continue
			}
			kept = append(kept, rec)
		}
		return kept
	})
	if err != nil {
		ocError(w, http.StatusInternalServerError, "Failed to delete OpenCode sync token")
		return
	}
	if !removed {
		ocError(w, http.StatusNotFound, "Token not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// --- sync bundle / version (ocs token auth) ---------------------------------

// authenticatedBundle resolves the caller's sync token, builds the
// bundle, and touches the token's lastUsedAt (failures ignored, as in
// Axon). Returns nil bundle with the HTTP class already decided:
// status 401 for no match, 400 validation, 500 other.
func (h *OpenCodeSyncHandler) authenticatedBundle(r *http.Request) (*ocBundleResult, int, string) {
	records, err := h.listTokens()
	if err != nil {
		return nil, http.StatusInternalServerError, ""
	}
	record := ocFindMatchingToken(records, r.Header.Get("Authorization"))
	if record == nil {
		return nil, http.StatusUnauthorized, ""
	}
	raw, err := h.loadPreferencesRaw()
	if err != nil {
		return nil, http.StatusInternalServerError, ""
	}
	catalog := ocLoadCatalog(h.repo, h.client)
	bundle, err := ocBuildBundle(raw, catalog)
	if err != nil {
		if ocIsValidationError(err) {
			return nil, http.StatusBadRequest, err.Error()
		}
		return nil, http.StatusInternalServerError, ""
	}
	if id, _ := record.Get("id").(string); id != "" {
		_ = h.touchToken(id)
	}
	return bundle, 0, ""
}

func (h *OpenCodeSyncHandler) GetSyncBundle(w http.ResponseWriter, r *http.Request) {
	bundle, status, msg := h.authenticatedBundle(r)
	if bundle == nil {
		if status == http.StatusBadRequest {
			ocError(w, status, msg)
		} else if status == http.StatusUnauthorized {
			ocError(w, status, "Unauthorized")
		} else {
			ocError(w, http.StatusInternalServerError, "Failed to generate OpenCode sync bundle")
		}
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, ocPublicResponse(bundle))
}

func (h *OpenCodeSyncHandler) GetSyncVersion(w http.ResponseWriter, r *http.Request) {
	bundle, status, msg := h.authenticatedBundle(r)
	if bundle == nil {
		if status == http.StatusBadRequest {
			ocError(w, status, msg)
		} else if status == http.StatusUnauthorized {
			ocError(w, status, "Unauthorized")
		} else {
			ocError(w, http.StatusInternalServerError, "Failed to generate OpenCode sync version")
		}
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, clisetup.NewJObj("version", bundle.Hash))
}
