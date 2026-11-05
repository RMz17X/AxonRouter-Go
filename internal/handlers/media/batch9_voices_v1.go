package media

// Batch 9 — GET /v1/audio/voices, ported from AxonRouter
// src/app/api/v1/audio/voices/route.js. This is the ENGINE spelling's
// own handler: Axon requires the provider query param, whitelists
// exactly {elevenlabs, deepgram, inworld, edge-tts, local-device},
// and reshapes the internal voices payload into an OpenAI-style list
// ({object:"list", data:[{id, name, lang, gender, model}]}). Axon
// self-fetches its internal capabilities-providers APIs to build that
// payload; the Go port calls the same in-package fetchers
// HandleAudioVoices uses — the observable contract is the reshaped
// output, not the self-fetch. The dashboard spellings
// (/api/media-providers/tts/*) keep the shared HandleAudioVoices and
// its {voices, languages, byLang} shape byte-identical.

import (
	"net/http"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/providers"
)

// audioVoicesV1Providers is Axon's PROVIDER_API key set, in the
// route's own order (the 400 message enumerates it verbatim).
var audioVoicesV1Providers = map[string]bool{
	"elevenlabs":   true,
	"deepgram":     true,
	"inworld":      true,
	"edge-tts":     true,
	"local-device": true,
}

const audioVoicesV1ProviderList = "elevenlabs, deepgram, inworld, edge-tts, local-device"

// writeAudioVoicesV1Error emits Axon's nested engine envelope for this
// route ({error:{message, type}} — no code key) with the CORS header
// the route sets on every response.
func writeAudioVoicesV1Error(w http.ResponseWriter, status int, message, errType string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	handlerutil.WriteJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
		},
	})
}

// HandleAudioVoicesV1 serves GET /v1/audio/voices (the engine
// registration only; see router.go).
func (h *MediaHandler) HandleAudioVoicesV1(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider := q.Get("provider")
	langFilter := q.Get("lang")

	if !audioVoicesV1Providers[provider] {
		writeAudioVoicesV1Error(w, http.StatusBadRequest,
			"provider must be one of: "+audioVoicesV1ProviderList, "invalid_request_error")
		return
	}

	var (
		voices []map[string]any
		err    error
	)
	switch provider {
	case "edge-tts":
		voices, err = fetchEdgeTTSVoices(h.Client, r.Context())
	case "deepgram":
		voices, err = h.fetchDeepgramVoices(r.Context())
	case "inworld":
		voices, err = h.fetchInworldVoices(r.Context())
	case "elevenlabs":
		apiKey := q.Get("apiKey")
		if apiKey == "" {
			apiKey = h.connectionAPIKey("elevenlabs")
		}
		voices, err = fetchElevenLabsVoices(h.Client, r.Context(), apiKey)
	case "local-device":
		voices = fetchLocalDeviceVoices()
	}
	if err != nil {
		// The fetchers carry no typed upstream status; per the batch-9
		// audit the wrapper answers 502 here (Axon's catch status).
		writeAudioVoicesV1Error(w, http.StatusBadGateway, err.Error(), "server_error")
		return
	}

	// Axon: with ?lang= the internal API's flat filtered list is used;
	// without it the byLang groups are flattened in group order. The
	// fetchers return one flat list, so the wrapper reproduces both
	// orders from it: filter for lang, else group by lang in
	// first-seen order (the internal API's byLang insertion order).
	var ordered []map[string]any
	if langFilter != "" {
		ordered = make([]map[string]any, 0, len(voices))
		for _, v := range voices {
			if v["lang"] == langFilter {
				ordered = append(ordered, v)
			}
		}
	} else {
		ordered = flattenVoicesByLang(voices)
	}

	alias := providers.GetProviderAlias(provider)
	out := make([]map[string]any, 0, len(ordered))
	for _, v := range ordered {
		id, _ := v["id"].(string)
		name, _ := v["name"].(string)
		lang, _ := v["lang"].(string)
		gender, _ := v["gender"].(string)
		out = append(out, map[string]any{
			"id":     id,
			"name":   name,
			"lang":   lang,
			"gender": gender,
			"model":  alias + "/" + id,
		})
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   out,
	})
}

// flattenVoicesByLang groups voices by their lang value, preserving
// first-seen language order and the original order within each group
// — the flattening Axon's route applies to the internal byLang map.
func flattenVoicesByLang(voices []map[string]any) []map[string]any {
	order := []string{}
	groups := map[string][]map[string]any{}
	for _, v := range voices {
		lang, _ := v["lang"].(string)
		if _, ok := groups[lang]; !ok {
			order = append(order, lang)
		}
		groups[lang] = append(groups[lang], v)
	}
	out := make([]map[string]any, 0, len(voices))
	for _, lang := range order {
		out = append(out, groups[lang]...)
	}
	return out
}
