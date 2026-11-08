package providers

import (
	"testing"
)

func TestLiquidProviderRegistration(t *testing.T) {
	// 1. Verify KnownProviders has liquid with dual endpoints
	cfg, ok := KnownProviders["liquid"]
	if !ok {
		t.Fatal("KnownProviders missing 'liquid'")
	}
	if cfg.BaseURL != "https://api.liquid.ai/openai/v1/chat/completions" {
		t.Errorf("expected BaseURL https://api.liquid.ai/openai/v1/chat/completions, got %s", cfg.BaseURL)
	}
	if cfg.SystemoneURL != "https://api.liquid.ai/decisions/v1/systemone" {
		t.Errorf("expected SystemoneURL https://api.liquid.ai/decisions/v1/systemone, got %s", cfg.SystemoneURL)
	}
	if cfg.AuthHeader != "Authorization" || cfg.AuthScheme != "bearer" {
		t.Errorf("unexpected auth config: %s %s", cfg.AuthHeader, cfg.AuthScheme)
	}

	// 2. Verify ProviderRegistry metadata
	reg, ok := ProviderRegistry["liquid"]
	if !ok {
		t.Fatal("ProviderRegistry missing 'liquid'")
	}
	if reg.ID != "liquid" || reg.Name != "Liquid AI" {
		t.Errorf("unexpected registry ID/Name: %s / %s", reg.ID, reg.Name)
	}
	hasLLM := false
	hasSystemone := false
	for _, k := range reg.ServiceKinds {
		if k == "llm" {
			hasLLM = true
		}
		if k == "systemone" {
			hasSystemone = true
		}
	}
	if !hasLLM || !hasSystemone {
		t.Errorf("expected ServiceKinds to contain llm and systemone, got %v", reg.ServiceKinds)
	}

	// 3. Verify models in ProviderModels
	models, ok := ProviderModels["liquid"]
	if !ok {
		t.Fatal("ProviderModels missing 'liquid'")
	}
	expectedModels := map[string]bool{
		"lfm2.5-24b-a2b": true,
		"lfm3-1.7b":      true,
		"lfm3-vl-2b":     true,
		"d1":             true,
		"d1.1":           true,
		"d1-ultra":       true,
	}
	for _, m := range models {
		delete(expectedModels, m)
	}
	if len(expectedModels) > 0 {
		t.Errorf("missing models from ProviderModels: %v", expectedModels)
	}

	// 4. Verify systemone kind in ProviderModelKinds
	kinds, ok := ProviderModelKinds["liquid"]
	if !ok {
		t.Fatal("ProviderModelKinds missing 'liquid'")
	}
	for _, m := range []string{"d1", "d1.1", "d1-ultra"} {
		if kinds[m] != "systemone" {
			t.Errorf("expected kind 'systemone' for model %s, got %s", m, kinds[m])
		}
	}

	// 5. Verify capabilities
	caps24b := modelCapabilities["lfm2.5-24b-a2b"]
	if !caps24b.Reasoning {
		t.Errorf("expected lfm2.5-24b-a2b Reasoning=true")
	}
	capsVl := modelCapabilities["lfm3-vl-2b"]
	if !capsVl.Vision {
		t.Errorf("expected lfm3-vl-2b Vision=true")
	}
}
