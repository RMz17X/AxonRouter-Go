package dashboard

import (
	"strings"
	"testing"
)

// A new custom provider should get a human-readable id from its alias (node
// prefix) whenever that alias is a usable id suffix. The dashboard URL then
// reads /dashboard/providers/openai-compatible-chat-myalias instead of a UUID,
// which is what operators expect from "add provider with alias".
func TestUsableAliasSuffix(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		nodeType string
		apiType  string
		want     string
		wantOK   bool
	}{
		{name: "simple alias", prefix: "myalias", nodeType: "openai-compatible", apiType: "chat", want: "myalias", wantOK: true},
		{name: "trimmed alias", prefix: "  my-alias  ", nodeType: "openai-compatible", apiType: "chat", want: "my-alias", wantOK: true},
		{name: "dotted alias", prefix: "v2.1", nodeType: "openai-compatible", apiType: "chat", want: "v2.1", wantOK: true},
		{name: "slash is not an id suffix", prefix: "team/x", nodeType: "openai-compatible", apiType: "chat", wantOK: false},
		{name: "space is not an id suffix", prefix: "my alias", nodeType: "openai-compatible", apiType: "chat", wantOK: false},
		{name: "leading dot", prefix: ".hidden", nodeType: "openai-compatible", apiType: "chat", wantOK: false},
		{name: "provider literal", prefix: "openai-compatible-x", nodeType: "openai-compatible", apiType: "chat", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := usableAliasSuffix(tt.nodeType, tt.apiType, tt.prefix)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("usableAliasSuffix(%q) = (%q, %v), want (%q, %v)", tt.prefix, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestComposeProviderNodeIDFromAlias(t *testing.T) {
	id := composeProviderNodeID("openai-compatible", "chat", "myalias")
	if id != "openai-compatible-chat-myalias" {
		t.Errorf("id = %q, want openai-compatible-chat-myalias", id)
	}
	if !strings.HasPrefix(id, "openai-compatible-chat-") {
		t.Errorf("id %q lost the node literal", id)
	}
}
