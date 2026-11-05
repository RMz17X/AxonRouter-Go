package chat

// GET /v1/models/info — AxonRouter's per-model info document
// (src/app/api/v1/models/info/route.js), reshaped in batch 10
// (audit item 2, owner order 2026-11-04 22:05 WIB).
//
// Axon's lookup is STATIC-CATALOGUE ONLY: the id must name a model
// in the provider's registry catalogue (combos are never found and
// answer 404), an optional ?kind= must match the catalogue kind,
// and two virtual models exist beside the catalogue — "<provider>/
// search" and "<provider>/fetch" for providers carrying a
// search/fetch config. The success document is buildInfo's shape:
// {id, name, kind, owned_by, endpoint} plus only the fields the
// catalogue entry itself carries.
//
// What Go can source truthfully, and what it cannot (all recorded
// in .changes/929):
//   - id / kind / owned_by / endpoint: the registry carries model
//     ids and per-model kinds; endpoint is Axon's KIND_ENDPOINT
//     table keyed by that kind.
//   - name: Go's catalogue is ids-only (ProviderModels is
//     map[string][]string) — no display names exist, so name falls
//     back to the model id, the same fallback Axon's buildInfo
//     applies when an entry has no name (model.name || model.id).
//   - contextWindow: emitted when the gateway's capabilities chain
//     yields one for the model (Axon emits its catalogue field of
//     the same name).
//   - voicesUrl: emitted for kind "tts" on Axon's five voice-list
//     providers.
//   - params / capabilities / options / dimensions: Axon's
//     catalogue carries these per model; Go's registry has no
//     per-model source for them (the capabilities chain is a
//     boolean feature struct, a different vocabulary from Axon's
//     string-array catalogue field), so they are OMITTED rather
//     than fabricated.
//   - The virtual search/fetch models carry Axon's fixed params
//     arrays (contract constants); searchTypes / maxResults /
//     required come from a provider searchConfig Go's registry
//     does not carry, so they are omitted, and the virtual name
//     uses the provider id where Axon uses its display name.
//
// Owner-ordered behavior changes vs the pre-batch-10 document:
// the Go-only keys (object, context_length, context_window,
// max_completion_tokens, max_input_tokens, max_output_tokens and
// the combo block) are deleted, and combo ids flip from 200 to
// Axon's 404. The per-key model-access gate this gateway applies
// to every /v1 surface stays (it is gateway policy, not document
// shape); Axon's error documents and its CORS header are served
// verbatim.

import (
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// axonKindEndpoint is Axon's KIND_ENDPOINT table
// (src/app/api/v1/models/info/route.js), keyed by Axon kind names.
// A kind outside the table yields a null endpoint, as in Axon.
// (access.go's kindEndpoint is keyed by the /v1/models/{kind}
// slugs and maps webFetch differently — a separate surface.)
var axonKindEndpoint = map[string]string{
	"llm":         "/v1/chat/completions",
	"image":       "/v1/images/generations",
	"tts":         "/v1/audio/speech",
	"stt":         "/v1/audio/transcriptions",
	"embedding":   "/v1/embeddings",
	"imageToText": "/v1/chat/completions",
	"webSearch":   "/v1/search",
	"webFetch":    "/v1/fetch",
	"task":        "/v1/task",
}

// axonVoicesProviders are the providers whose tts models carry a
// voicesUrl in Axon's buildInfo.
var axonVoicesProviders = map[string]bool{
	"elevenlabs": true, "edge-tts": true, "deepgram": true,
	"inworld": true, "local-device": true,
}

// axonWebSearchParams / axonWebFetchParams are the fixed params
// arrays Axon's buildInfo emits for the virtual web models.
var (
	axonWebSearchParams = []string{"query", "max_results", "country", "language", "time_range", "domain_filter", "search_type"}
	axonWebFetchParams  = []string{"url", "format", "max_characters"}
)

// HandleModelsInfo returns metadata for a specific model.
// GET /v1/models/info?id={modelId}[&kind={kind}]
func (h *ChatHandler) HandleModelsInfo(w http.ResponseWriter, r *http.Request) {
	// Axon sends its CORS header on every response of this route.
	w.Header().Set("Access-Control-Allow-Origin", "*")

	modelID := r.URL.Query().Get("id")
	if modelID == "" {
		writeModelInfoMissingID(w)
		return
	}
	if !h.enforceModelAccess(w, r, modelID) {
		return
	}

	slash := strings.Index(modelID, "/")
	if slash < 0 {
		writeModelInfoNotFound(w, modelID)
		return
	}
	alias, bareModel := modelID[:slash], modelID[slash+1:]
	providerID := resolveProviderAlias(alias)
	requestedKind := r.URL.Query().Get("kind")

	// Catalogue lookup (Axon checks the alias key, then the
	// canonical provider id — GetProviderModels does the same).
	for _, candidate := range providers.GetProviderModels(alias) {
		if candidate != bareModel {
			continue
		}
		kind := providers.GetProviderModelKind(alias, bareModel)
		if kind == "" {
			kind = "llm" // Axon's getModelKind fallback
		}
		if requestedKind != "" && requestedKind != kind {
			writeModelInfoNotFound(w, modelID)
			return
		}
		info := map[string]any{
			"id":       alias + "/" + bareModel,
			"name":     bareModel,
			"kind":     kind,
			"owned_by": alias,
		}
		if endpoint, ok := axonKindEndpoint[kind]; ok {
			info["endpoint"] = endpoint
		} else {
			info["endpoint"] = nil
		}
		if caps := providers.GetCapabilitiesDetailForModel(providerID, bareModel); caps.ContextWindow > 0 {
			info["contextWindow"] = caps.ContextWindow
		}
		if kind == "tts" && axonVoicesProviders[providerID] {
			info["voicesUrl"] = "/v1/audio/voices?provider=" + providerID
		}
		handlerutil.WriteJSON(w, http.StatusOK, info)
		return
	}

	// Virtual web models (Axon builds these beside the catalogue
	// when the provider carries a search/fetch config; Go's
	// registry carries the same fact as SearchURL / FetchURL).
	if cfg, ok := providers.KnownProviders[providerID]; ok {
		switch bareModel {
		case "search":
			if cfg.SearchURL != "" {
				handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
					"id": alias + "/search", "name": providerID + " Search",
					"kind": "webSearch", "owned_by": alias,
					"endpoint": axonKindEndpoint["webSearch"],
					"params":   axonWebSearchParams,
				})
				return
			}
		case "fetch":
			if cfg.FetchURL != "" {
				handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
					"id": alias + "/fetch", "name": providerID + " Fetch",
					"kind": "webFetch", "owned_by": alias,
					"endpoint": axonKindEndpoint["webFetch"],
					"params":   axonWebFetchParams,
				})
				return
			}
		}
	}

	writeModelInfoNotFound(w, modelID)
}

// writeModelInfoMissingID writes Axon's 400 document for this
// route, hand-built like the 404 below: the shared
// handlerutil.WriteJSONError adds a "code" key Axon does not send,
// and the earlier port also mistyped the message (no space in
// "e.g. ?id=") — the file header's "served verbatim" claim covers
// this document only with the hand-built form (council round 1,
// fidelity NIT-1).
// {error:{message:"Missing required query param: id (e.g. ?id=openai/dall-e-3)",
// type:"invalid_request_error"}}
func writeModelInfoMissingID(w http.ResponseWriter) {
	handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"message": "Missing required query param: id (e.g. ?id=openai/dall-e-3)",
			"type":    "invalid_request_error",
		},
	})
}

// writeModelInfoNotFound writes Axon's 404 document for this
// route: {error:{message:"Model not found: <id>", type:"not_found"}}.
func writeModelInfoNotFound(w http.ResponseWriter, modelID string) {
	handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"message": "Model not found: " + modelID,
			"type":    "not_found",
		},
	})
}
