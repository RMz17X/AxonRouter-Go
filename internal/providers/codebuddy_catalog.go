package providers

// CodeBuddy-family catalogues (CodeBuddy CN, CodeBuddy Intl, WorkBuddy).
//
// One home for the family's model lists and per-model credit values, ported
// from the JS registries (open-sse/providers/registry/codebuddy-cn.js,
// codebuddy-intl.js, workbuddy.js). ProviderModels references these slices
// under both the canonical id and the alias key, so the two keys can never
// diverge again (audit CB #4: kimi-k2.8-preview once existed under only one
// of the two CN keys).
//
// Credit semantics mirror the JS registry exactly: a model present in
// modelCredits with value 0 is free; a positive value is the per-request
// credit price the dashboard renders as a pill; a model ABSENT from the
// table is unpriced (plan-priced — the Intl/WorkBuddy `default-model`
// preset), renders no pill and sorts last in combo credit ordering.

var codebuddyCNModels = []string{
	"default", "deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4.1-flash",
	"deepseek-v3-2-volc", "minimax-m2.5", "minimax-m3", "minimax-m2.7",
	"minimax-m3.1-flash", "glm-5.3", "glm-5.3-flash", "glm-5.2", "glm-5.1",
	"glm-5.0", "glm-5.0-turbo", "glm-5v-turbo", "glm-4.7", "glm-4.6",
	"glm-4.6v", "kimi-k3-1", "kimi-k2.8-preview", "kimi-k2.7", "kimi-k2.6",
	"kimi-k2.5", "kimi-k2-thinking", "hy3", "hy3-x", "hy4-preview",
	"hy4-preview-x", "hunyuan-chat", "space-bunny", "step-5-preview",
	"mimo-v2.6-pro",
}

var codebuddyIntlModels = []string{
	"default-model", "fast-model", "balanced-model", "primary-model",
	"deep-model", "hy3", "hy4-preview", "deepseek-v4.1-flash",
	"deepseek-v4.1-flash-sg", "glm-5.3", "glm-5.3-flash", "glm-5.2",
	"kimi-k3", "kimi-k2.8-preview", "kimi-k2.6", "minimax-m3.1-flash",
	"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5",
	"gpt-5.4", "gemini-3.5-flash",
}

var workbuddyModels = []string{
	"default-model", "fast-model", "balanced-model", "primary-model",
	"deep-model", "hy3", "hy4-preview", "deepseek-v4.1-flash",
	"deepseek-v4.1-flash-sg", "glm-5.3", "glm-5.3-flash", "glm-5.2",
	"kimi-k3", "kimi-k2.8-preview", "kimi-k2.6", "minimax-m3.1-flash",
	"gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gemini-3.5-flash",
}

// modelCredits is the family's one credit table, keyed by canonical
// provider id. Consumers: the catalog API (batch2 aggregate), the model
// caps API (pill + FREE badge + free-only filter), and combo credit
// ordering (orderByCredit parity). Values are the JS registries' `credit`
// fields, verbatim.
var modelCredits = map[string]map[string]float64{
	"codebuddy-cn": {
		"default": 2.00, "deepseek-v4-pro": 0.51, "deepseek-v4-flash": 0.17,
		"deepseek-v4.1-flash": 0.11, "deepseek-v3-2-volc": 0.29,
		"minimax-m2.5": 0.18, "minimax-m3": 0.25, "minimax-m2.7": 0.19,
		"minimax-m3.1-flash": 0.08, "glm-5.3": 0.69, "glm-5.3-flash": 0.08,
		"glm-5.2": 0.79, "glm-5.1": 0.79, "glm-5.0": 0.80,
		"glm-5.0-turbo": 0.95, "glm-5v-turbo": 0.71, "glm-4.7": 0.23,
		"glm-4.6": 0.23, "glm-4.6v": 0.11, "kimi-k3-1": 1.62,
		"kimi-k2.8-preview": 0.77, "kimi-k2.7": 0.57, "kimi-k2.6": 0.52,
		"kimi-k2.5": 0.45, "kimi-k2-thinking": 0.54, "hy3": 0,
		"hy3-x": 0.05, "hy4-preview": 0.29, "hy4-preview-x": 0.29,
		"hunyuan-chat": 0.10, "space-bunny": 0.08, "step-5-preview": 0,
		"mimo-v2.6-pro": 0.13,
	},
	"codebuddy-intl": {
		// default-model carries no credit in the JS registry (plan-priced).
		"fast-model": 0.34, "balanced-model": 0.59, "primary-model": 3.31,
		"deep-model": 3.33, "hy3": 0, "hy4-preview": 0.29,
		"deepseek-v4.1-flash": 0, "deepseek-v4.1-flash-sg": 0.03,
		"glm-5.3": 0.79, "glm-5.3-flash": 0.06, "glm-5.2": 0.79,
		"kimi-k3": 1.62, "kimi-k2.8-preview": 0.77, "kimi-k2.6": 0.52,
		"minimax-m3.1-flash": 0.08, "gpt-6-astra": 6.67, "gpt-5.6-sol": 3.47,
		"gpt-5.6-terra": 1.39, "gpt-5.6-luna": 0.14, "gpt-5.5": 3.31,
		"gpt-5.4": 1.65, "gemini-3.5-flash": 0.99,
	},
	"workbuddy": {
		// default-model carries no credit in the JS registry (plan-priced).
		"fast-model": 0.34, "balanced-model": 0.59, "primary-model": 3.31,
		"deep-model": 3.33, "hy3": 0, "hy4-preview": 0.29,
		"deepseek-v4.1-flash": 0, "deepseek-v4.1-flash-sg": 0.03,
		"glm-5.3": 0.79, "glm-5.3-flash": 0.06, "glm-5.2": 0.79,
		"kimi-k3": 1.62, "kimi-k2.8-preview": 0.77, "kimi-k2.6": 0.52,
		"minimax-m3.1-flash": 0.08, "gpt-5.6-terra": 1.39,
		"gpt-5.6-luna": 0.14, "gpt-5.5": 3.31, "gpt-5.4": 1.65,
		"gemini-3.5-flash": 0.99,
	},
}

// GetModelCredit returns the model's credit price and whether it is priced
// at all. The provider may be the canonical id or any registered alias.
func GetModelCredit(provider, model string) (float64, bool) {
	byModel, ok := modelCredits[ResolveAlias(provider)]
	if !ok {
		return 0, false
	}
	credit, ok := byModel[model]
	return credit, ok
}
