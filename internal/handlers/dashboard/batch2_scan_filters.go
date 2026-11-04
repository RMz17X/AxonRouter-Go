package dashboard

import (
	"sort"
	"strconv"
	"strings"
)

// Port of AxonRouter's suggested-models FILTERS
// (src/app/api/providers/suggested-models/filters.js), used by
// scan-free-models when a provider's modelsFetcher declares a filter
// type. Inputs are decoded JSON values; outputs are candidate items.

func asAnyList(raw any) []any {
	if list, ok := raw.([]any); ok {
		return list
	}
	return nil
}

func asModelMap(item any) map[string]any {
	m, _ := item.(map[string]any)
	return m
}

func numField(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

// jsParseFloatZero mirrors `parseFloat(v) || 0`: anything unparseable or
// missing collapses to 0.
func jsParseFloatZero(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

var opencodeKnownFree = map[string]bool{"big-pickle": true, "beam-free": true, "flint-40b-free": true}

var opencodeDeadFree = map[string]bool{
	"deepseek-v4-flash-free":          true,
	"minimax-m3.1-flash-free":         true,
	"muse-spark-1.2-contributor-free": true,
	"mimo-v2.5-free":                  true,
	"space-bunny-free":                true,
	"longcat-2.5-preview-free":        true,
	"cat-jam-free":                    true,
}

// applyModelFetcherFilter runs one named FILTERS entry. ok=false means
// the type has no filter and the caller keeps the raw list.
func applyModelFetcherFilter(filterType string, raw any) ([]any, bool) {
	switch filterType {
	case "openai":
		out := []any{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			id := dataStr(m, "id")
			if id == "" {
				id = dataStr(m, "name")
			}
			if id == "" {
				continue
			}
			name := dataStr(m, "name")
			if name == "" {
				name = id
			}
			entry := map[string]any{"id": id, "name": name}
			if _, has := m["context_length"]; has {
				entry["contextLength"] = m["context_length"]
			}
			out = append(out, entry)
		}
		return out, true

	case "openrouter-free":
		type entry struct {
			m   map[string]any
			ctx float64
		}
		entries := []entry{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			pricing := dataMap(m, "pricing")
			ctx := numField(m, "context_length")
			if dataStr(pricing, "prompt") == "0" && dataStr(pricing, "completion") == "0" && ctx >= 200000 {
				entries = append(entries, entry{m, ctx})
			}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].ctx > entries[j].ctx })
		out := []any{}
		for _, e := range entries {
			out = append(out, map[string]any{"id": e.m["id"], "name": e.m["name"], "contextLength": e.ctx})
		}
		return out, true

	case "opencode-free":
		out := []any{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			id := dataStr(m, "id")
			if (strings.HasSuffix(id, "-free") || opencodeKnownFree[id]) && !opencodeDeadFree[id] {
				out = append(out, map[string]any{"id": id, "name": id})
			}
		}
		return out, true

	case "orcarouter-free":
		out := []any{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			id := dataStr(m, "id")
			name := dataStr(m, "name")
			zeroPricing := false
			if pricing, ok := m["pricing"].(map[string]any); ok && pricing != nil {
				zeroPricing = jsParseFloatZero(pricing["prompt"]) == 0 && jsParseFloatZero(pricing["completion"]) == 0
			}
			if zeroPricing || strings.HasSuffix(id, ":free") || strings.HasSuffix(id, "-free") ||
				strings.Contains(strings.ToLower(name), "free") {
				out = append(out, item)
			}
		}
		return out, true

	case "airforce-free":
		out := []any{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			id := dataStr(m, "id")
			mediaType := dataStr(m, "model_type")
			if mediaType == "" {
				mediaType = dataStr(m, "media_type")
			}
			if (dataStr(m, "tier") == "free" || strings.HasSuffix(id, ":free")) &&
				m["supports_chat"] == true &&
				(mediaType == "" || mediaType == "chat" || mediaType == "text") {
				out = append(out, item)
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			return dataStr(asModelMap(out[i]), "id") < dataStr(asModelMap(out[j]), "id")
		})
		return out, true

	case "kilocode-free":
		out := []any{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			id := dataStr(m, "id")
			if !(m["isFree"] == true || strings.HasSuffix(id, ":free") || strings.HasSuffix(id, "-free") ||
				id == "kilo-auto/free" || id == "openrouter/free" || id == "stealth/space-bunny-alpha") {
				continue
			}
			cp := make(map[string]any, len(m)+2)
			for k, v := range m {
				cp[k] = v
			}
			contextLength := numField(m, "context_length")
			if contextLength == 0 {
				contextLength = numField(m, "contextLength")
			}
			cp["contextLength"] = contextLength
			maxTokens := numField(dataMap(m, "top_provider"), "max_completion_tokens")
			if maxTokens == 0 {
				maxTokens = numField(m, "maxTokens")
			}
			cp["maxTokens"] = maxTokens
			out = append(out, cp)
		}
		sort.SliceStable(out, func(i, j int) bool {
			return dataStr(asModelMap(out[i]), "name") < dataStr(asModelMap(out[j]), "name")
		})
		return out, true

	case "ovhcloud-free":
		out := []any{}
		for _, item := range asAnyList(raw) {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			if dataStr(m, "id") == "" || numField(m, "context_length") <= 0 {
				continue
			}
			cp := make(map[string]any, len(m)+1)
			for k, v := range m {
				cp[k] = v
			}
			ctx := numField(m, "context_length")
			if ctx == 0 {
				ctx = 131072
			}
			cp["contextLength"] = ctx
			out = append(out, cp)
		}
		return out, true

	case "cline-free":
		list := asAnyList(raw)
		if list == nil {
			if m := asModelMap(raw); m != nil {
				list = asAnyList(m["free"])
			}
		}
		out := []any{}
		for _, item := range list {
			m := asModelMap(item)
			if m == nil {
				continue
			}
			rawID := dataStr(m, "id")
			id := rawID
			for _, prefix := range []string{"cline-free/", "clf/"} {
				if strings.HasPrefix(id, prefix) {
					id = strings.TrimPrefix(id, prefix)
					break
				}
			}
			name := dataStr(m, "name")
			if name == "" {
				name = rawID
			}
			entry := map[string]any{"id": id, "name": name}
			if desc, ok := m["description"].(string); ok {
				entry["description"] = desc
			}
			out = append(out, entry)
		}
		return out, true

	case "minimax-code":
		list := asAnyList(raw)
		if len(list) == 0 {
			return []any{}, true
		}
		first := asModelMap(list[0])
		var config map[string]any
		for _, p := range asAnyList(first["providers"]) {
			pm := asModelMap(p)
			if pm != nil && dataStr(pm, "providerId") == "minimax" {
				config = dataMap(pm, "config")
				break
			}
		}
		if config == nil {
			return []any{}, true
		}
		type entry struct {
			m   map[string]any
			ctx float64
		}
		entries := []entry{}
		modelsObj := dataMap(config, "models")
		ids := make([]string, 0, len(modelsObj))
		for id := range modelsObj {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			mm := dataMap(modelsObj, id)
			name := dataStr(mm, "name")
			if name == "" {
				name = id
			}
			ctx := numField(dataMap(mm, "limit"), "context")
			entries = append(entries, entry{map[string]any{"id": id, "name": name, "contextLength": ctx}, ctx})
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].ctx > entries[j].ctx })
		out := []any{}
		for _, e := range entries {
			out = append(out, e.m)
		}
		return out, true

	case "nous-free":
		return normalizeNousFreeList(raw), true
	}
	return nil, false
}

// normalizeNousFreeList ports open-sse's normalizeNousFreeModels: text-
// capable models with zero prompt+completion pricing and a context of at
// least 200k, normalized like normalizeNousModels and sorted by context
// descending.
func normalizeNousFreeList(raw any) []any {
	list := asAnyList(raw)
	if list == nil {
		if m := asModelMap(raw); m != nil {
			list = asAnyList(m["data"])
			if list == nil {
				list = asAnyList(m["models"])
			}
			if list == nil {
				list = asAnyList(m["results"])
			}
		}
	}
	type entry struct {
		m   map[string]any
		ctx float64
	}
	entries := []entry{}
	for _, item := range list {
		m := asModelMap(item)
		if m == nil {
			continue
		}
		if arch, ok := m["architecture"].(map[string]any); ok {
			if modalities, ok := arch["output_modalities"].([]any); ok && len(modalities) > 0 {
				hasText := false
				for _, mod := range modalities {
					if mod == "text" {
						hasText = true
					}
				}
				if !hasText {
					continue
				}
			}
		}
		pricing := dataMap(m, "pricing")
		zeroPrice := func(v any) bool {
			if v == nil {
				return false
			}
			return jsParseFloatZero(v) == 0
		}
		if !zeroPrice(pricing["prompt"]) || !zeroPrice(pricing["completion"]) {
			continue
		}
		ctx := numField(m, "context_length")
		if ctx == 0 {
			ctx = numField(dataMap(m, "top_provider"), "context_length")
		}
		if ctx < 200000 {
			continue
		}
		id := strings.TrimSpace(dataStr(m, "id"))
		if id == "" {
			continue
		}
		name := dataStr(m, "name")
		if name == "" {
			name = id
		}
		normalized := map[string]any{"id": id, "name": name, "contextLength": ctx}
		if maxOut := numField(dataMap(m, "top_provider"), "max_completion_tokens"); maxOut > 0 {
			normalized["maxOutputTokens"] = maxOut
		}
		if desc, ok := m["description"].(string); ok && desc != "" {
			normalized["description"] = desc
		}
		entries = append(entries, entry{normalized, ctx})
	}
	// normalizeNousModels first stable-sorts first-party ids ahead; the
	// free variant then sorts by context descending (also stable).
	sort.SliceStable(entries, func(i, j int) bool {
		iFirst := strings.HasPrefix(dataStr(entries[i].m, "id"), "nousresearch/")
		jFirst := strings.HasPrefix(dataStr(entries[j].m, "id"), "nousresearch/")
		return iFirst && !jFirst
	})
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ctx > entries[j].ctx })
	out := []any{}
	for _, e := range entries {
		out = append(out, e.m)
	}
	return out
}
