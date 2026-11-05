package chat

// Batch 9 — GET /v1/models/{kind}: Axon's kind-filtered model
// listings (src/app/api/v1/models/[...model]/route.js KIND_SLUG_MAP +
// buildModelsList(kindFilter) in src/app/api/v1/models/route.js).
//
// The listings are MODEL-granular and CONNECTION-scoped, built by the
// same machinery as GET /v1/models (appendCombosFiltered +
// appendConnectionModels with a kind filter): with no active
// connections the provider loop emits nothing — there is no static
// catalogue fallback here, unlike Go's /v1/models default mode. On a
// fresh install Axon returns an empty list; this builder matches that.
//
// Two Axon builder details could not be carried over and are recorded
// in .changes/926:
//   - providerMatchesKinds (the provider-level serviceKinds gate) is
//     not applied: Go's ProviderRegistry ServiceKinds data is an
//     incomplete port (cloudflare-ai carries ["llm","image",
//     "systemone"] where Axon declares ["llm","image","embedding",
//     "tts","stt","task"]), so gating on it would wrongly suppress
//     providers whose models do resolve to the requested kind. The
//     per-model kind resolution below is the operative filter; web
//     lane entries self-gate on the provider's SearchURL/FetchURL.
//   - The comboOnlyModels setting has no counterpart in Go's builder
//     (pre-existing; the LLM list ignores it too).

import (
	"context"
	"slices"
	"strings"

	"axonrouter/proxy/internal/models"
	"axonrouter/proxy/internal/providers"
)

// modelKindSlugMap is Axon's KIND_SLUG_MAP: URL slug → service
// kind(s). `web` covers both webSearch and webFetch. Single source of
// truth for both /models/{kind} and the /models/* lookup dispatch.
var modelKindSlugMap = map[string][]string{
	"image":         {"image"},
	"tts":           {"tts"},
	"stt":           {"stt"},
	"embedding":     {"embedding"},
	"image-to-text": {"imageToText"},
	"web":           {"webSearch", "webFetch"},
	"task":          {"task"},
}

// modelKindFromType mirrors Axon's MODEL_TYPE_TO_KIND + the modelKind
// default: the seven named kinds map to themselves; anything else —
// "llm" itself, unknown types, Go-only markers like "systemone" —
// resolves to "llm".
func modelKindFromType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "image":
		return "image"
	case "tts":
		return "tts"
	case "embedding":
		return "embedding"
	case "stt":
		return "stt"
	case "imagetotext":
		return "imageToText"
	case "video":
		return "video"
	case "task":
		return "task"
	}
	return "llm"
}

// inferKindFromModelID mirrors Axon's inferKindFromUnknownModelId,
// reusing the id-hint patterns the LLM list already uses.
func inferKindFromModelID(modelID string) string {
	switch {
	case embeddingIDHint.MatchString(modelID):
		return "embedding"
	case ttsIDHint.MatchString(modelID):
		return "tts"
	case imageIDHint.MatchString(modelID):
		return "image"
	}
	return "llm"
}

// resolveModelKind mirrors Axon's per-model kind resolution order:
// the custom row's own kind wins, then the registry entry's declared
// kind (staticModelKindById), then the id heuristic.
func resolveModelKind(outputAlias, staticAlias, providerID, modelID, customKind string, hasCustom bool) string {
	if hasCustom {
		return customKind
	}
	for _, key := range []string{outputAlias, staticAlias, providerID} {
		if kind := providers.GetProviderModelKind(key, modelID); kind != "" {
			return modelKindFromType(kind)
		}
	}
	return inferKindFromModelID(modelID)
}

// kindFilterIncludes mirrors Axon's membership test: the kind must be
// in the filter, except imageToText, which also serves the llm list.
func kindFilterIncludes(kinds []string, kind string) bool {
	if slices.Contains(kinds, kind) {
		return true
	}
	return kind == "imageToText" && slices.Contains(kinds, "llm")
}

// buildModelsListForKinds builds a kind-filtered listing the way
// Axon's buildModelsList(kindFilter) does: combos of the kind first,
// then one model set per active connection (first connection per
// provider, hidden providers not offered). No connections → no
// provider entries at all.
func (h *ChatHandler) buildModelsListForKinds(ctx context.Context, kinds []string) []ModelInfoObject {
	data := []ModelInfoObject{}
	if h.Repo == nil {
		return data
	}
	seen := make(map[string]bool)
	disabled := h.disabledModelIndex()
	isDisabled := func(provider, modelID string) bool {
		return disabled[provider][modelID]
	}

	allConns := h.allConnections()
	firstPerProvider := make(map[string]*models.ProviderConnection)
	order := make([]string, 0, len(allConns))
	for _, conn := range allConns {
		if conn == nil || conn.Provider == "" || conn.IsActive == 0 {
			continue
		}
		if hiddenProvider(conn.Provider) {
			continue
		}
		if _, ok := firstPerProvider[conn.Provider]; !ok {
			firstPerProvider[conn.Provider] = conn
			order = append(order, conn.Provider)
		}
	}

	// The active alias set Axon's hasActiveTarget checks combo seats
	// against: provider id, its canonical/alias spellings, and the
	// output alias the connection publishes under.
	nodePrefixes := h.providerNodePrefixMap()
	activePrefixes := make(map[string]bool)
	for _, provID := range order {
		conn := firstPerProvider[provID]
		activePrefixes[provID] = true
		if alias := providers.GetProviderAlias(provID); alias != "" {
			activePrefixes[alias] = true
		}
		if canon := providers.ResolveAlias(provID); canon != "" {
			activePrefixes[canon] = true
		}
		connData := parseConnectionModelData(conn)
		switch {
		case connData.ProviderSpecificData.Prefix != "":
			activePrefixes[connData.ProviderSpecificData.Prefix] = true
		case connData.Prefix != "":
			activePrefixes[connData.Prefix] = true
		case isCompatibleProviderID(provID):
			if prefix := nodePrefixes[provID]; prefix != "" {
				activePrefixes[prefix] = true
			}
		}
	}

	data = h.appendCombosFiltered(data, seen, kinds, activePrefixes)

	customs := h.customModelsByProvider()
	aliases := h.modelAliasTargets()
	for _, provID := range order {
		conn := firstPerProvider[provID]
		data = h.appendConnectionModels(ctx, data, seen, conn, provID, customs, aliases, nodePrefixes, isDisabled, kinds)
	}
	return finalizeModels(data)
}
