package dashboard

import (
	stdjson "encoding/json"
	"sort"
	"strings"

	"9router/proxy/internal/clisetup"
)

// OpenCode sync bundle generator. Port of Axon's
// src/lib/opencodeSync/generator.ts + presets.ts: deterministic
// plugin/model selection from preferences + catalog, artifact
// construction for opencode.json and the variant configs, and the
// sha256 content hash that versions a bundle.

const (
	ocSyncPlugin      = "opencode-axonrouter-sync@latest"
	ocOpenagentPlugin = "oh-my-openagent@latest"
	ocSlimPlugin      = "oh-my-opencode-slim@latest"
	ocOpenCodeSchema  = "https://opencode.ai/config.json"
	ocOpenAgentSchema = "https://raw.githubusercontent.com/code-yeongyu/oh-my-openagent/main/assets/oh-my-opencode.schema.json"
	ocSlimSchema      = "https://unpkg.com/oh-my-opencode-slim@latest/oh-my-opencode-slim.schema.json"
	ocProviderID      = "axonrouter"
	ocProviderPackage = "@ai-sdk/openai-compatible"
	ocProviderName    = "AxonRouter"
	ocDefaultBaseURL  = "http://localhost:12711/v1"
	ocDefaultAPIKey   = "sk_axonrouter"
	ocBundleSchemaVer = 1
)

var ocPluginPriority = map[string]int{
	ocSyncPlugin:      0,
	ocOpenagentPlugin: 1,
	ocSlimPlugin:      2,
}

// --- generic tree helpers ---------------------------------------------

// ocCompare approximates JS localeCompare for ASCII identifiers:
// primary comparison is case-insensitive, case breaks ties with
// lowercase first. Axon sorts catalog ids, object keys and plugin
// names with localeCompare; byte order would rank every uppercase
// letter before all lowercase ones.
func ocCompare(a, b string) int {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return strings.Compare(la, lb)
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] > b[i] { // a is uppercase where b is lowercase
				return 1
			}
			return -1
		}
	}
	return len(a) - len(b)
}

func ocSortStrings(s []string) {
	sort.Slice(s, func(i, j int) bool { return ocCompare(s[i], s[j]) < 0 })
}

func ocStableClone(v any) any {
	switch t := v.(type) {
	case *clisetup.JObj:
		keys := t.Keys()
		ocSortStrings(keys)
		out := clisetup.NewJObj()
		for _, k := range keys {
			out.Set(k, ocStableClone(t.Get(k)))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = ocStableClone(item)
		}
		return out
	default:
		return v
	}
}

// ocMergePlain ports mergePlainObjects (deep merge, override wins,
// plain-object values merge recursively).
func ocMergePlain(base, override any) any {
	baseObj, baseOK := ocPlain(base)
	overrideObj, overrideOK := ocPlain(override)
	if !baseOK {
		return ocStableClone(override)
	}
	if !overrideOK {
		return ocStableClone(base)
	}
	merged := clisetup.NewJObj()
	for _, k := range baseObj.Keys() {
		merged.Set(k, ocStableClone(baseObj.Get(k)))
	}
	for _, k := range overrideObj.Keys() {
		if _, isObj := ocPlain(merged.Get(k)); isObj {
			if _, overIsObj := ocPlain(overrideObj.Get(k)); overIsObj {
				merged.Set(k, ocMergePlain(merged.Get(k), overrideObj.Get(k)))
				continue
			}
		}
		merged.Set(k, ocStableClone(overrideObj.Get(k)))
	}
	return ocStableClone(merged)
}

// ocNum reads a finite JSON number (Number.isFinite semantics).
func ocNum(v any) (float64, bool) {
	switch n := v.(type) {
	case stdjson.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func ocSortedKeys(o *clisetup.JObj) []string {
	if o == nil {
		return nil
	}
	keys := o.Keys()
	ocSortStrings(keys)
	return keys
}

// --- catalog normalization (generator side) ---------------------------

// ocCatalog is the normalized model catalog: insertion-ordered ids
// mapped to their entry objects.
type ocCatalog struct {
	order   []string
	entries map[string]*clisetup.JObj
}

func ocCatalogEntryID(item any, fallback string) string {
	if s, ok := item.(string); ok {
		return strings.TrimSpace(s)
	}
	obj, ok := ocPlain(item)
	if !ok {
		return fallback
	}
	for _, key := range []string{"id", "key", "model"} {
		if s, ok := obj.Get(key).(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return fallback
}

// ocNormalizeCatalogEntry ports normalizeCatalogEntry.
func ocNormalizeCatalogEntry(id string, value any) *clisetup.JObj {
	if obj, ok := ocPlain(value); ok {
		clone, _ := ocStableClone(obj).(*clisetup.JObj)
		clone.Set("id", id)
		return clone
	}
	if value == nil {
		return clisetup.NewJObj("id", id)
	}
	return clisetup.NewJObj("id", id, "value", value)
}

func (c *ocCatalog) has(id string) bool {
	_, ok := c.entries[id]
	return ok
}

func (c *ocCatalog) get(id string) *clisetup.JObj {
	return c.entries[id]
}

// --- deterministic selection ------------------------------------------

func ocComparePlugins(a, b string) int {
	pa, okA := ocPluginPriority[a]
	pb, okB := ocPluginPriority[b]
	if !okA {
		pa = int(^uint(0) >> 1)
	}
	if !okB {
		pb = int(^uint(0) >> 1)
	}
	if pa != pb {
		return pa - pb
	}
	return ocCompare(a, b)
}

func ocPluginList(prefs *clisetup.JObj) []any {
	set := map[string]bool{ocSyncPlugin: true}
	variant, _ := prefs.Get("variant").(string)
	switch variant {
	case "openagent":
		set[ocOpenagentPlugin] = true
	case "slim":
		set[ocSlimPlugin] = true
	}
	// Custom-template presets carry no plugin in Axon.
	if plugins, ok := prefs.Get("customPlugins").([]any); ok {
		for _, p := range plugins {
			if s, ok := p.(string); ok && s != "" {
				set[s] = true
			}
		}
	}
	list := make([]string, 0, len(set))
	for p := range set {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return ocComparePlugins(list[i], list[j]) < 0 })
	out := make([]any, len(list))
	for i, p := range list {
		out[i] = p
	}
	return out
}

func ocModelMap(prefs *clisetup.JObj, catalog *ocCatalog) *clisetup.JObj {
	excluded := map[string]bool{}
	if arr, ok := prefs.Get("excludedModels").([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				excluded[s] = true
			}
		}
	}
	var ids []string
	if mode, _ := prefs.Get("modelSelectionMode").(string); mode == "include" {
		if arr, ok := prefs.Get("includedModels").([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && catalog.has(s) {
					ids = append(ids, s)
				}
			}
		}
	} else {
		for _, id := range catalog.order {
			if !excluded[id] {
				ids = append(ids, id)
			}
		}
	}
	seen := map[string]bool{}
	unique := []string{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	ocSortStrings(unique)
	out := clisetup.NewJObj()
	for _, id := range unique {
		out.Set(id, ocStableClone(catalog.get(id)))
	}
	return out
}

// ocTemplateOverrides returns the custom template preset's
// advancedOverrides ({} for non-custom variants / no template).
func ocTemplateOverrides(prefs *clisetup.JObj) any {
	variant, _ := prefs.Get("variant").(string)
	if variant != "custom" {
		return clisetup.NewJObj()
	}
	switch tmpl, _ := prefs.Get("customTemplate").(string); tmpl {
	case "minimal":
		return clisetup.NewJObj(
			"generation", clisetup.NewJObj("strategy", "manual"),
			"ui", clisetup.NewJObj("mode", "minimal"),
		)
	case "opinionated":
		return clisetup.NewJObj(
			"generation", clisetup.NewJObj("strategy", "assisted"),
			"safety", clisetup.NewJObj("confirmations", true),
			"ui", clisetup.NewJObj("mode", "opinionated"),
		)
	default:
		return clisetup.NewJObj()
	}
}

// --- variant config -----------------------------------------------------

func ocPrimaryModels(models *clisetup.JObj, defaultModel string) (preferred, secondary, tertiary string) {
	ids := ocSortedKeys(models)
	if len(ids) == 0 {
		return "", "", ""
	}
	preferred = ids[0]
	if defaultModel != "" {
		for _, id := range ids {
			if id == defaultModel {
				preferred = defaultModel
			}
		}
	}
	secondary = preferred
	for _, id := range ids {
		if id != preferred {
			secondary = id
			break
		}
	}
	tertiary = secondary
	for _, id := range ids {
		if id != preferred && id != secondary {
			tertiary = id
			break
		}
	}
	return preferred, secondary, tertiary
}

// ocVariantConfig ports buildGeneratedVariantConfig. bundle carries
// variant, models, defaultModel and the merged advancedOverrides.
func ocVariantConfig(variant string, models *clisetup.JObj, defaultModel string, advancedOverrides any) any {
	if variant == "custom" {
		return ocStableClone(advancedOverrides)
	}
	preferred, secondary, tertiary := ocPrimaryModels(models, defaultModel)
	var base *clisetup.JObj
	if variant == "slim" {
		base = clisetup.NewJObj(
			"preset", "balanced",
			"agentAssignments", clisetup.NewJObj(
				"core", preferred, "research", secondary, "execution", secondary),
			"categoryAssignments", clisetup.NewJObj(
				"default", preferred, "long-context", preferred, "low-latency", secondary),
		)
	} else {
		base = clisetup.NewJObj(
			"preset", "balanced",
			"agentAssignments", clisetup.NewJObj(
				"explorer", secondary, "sisyphus", preferred, "oracle", preferred,
				"librarian", secondary, "prometheus", preferred, "atlas", secondary),
			"categoryAssignments", clisetup.NewJObj(
				"deep", preferred, "quick", secondary, "visual-engineering", secondary,
				"writing", preferred, "artistry", tertiary),
		)
	}
	return ocMergePlain(base, advancedOverrides)
}

// --- model metadata -----------------------------------------------------

type ocInferred struct {
	context   int
	output    int
	reasoning bool
}

func ocInferMetadata(modelID, ownedBy string) ocInferred {
	id := strings.ToLower(modelID)
	provider := strings.ToLower(ownedBy)
	reasoning := strings.Contains(id, "thinking") || strings.Contains(id, "reasoning") ||
		strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4")
	context, output := 200000, 64000
	switch {
	case provider == "google" || provider == "antigravity" || provider == "ag" || strings.Contains(id, "gemini"):
		context, output = 1048576, 65536
	case provider == "openai" || provider == "cx" || provider == "gh" || strings.HasPrefix(id, "gpt"):
		context, output = 400000, 128000
		if strings.Contains(id, "gpt-5.4") || strings.Contains(id, "gpt-5.3") {
			context = 1050000
		}
	case provider == "anthropic" || provider == "cc" || strings.Contains(id, "claude"):
		context, output = 200000, 32000
		if strings.Contains(id, "claude-4") || strings.Contains(id, "claude-opus-4") || strings.Contains(id, "claude-sonnet-4") {
			output = 64000
		}
	case provider == "deepseek" || strings.Contains(id, "deepseek"):
		context, output = 128000, 8192
		if strings.Contains(id, "r1") || strings.Contains(id, "reasoner") {
			context = 64000
		}
	case provider == "qwen" || provider == "qw" || provider == "if" || strings.Contains(id, "qwen"):
		context, output = 128000, 8192
	case strings.Contains(id, "glm"):
		context, output = 128000, 4096
	}
	return ocInferred{context: context, output: output, reasoning: reasoning}
}

func ocNormalizeModalities(src *clisetup.JObj) any {
	mods, ok := ocPlain(src.Get("modalities"))
	if !ok {
		return nil
	}
	out := clisetup.NewJObj()
	for _, key := range []string{"input", "output"} {
		if arr, ok := mods.Get(key).([]any); ok && len(arr) > 0 {
			out.Set(key, ocStableClone(arr))
		}
	}
	if len(out.Keys()) == 0 {
		return nil
	}
	return out
}

func ocNormalizeLimit(src *clisetup.JObj) any {
	limitObj, _ := ocPlain(src.Get("limit"))
	var contextVal, outputVal any
	if f, ok := ocNum(limitObj.Get("context")); ok {
		contextVal = numAny(f)
	} else if f, ok := ocNum(src.Get("contextWindow")); ok {
		contextVal = numAny(f)
	} else if f, ok := ocNum(src.Get("maxInputTokens")); ok {
		contextVal = numAny(f)
	}
	if f, ok := ocNum(limitObj.Get("output")); ok {
		outputVal = numAny(f)
	} else if f, ok := ocNum(src.Get("maxOutputTokens")); ok {
		outputVal = numAny(f)
	}
	out := clisetup.NewJObj()
	if contextVal != nil {
		out.Set("context", contextVal)
	}
	if outputVal != nil {
		out.Set("output", outputVal)
	}
	if len(out.Keys()) == 0 {
		return nil
	}
	return out
}

// numAny keeps integral floats as ints so the JSON hash input matches
// JSON.stringify's rendering.
func numAny(f float64) any {
	if f == float64(int64(f)) {
		return int(f)
	}
	return f
}

// ocConfigModelMetadata ports buildConfigModelMetadata.
func ocConfigModelMetadata(modelID string, value any) *clisetup.JObj {
	src, ok := ocPlain(value)
	if !ok {
		src = clisetup.NewJObj()
	}
	configModelID := strings.TrimSpace(modelID)

	_, hasModalities := ocPlain(src.Get("modalities"))
	_, limitIsObj := ocPlain(src.Get("limit"))
	_, hasCtx := ocNum(src.Get("contextWindow"))
	_, hasIn := ocNum(src.Get("maxInputTokens"))
	_, hasOut := ocNum(src.Get("maxOutputTokens"))
	hasLimit := limitIsObj || hasCtx || hasIn || hasOut

	var inferred *ocInferred
	if !hasModalities || !hasLimit {
		ownedBy, _ := src.Get("provider").(string)
		if ownedBy == "" {
			ownedBy, _ = src.Get("owned_by").(string)
		}
		inf := ocInferMetadata(configModelID, ownedBy)
		inferred = &inf
	}

	meta := clisetup.NewJObj()
	meta.Set("name", configModelID)
	if _, present := hasKey(src, "attachment"); present {
		meta.Set("attachment", src.Get("attachment") == true)
	} else {
		meta.Set("attachment", true)
	}
	if mods := ocNormalizeModalities(src); mods != nil {
		meta.Set("modalities", mods)
	} else if inferred != nil {
		meta.Set("modalities", clisetup.NewJObj(
			"input", []any{"text", "image"},
			"output", []any{"text"},
		))
	}
	if lim := ocNormalizeLimit(src); lim != nil {
		meta.Set("limit", lim)
	} else if inferred != nil {
		meta.Set("limit", clisetup.NewJObj("context", inferred.context, "output", inferred.output))
	}
	if src.Get("reasoning") == true || (inferred != nil && inferred.reasoning) {
		meta.Set("reasoning", true)
	}
	clone, _ := ocStableClone(meta).(*clisetup.JObj)
	return clone
}
