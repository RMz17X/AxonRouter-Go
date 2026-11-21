package chat

import (
	"testing"

	"axonrouter/proxy/internal/providers"
)

// The dashboard publishes DeepAI Free under its alias prefix
// (web/src/lib/models.ts: "deepai-free": "daifree"). Resolution must return the
// canonical id: that is the key carrying NoAuth in KnownProviders and the
// executor registration. Leaving the alias unresolved answers every dashboard
// test request with 503 "no active connections for provider: daifree".
func TestResolveDeepAIFreeAlias(t *testing.T) {
	tests := []struct {
		model     string
		canonical string
	}{
		{"daifree/chat-standard", "deepai-free"},
		{"deepai-free/chat-standard", "deepai-free"},
		{"dai/glm-5.3-flash", "deepai"},
	}
	h := &ChatHandler{}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			info, err := h.resolveModel(tt.model)
			if err != nil {
				t.Fatalf("resolveModel(%q): %v", tt.model, err)
			}
			if info.Provider != tt.canonical {
				t.Fatalf("provider = %q, want %q", info.Provider, tt.canonical)
			}
			cfg, ok := providers.KnownProviders[info.Provider]
			if !ok {
				t.Fatalf("KnownProviders[%q] missing", info.Provider)
			}
			if tt.canonical == "deepai-free" && !cfg.NoAuth {
				t.Error("deepai-free must be a no-auth provider so the virtual connection is injected")
			}
		})
	}
}
