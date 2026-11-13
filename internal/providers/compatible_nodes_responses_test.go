package providers

import "testing"

// Upstream #5611: a custom openai-compatible node on the Responses wire is
// recognised by its DECLARED apiType, not by the URL it happens to dial.
//
// Upstream reads the declaration (getTargetFormat →
// resolveOpenAICompatibleApiType), and the fork has to as well: the lane is
// appended to the node's baseUrl prefix, so the resolved URL names the lane
// only while the connection row kept the apiType in sync. A connection
// written before that sync resolves to a URL that says nothing about the
// wire, and a request that then believes it is on the Chat lane sends a
// top-level reasoning_effort to an endpoint that answers 400.

// TestCompatibleNodeSpeaksResponses pins the declaration-driven answer.
func TestCompatibleNodeSpeaksResponses(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		apiType  string
		want     bool
	}{
		{"a responses apiType makes the node serve Responses", "openai-compatible-chat-acme", "responses", true},
		{"a chat apiType keeps the node on the Chat wire", "openai-compatible-chat-acme", "chat", false},
		{"no apiType means the Chat wire", "openai-compatible-chat-acme", "", false},
		// Upstream's fallback: a node created before the dashboard began
		// syncing apiType carries none, and its id is all that says so.
		{"a responses id without an apiType still serves Responses", "openai-compatible-responses-acme", "", true},
		{"an anthropic-compatible node never speaks Responses", "anthropic-compatible-acme", "responses", false},
		{"a registry provider never speaks Responses here", "openai", "responses", false},
		{"a custom-embedding node never speaks Responses", "custom-embedding-acme", "responses", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompatibleNodeSpeaksResponses(tt.provider, tt.apiType); got != tt.want {
				t.Errorf("CompatibleNodeSpeaksResponses(%q, %q) = %v, want %v",
					tt.provider, tt.apiType, got, tt.want)
			}
		})
	}
}

// TestIsOpenAICompatibleNode pins the node-type detection the helper relies
// on: only the openai-compatible family is a candidate, and the bare type
// without a suffix is not a node id.
func TestIsOpenAICompatibleNode(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"openai-compatible-chat-acme", true},
		{"openai-compatible-responses-acme", true},
		{"anthropic-compatible-acme", false},
		{"custom-embedding-acme", false},
		{"openai-compatible", false},
		{"openai", false},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := IsOpenAICompatibleNode(tt.provider); got != tt.want {
				t.Errorf("IsOpenAICompatibleNode(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}
