package chat

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/proxy/executor"
)

// executorName names the function behind an executor value. Go cannot compare
// function values, but the runtime knows the symbol behind the code pointer.
func executorName(exec executor.Executor) string {
	if exec == nil {
		return ""
	}
	fn := runtime.FuncForPC(reflect.ValueOf(exec).Pointer())
	if fn == nil {
		return ""
	}
	return fn.Name()
}

// TestResolveExecutor covers the dispatch choice behind upstream luqman-v1
// PR #930: a custom openai-compatible node whose stored apiType is "responses"
// has its lane moved to /responses, but its provider id is dynamic
// ("openai-compatible-responses-<suffix>") and so can never claim a registered
// executor. Without the fallback the request reached the generic chat
// forwarder, which posts messages[] and nested tools to an endpoint that only
// accepts input[] and flat tools ("function tool name is required").
func TestResolveExecutor(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		wireModel      string
		cfg            *providers.ProviderConfig
		claudeNative   bool
		upstreamClaude bool
		wantResponses  bool
	}{
		{
			name:          "a responses-lane compatible node uses the Responses forwarder",
			provider:      "openai-compatible-responses-studio",
			wireModel:     "gemini-4-argon",
			cfg:           &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/responses"},
			wantResponses: true,
		},
		{
			name:          "a plain compatible node keeps the generic chat path",
			provider:      "openai-compatible-chat-studio",
			wireModel:     "some-model",
			cfg:           &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/chat/completions"},
			wantResponses: false,
		},
		{
			name:          "a Claude-wire request keeps the generic chat path",
			provider:      "openai-compatible-responses-studio",
			wireModel:     "gemini-4-argon",
			cfg:           &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/responses"},
			claudeNative:  true,
			wantResponses: false,
		},
		{
			name:           "a Claude upstream keeps the generic chat path",
			provider:       "openai-compatible-responses-studio",
			wireModel:      "gemini-4-argon",
			cfg:            &providers.ProviderConfig{BaseURL: "https://api.example.com/v1/responses"},
			upstreamClaude: true,
			wantResponses:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := resolveExecutor(tt.provider, tt.wireModel, tt.cfg, tt.claudeNative, tt.upstreamClaude)
			if tt.wantResponses {
				if exec == nil {
					t.Fatal("expected an executor, got nil")
				}
				// The Responses forwarder is the only one that speaks the
				// Responses body.
				if got := executorName(exec); !strings.HasSuffix(got, "ForwardCompatibleResponses") {
					t.Errorf("executor = %q, want ForwardCompatibleResponses", got)
				}
				return
			}
			if exec != nil {
				t.Errorf("expected no executor, got %q", executorName(exec))
			}
		})
	}
}
