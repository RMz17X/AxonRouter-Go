package executor

import "testing"

func TestEffectiveOpenCodeMaxTokens(t *testing.T) {
	tests := []struct {
		name      string
		requested int
		provider  string
		model     string
		reasoning bool
		want      int
	}{
		{
			name:      "reasoning request below the answer floor gets floor plus thinking reserve",
			requested: 50,
			provider:  "opencode",
			model:     "big-pickle",
			reasoning: true,
			want:      5120,
		},
		{
			name:      "reasoning request above the floor keeps its value plus reserve",
			requested: 2000,
			provider:  "opencode",
			model:     "big-pickle",
			reasoning: true,
			want:      6096,
		},
		{
			name:      "plain request below the floor is raised to 512",
			requested: 50,
			provider:  "opencode",
			model:     "lfm3-1.7b",
			want:      512,
		},
		{
			name:      "plain request above the floor is untouched",
			requested: 2000,
			provider:  "opencode",
			model:     "lfm3-1.7b",
			want:      2000,
		},
		{
			name:      "capability ceiling clamps the reasoning floor",
			requested: 5000,
			provider:  "opencode",
			model:     "lfm2.5-24b-a2b",
			reasoning: true,
			want:      8192,
		},
		{
			name:      "unknown model uses the global ceiling",
			requested: 200000,
			provider:  "opencode",
			model:     "totally-unknown-model",
			want:      131072,
		},
		{
			name:      "known large ceiling is still capped globally",
			requested: 200000,
			provider:  "opencode",
			model:     "space-bunny-free",
			want:      131072,
		},
		{
			name:      "zero budget means the client declared none",
			requested: 0,
			provider:  "opencode",
			model:     "big-pickle",
			reasoning: true,
			want:      0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveOpenCodeMaxTokens(tt.requested, tt.provider, tt.model, tt.reasoning)
			if got != tt.want {
				t.Errorf("effectiveOpenCodeMaxTokens(%d, %q, %q, %v) = %d, want %d",
					tt.requested, tt.provider, tt.model, tt.reasoning, got, tt.want)
			}
		})
	}
}

func TestOpenCodeIsReasoningRequest(t *testing.T) {
	tests := []struct {
		name     string
		reqMap   map[string]any
		provider string
		model    string
		want     bool
	}{
		{"explicit effort wins over the catalog", map[string]any{"reasoning_effort": "high"}, "opencode", "lfm3-1.7b", true},
		{"effort none does not imply reasoning", map[string]any{"reasoning_effort": "none"}, "opencode", "lfm3-1.7b", false},
		{"catalog reasoning model", map[string]any{}, "opencode", "big-pickle", true},
		{"catalog plain model", map[string]any{}, "opencode", "lfm3-1.7b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openCodeIsReasoningRequest(tt.reqMap, tt.provider, tt.model); got != tt.want {
				t.Errorf("openCodeIsReasoningRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyOpenCodeTokenFloor(t *testing.T) {
	t.Run("rewrites max_tokens into the cli spelling", func(t *testing.T) {
		reqMap := map[string]any{"max_tokens": float64(32)}
		applyOpenCodeTokenFloor(reqMap, "opencode", "big-pickle")
		if _, ok := reqMap["max_tokens"]; ok {
			t.Error("max_tokens must be removed once max_completion_tokens is set")
		}
		if got := reqMap["max_completion_tokens"]; got != 5120 {
			t.Errorf("max_completion_tokens = %v, want 5120", got)
		}
	})

	t.Run("string budget is honoured", func(t *testing.T) {
		reqMap := map[string]any{"max_tokens": "32"}
		applyOpenCodeTokenFloor(reqMap, "opencode", "lfm3-1.7b")
		if got := reqMap["max_completion_tokens"]; got != 512 {
			t.Errorf("max_completion_tokens = %v, want 512", got)
		}
	})

	t.Run("existing max_completion_tokens is raised in place", func(t *testing.T) {
		reqMap := map[string]any{"max_completion_tokens": float64(100)}
		applyOpenCodeTokenFloor(reqMap, "opencode", "lfm3-1.7b")
		if got := reqMap["max_completion_tokens"]; got != 512 {
			t.Errorf("max_completion_tokens = %v, want 512", got)
		}
	})

	t.Run("no declared budget stays untouched", func(t *testing.T) {
		reqMap := map[string]any{"model": "big-pickle"}
		applyOpenCodeTokenFloor(reqMap, "opencode", "big-pickle")
		if _, ok := reqMap["max_completion_tokens"]; ok {
			t.Error("the floor must not invent a budget")
		}
	})
}
