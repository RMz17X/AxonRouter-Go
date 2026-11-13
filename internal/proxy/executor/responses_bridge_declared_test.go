package executor

import (
	"testing"

	"axonrouter/proxy/internal/providers"
)

// Upstream #5611: a custom openai-compatible node on the Responses wire is
// recognised by the wire format its config DECLARES, not only by a base URL
// that happens to end in "/responses".
//
// The distinction matters because the lane is appended to the node's baseUrl
// prefix, so the resolved URL names the lane only while the connection row
// kept the apiType in sync. When it did not, the node was believed to speak
// Chat Completions, the thinking pass wrote a top-level reasoning_effort, and
// the upstream answered 400 ("moved to reasoning.effort").

// TestUpstreamSpeaksResponses_DeclaredFormatWins pins that a declared
// Responses wire format is recognised even when the base URL does not name
// the lane.
func TestUpstreamSpeaksResponses_DeclaredFormatWins(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		cfg      *providers.ProviderConfig
		want     bool
	}{
		{
			name:     "a declared Responses format is recognised without a lane in the URL",
			provider: "openai-compatible-chat-acme",
			model:    "gpt-5",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.example.com/v1", Format: providers.FormatOpenAIResponses},
			want:     true,
		},
		{
			name:     "a declared Chat format is not Responses",
			provider: "openai-compatible-chat-acme",
			model:    "gpt-5",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/chat/completions", Format: "openai"},
			want:     false,
		},
		{
			name:     "the Claude wire format is not Responses",
			provider: "anthropic-compatible-acme",
			model:    "claude-sonnet-5",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/messages", Format: providers.FormatClaude},
			want:     false,
		},
		{
			// The URL alone still counts: that is how the registry providers
			// that end on the /responses endpoint are recognised.
			name:     "a base URL that names the lane is still Responses",
			provider: "openai-compatible-chat-acme",
			model:    "gpt-5",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/responses"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UpstreamSpeaksResponses(tt.provider, tt.model, tt.cfg); got != tt.want {
				t.Errorf("UpstreamSpeaksResponses(%q, %q, %+v) = %v, want %v",
					tt.provider, tt.model, tt.cfg, got, tt.want)
			}
		})
	}
}
