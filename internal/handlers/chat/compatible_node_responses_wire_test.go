package chat

import (
	"testing"

	"axonrouter/proxy/internal/providers"
)

// Upstream #5611: a custom openai-compatible node on the Responses wire must
// have that wire stamped onto its resolved config, because the wire is what
// every downstream reader consults.
//
// The stamp is what makes three things agree: the thinking pass nests the
// effort under reasoning.effort (a top-level reasoning_effort is rejected
// with 400), the executor picks the Responses forwarder, and the token-field
// normaliser stays off. A node whose declaration was not stamped was believed
// to speak Chat Completions, and the whole turn died on the upstream's 400.

// TestGetProviderConfig_StampsResponsesWireForCompatibleNode pins that a
// compatible node declared as "responses" gets the Responses wire format.
func TestGetProviderConfig_StampsResponsesWireForCompatibleNode(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		apiType    string
		wantFormat string
	}{
		{
			name:       "a responses apiType stamps the Responses wire",
			provider:   "openai-compatible-chat-acme",
			apiType:    "responses",
			wantFormat: providers.FormatOpenAIResponses,
		},
		{
			name:       "a chat apiType leaves the wire unset",
			provider:   "openai-compatible-chat-acme",
			apiType:    "chat",
			wantFormat: "",
		},
		{
			// A node created before the dashboard began syncing apiType carries
			// none, and its id is all that says so — upstream's fallback.
			name:       "a responses id without an apiType stamps the Responses wire",
			provider:   "openai-compatible-responses-acme",
			apiType:    "",
			wantFormat: providers.FormatOpenAIResponses,
		},
		{
			name:       "an anthropic-compatible node keeps the Claude wire",
			provider:   "anthropic-compatible-acme",
			apiType:    "",
			wantFormat: providers.FormatClaude,
		},
		{
			name:       "a plain compatible node stays on the Chat wire",
			provider:   "openai-compatible-chat-acme",
			apiType:    "",
			wantFormat: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &ChatHandler{}
			psd := map[string]any{}
			if tt.apiType != "" {
				psd["apiType"] = tt.apiType
			}
			connData := &ConnectionData{
				APIKey:               "sk-test",
				BaseURL:              "https://api.example.com/proxy/v1",
				ProviderSpecificData: psd,
			}

			cfg, err := h.getProviderConfig(tt.provider, connData)
			if err != nil {
				t.Fatalf("getProviderConfig: %v", err)
			}
			if cfg.Format != tt.wantFormat {
				t.Errorf("resolved Format = %q, want %q", cfg.Format, tt.wantFormat)
			}
		})
	}
}

// TestGetProviderConfig_StampedWireReachesTheResponsesDecision pins the link
// between the two halves: the stamped wire is what makes the request path
// treat the node as a Responses upstream.
func TestGetProviderConfig_StampedWireReachesTheResponsesDecision(t *testing.T) {
	h := &ChatHandler{}
	connData := &ConnectionData{
		APIKey:  "sk-test",
		BaseURL: "https://api.example.com/proxy/v1",
		ProviderSpecificData: map[string]any{
			"apiType": "responses",
		},
	}
	cfg, err := h.getProviderConfig("openai-compatible-chat-acme", connData)
	if err != nil {
		t.Fatalf("getProviderConfig: %v", err)
	}
	if cfg.Format != providers.FormatOpenAIResponses {
		t.Fatalf("resolved Format = %q, want %q", cfg.Format, providers.FormatOpenAIResponses)
	}
	// The lane the request is dialled must be the Responses one too, or the
	// body and the route disagree — the drift that produced "function tool
	// name is required".
	if want := "https://api.example.com/proxy/v1/responses"; cfg.BaseURL != want {
		t.Errorf("resolved BaseURL = %q, want %q", cfg.BaseURL, want)
	}
}
