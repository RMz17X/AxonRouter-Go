package providers

import "testing"

func TestDeepAIAliases(t *testing.T) {
	tests := []struct {
		alias     string
		canonical string
		noAuth    bool
	}{
		{"dai", "deepai", false},
		{"daifree", "deepai-free", true},
	}
	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			if got := ResolveAlias(tt.alias); got != tt.canonical {
				t.Errorf("ResolveAlias(%q) = %q, want %q", tt.alias, got, tt.canonical)
			}
			if got := GetProviderAlias(tt.canonical); got != tt.alias {
				t.Errorf("GetProviderAlias(%q) = %q, want %q", tt.canonical, got, tt.alias)
			}
			cfg, ok := KnownProviders[tt.canonical]
			if !ok {
				t.Fatalf("KnownProviders[%q] missing", tt.canonical)
			}
			if cfg.NoAuth != tt.noAuth {
				t.Errorf("KnownProviders[%q].NoAuth = %v, want %v", tt.canonical, cfg.NoAuth, tt.noAuth)
			}
			meta, ok := ProviderRegistry[tt.canonical]
			if !ok || meta.Alias != tt.alias {
				t.Errorf("ProviderRegistry[%q].Alias = %q, want %q", tt.canonical, meta.Alias, tt.alias)
			}
		})
	}
}
