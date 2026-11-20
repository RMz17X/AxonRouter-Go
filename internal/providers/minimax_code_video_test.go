package providers

import (
	"slices"
	"testing"
)

func TestMiniMaxCodeGlobalVideoRegistration(t *testing.T) {
	const canonical = "minimax-code-global"
	if got := ResolveAlias("mmg"); got != canonical {
		t.Fatalf("mmg resolves to %q, want %q", got, canonical)
	}
	cfg, ok := KnownProviders[canonical]
	if !ok {
		t.Fatalf("%s missing from KnownProviders", canonical)
	}
	if cfg.VideoURL == "" {
		t.Fatalf("%s has no VideoURL", canonical)
	}
	for _, model := range []string{"MiniMax-H3", "MiniMax-H3-Highspeed", "MiniMax-H3-Lite", "MiniMax-H3-Max"} {
		if !slices.Contains(ProviderModels[canonical], model) {
			t.Errorf("%s missing from %s catalog", model, canonical)
		}
		if kind := GetProviderModelKind(canonical, model); kind != "video" {
			t.Errorf("%s kind = %q, want video", model, kind)
		}
		if kind := GetProviderModelKind("mmg", model); kind != "video" {
			t.Errorf("mmg/%s kind = %q, want video", model, kind)
		}
	}
	for _, model := range []string{"MiniMax-M3", "MiniMax-M2.7"} {
		if GetProviderModelKind(canonical, model) == "video" {
			t.Errorf("chat model %s was reclassified as video", model)
		}
	}
}
