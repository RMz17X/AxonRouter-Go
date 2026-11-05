package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"sort"

	"axonrouter/proxy/internal/handlerutil"
)

// /api/pricing — port of AxonRouter's src/app/api/pricing/route.js +
// pricingRepo: user overrides live in the kv store under scope "pricing"
// (key = provider, value = that provider's model->rates object), merged
// over the shipped PROVIDER_PRICING defaults for reads.

const pricingKVScope = "pricing"

var pricingValidFields = map[string]bool{
	"input": true, "output": true, "cached": true, "reasoning": true, "cache_creation": true,
}

func pricingDefaults() map[string]any {
	var defaults map[string]any
	if err := json.Unmarshal([]byte(providerPricingDefaultsJSON), &defaults); err != nil {
		return map[string]any{}
	}
	return defaults
}

// userPricing reads the whole pricing kv scope as {provider: models}.
func (h *DashboardHandler) userPricing() (map[string]any, error) {
	rows, err := h.Repo.GetKVScope(pricingKVScope)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(rows))
	for provider, raw := range rows {
		var models map[string]any
		if err := json.Unmarshal([]byte(raw), &models); err == nil && models != nil {
			out[provider] = models
		}
	}
	return out, nil
}

// mergedPricing reproduces pricingRepo.getPricing: defaults first with
// user models shallow-merged per model, then user-only providers.
func (h *DashboardHandler) mergedPricing() (map[string]any, error) {
	user, err := h.userPricing()
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	defaults := pricingDefaults()
	for provider, defModels := range defaults {
		mergedModels := map[string]any{}
		if dm, ok := defModels.(map[string]any); ok {
			for model, rates := range dm {
				mergedModels[model] = rates
			}
		}
		if um, ok := user[provider].(map[string]any); ok {
			for model, rates := range um {
				if existing, ok := mergedModels[model].(map[string]any); ok {
					combined := map[string]any{}
					for k, v := range existing {
						combined[k] = v
					}
					if rm, ok := rates.(map[string]any); ok {
						for k, v := range rm {
							combined[k] = v
						}
					}
					mergedModels[model] = combined
				} else {
					mergedModels[model] = rates
				}
			}
		}
		merged[provider] = mergedModels
	}
	for provider, models := range user {
		if _, ok := merged[provider]; !ok {
			merged[provider] = models
		}
	}
	return merged, nil
}

// HandleGetPricing serves GET /api/pricing (merged defaults + overrides).
func (h *DashboardHandler) HandleGetPricing(w http.ResponseWriter, r *http.Request) {
	merged, err := h.mergedPricing()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to fetch pricing"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, merged)
}

// HandleUpdatePricing serves PATCH /api/pricing. Validation messages are
// AxonRouter's verbatim strings. On success it answers with the USER
// pricing only — that is what AxonRouter's updatePricing returns.
func (h *DashboardHandler) HandleUpdatePricing(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update pricing"})
		return
	}
	defer r.Body.Close()
	// AxonRouter distinguishes two failures: request.json() throwing lands
	// in the route catch (500), while parsed JSON of the wrong shape is the
	// 400 format error.
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update pricing"})
		return
	}
	body, isObject := parsed.(map[string]any)
	if !isObject || body == nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid pricing data format"})
		return
	}

	providers := make([]string, 0, len(body))
	for p := range body {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		models, ok := body[provider].(map[string]any)
		if !ok {
			handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid pricing for provider: " + provider})
			return
		}
		modelNames := make([]string, 0, len(models))
		for m := range models {
			modelNames = append(modelNames, m)
		}
		sort.Strings(modelNames)
		for _, model := range modelNames {
			rates, ok := models[model].(map[string]any)
			if !ok {
				handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid pricing for model: " + provider + "/" + model})
				return
			}
			fields := make([]string, 0, len(rates))
			for f := range rates {
				fields = append(fields, f)
			}
			sort.Strings(fields)
			for _, field := range fields {
				if !pricingValidFields[field] {
					handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid pricing field: " + field + " for " + provider + "/" + model})
					return
				}
				value, isNumber := rates[field].(float64)
				if !isNumber || value < 0 {
					handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid pricing value for " + field + " in " + provider + "/" + model + ": must be non-negative number"})
					return
				}
			}
		}
	}

	user, err := h.userPricing()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update pricing"})
		return
	}
	for _, provider := range providers {
		current, _ := user[provider].(map[string]any)
		if current == nil {
			current = map[string]any{}
		}
		// Model-level replace, not a field merge (updatePricing semantics).
		for model, rates := range body[provider].(map[string]any) {
			current[model] = rates
		}
		encoded, err := json.Marshal(current)
		if err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update pricing"})
			return
		}
		if err := h.Repo.SetKV(pricingKVScope, provider, string(encoded)); err != nil {
			handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update pricing"})
			return
		}
	}

	updated, err := h.userPricing()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to update pricing"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, updated)
}

// HandleResetPricing serves DELETE /api/pricing?provider=&model=.
func (h *DashboardHandler) HandleResetPricing(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	model := r.URL.Query().Get("model")
	fail := func() {
		handlerutil.WriteJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to reset pricing"})
	}
	switch {
	case provider != "" && model != "":
		user, err := h.userPricing()
		if err != nil {
			fail()
			return
		}
		if models, ok := user[provider].(map[string]any); ok {
			delete(models, model)
			if len(models) == 0 {
				if err := h.Repo.DeleteKV(pricingKVScope, provider); err != nil {
					fail()
					return
				}
			} else {
				encoded, err := json.Marshal(models)
				if err != nil {
					fail()
					return
				}
				if err := h.Repo.SetKV(pricingKVScope, provider, string(encoded)); err != nil {
					fail()
					return
				}
			}
		}
	case provider != "":
		if err := h.Repo.DeleteKV(pricingKVScope, provider); err != nil {
			fail()
			return
		}
	default:
		rows, err := h.Repo.GetKVScope(pricingKVScope)
		if err != nil {
			fail()
			return
		}
		for key := range rows {
			if err := h.Repo.DeleteKV(pricingKVScope, key); err != nil {
				fail()
				return
			}
		}
	}
	merged, err := h.mergedPricing()
	if err != nil {
		fail()
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, merged)
}
