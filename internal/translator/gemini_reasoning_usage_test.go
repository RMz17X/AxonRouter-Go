package translator

import (
	json "encoding/json/v2"
	"math"
	"testing"

	"axonrouter/proxy/internal/pricing"
)

// geminiReasoningBody is a Gemini-native answer whose reasoning slice is
// reported SEPARATE from the output count: candidatesTokenCount is the
// visible answer (1000), the reasoning lives in
// candidatesTokenDetails.reasoningTokens (800).
const geminiReasoningBody = `{
	"candidates": [
		{
			"content": {
				"parts": [{"text": "Hello, world!"}],
				"role": "model"
			},
			"finishReason": "STOP"
		}
	],
	"usageMetadata": {
		"promptTokenCount": 100,
		"candidatesTokenCount": 1000,
		"candidatesTokenDetails": {
			"reasoningTokens": 800
		}
	}
}`

// geminiReasoningRates is the worked example of the pricing tests: prompt
// 100, completion 1000, reasoning 800, input rate 1, output rate 10, reasoning
// rate 10.
//
//   - additive (what Gemini must be billed as): 100*1 + 1000*10 + 800*10
//     = 18100 per million → 0.0181
//   - subset (what the OpenAI families must be billed as): 100*1 +
//     200*10 + 800*10 = 10100 per million → 0.0101
var geminiReasoningRates = pricing.ModelPricing{
	InputPer1M:     1,
	OutputPer1M:    10,
	ReasoningPer1M: 10,
}

func geminiReasoningCost(u *OpenAIUsage) float64 {
	return pricing.CalculateCost(pricing.TokenCounts{
		PromptTokens:             u.PromptTokens,
		CompletionTokens:         u.CompletionTokens,
		ReasoningTokens:          u.ReasoningTokens(),
		ReasoningIncludedInOutput: u.ReasoningIncludedInOutput,
	}, geminiReasoningRates)
}

// TestGeminiNonStreamReasoningStaysAdditive pins the money on the
// Gemini-native non-stream lane.
//
// Gemini reports the reasoning slice apart from the output count, so the
// reasoning has to be billed ON TOP of the output. The translated Chat
// Completions body used to carry no total_tokens, which made the subset
// detector fall back to "the reasoning is inside the output" and undercharged
// the turn by its whole reasoning slice (0.0101 instead of 0.0181 on the
// worked example — the exact opposite of what #5634 fixed for OpenAI).
//
// The body now carries the total, so the detector reads the additive shape
// (total == prompt + completion + reasoning) on every lane that consumes it.
func TestGeminiNonStreamReasoningStaysAdditive(t *testing.T) {
	openaiBody, directUsage, err := TranslateGeminiResponseToOpenAI([]byte(geminiReasoningBody))
	if err != nil {
		t.Fatalf("TranslateGeminiResponseToOpenAI: %v", err)
	}

	if directUsage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", directUsage.PromptTokens)
	}
	if directUsage.CompletionTokens != 1000 {
		t.Errorf("CompletionTokens = %d, want 1000: the output count must stay the candidate count", directUsage.CompletionTokens)
	}
	if directUsage.ReasoningTokens() != 800 {
		t.Errorf("ReasoningTokens = %d, want 800", directUsage.ReasoningTokens())
	}
	if directUsage.ReasoningIncludedInOutput {
		t.Error("ReasoningIncludedInOutput = true, want false: Gemini reports the reasoning apart from the output")
	}
	// The total has to be the additive one, or the detector cannot tell the
	// families apart.
	if want := 100 + 1000 + 800; directUsage.TotalTokens != want {
		t.Errorf("TotalTokens = %d, want %d (prompt + completion + reasoning)", directUsage.TotalTokens, want)
	}

	// The wire body has to carry that total as well: the Claude and the
	// /v1/responses lanes read the usage back out of it.
	var wire struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(openaiBody, &wire); err != nil {
		t.Fatalf("unmarshal translated body: %v", err)
	}
	total, ok := wire.Usage["total_tokens"].(float64)
	if !ok || int(total) != 1900 {
		t.Errorf("wire total_tokens = %v, want 1900", wire.Usage["total_tokens"])
	}

	// Lane 1: the /v1/responses lane parses the translated body back
	// (handlers/chat/forward.go respondAsResponses → ParseResponseUsage).
	parsed := ParseResponseUsage(openaiBody)
	if parsed == nil {
		t.Fatal("ParseResponseUsage returned nil for the translated body")
	}
	if parsed.ReasoningIncludedInOutput {
		t.Error("parsed ReasoningIncludedInOutput = true, want false on the Gemini lane")
	}
	if got := geminiReasoningCost(parsed); math.Abs(got-0.0181) > 1e-12 {
		t.Errorf("cost = %v, want 0.0181 (the reasoning slice is billed on top)", got)
	}

	// Lane 2: the Claude lane re-translates the same body
	// (handlers/chat/gemini_handler.go → TranslateOpenAIToClaude).
	_, claudeUsage, err := TranslateOpenAIToClaude(openaiBody)
	if err != nil {
		t.Fatalf("TranslateOpenAIToClaude: %v", err)
	}
	if claudeUsage.ReasoningIncludedInOutput {
		t.Error("Claude usage ReasoningIncludedInOutput = true, want false on the Gemini lane")
	}
	if got := geminiReasoningCost(claudeUsage); math.Abs(got-0.0181) > 1e-12 {
		t.Errorf("cost = %v, want 0.0181 (the reasoning slice is billed on top)", got)
	}
}

// TestOpenAINonStreamReasoningStaysSubset is the counterpart: the OpenAI
// families nest the reasoning slice INSIDE the completion count, so the same
// numbers have to be billed once (0.0101). It guards #5634 against a fix that
// would make every usage additive.
func TestOpenAINonStreamReasoningStaysSubset(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":1000,"total_tokens":1100,` +
		`"completion_tokens_details":{"reasoning_tokens":800}}}`)

	parsed := ParseResponseUsage(body)
	if parsed == nil {
		t.Fatal("ParseResponseUsage returned nil")
	}
	if !parsed.ReasoningIncludedInOutput {
		t.Error("ReasoningIncludedInOutput = false, want true: the OpenAI families nest the reasoning in the output")
	}
	if got := geminiReasoningCost(parsed); math.Abs(got-0.0101) > 1e-12 {
		t.Errorf("cost = %v, want 0.0101 (the reasoning slice is billed once)", got)
	}

	// The same body on the Claude lane.
	openaiBody := []byte(`{"id":"chatcmpl-x","object":"chat.completion","created":1,"model":"gpt-5",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":1000,"total_tokens":1100,` +
		`"completion_tokens_details":{"reasoning_tokens":800}}}`)
	_, claudeUsage, err := TranslateOpenAIToClaude(openaiBody)
	if err != nil {
		t.Fatalf("TranslateOpenAIToClaude: %v", err)
	}
	if !claudeUsage.ReasoningIncludedInOutput {
		t.Error("Claude usage ReasoningIncludedInOutput = false, want true on the OpenAI lane")
	}
	if got := geminiReasoningCost(claudeUsage); math.Abs(got-0.0101) > 1e-12 {
		t.Errorf("cost = %v, want 0.0101 (the reasoning slice is billed once)", got)
	}
}

// TestGeminiUsageMetadataCounters pins the counter resolution itself: the
// output count stays the candidate count, the reasoning is kept apart, and
// the total is the additive one.
func TestGeminiUsageMetadataCounters(t *testing.T) {
	tests := []struct {
		name          string
		metadata      string
		wantPrompt    int
		wantOutput    int
		wantReasoning int
		wantTotal     int
	}{
		{
			name: "reasoning in the candidate details",
			metadata: `{"promptTokenCount":100,"candidatesTokenCount":1000,` +
				`"candidatesTokenDetails":{"reasoningTokens":800}}`,
			wantPrompt: 100, wantOutput: 1000, wantReasoning: 800, wantTotal: 1900,
		},
		{
			name: "reasoning in thoughtsTokenCount",
			metadata: `{"promptTokenCount":100,"candidatesTokenCount":1000,` +
				`"thoughtsTokenCount":800}`,
			wantPrompt: 100, wantOutput: 1000, wantReasoning: 800, wantTotal: 1900,
		},
		{
			name: "no reasoning reported",
			metadata: `{"promptTokenCount":100,"candidatesTokenCount":1000}`,
			wantPrompt: 100, wantOutput: 1000, wantReasoning: 0, wantTotal: 1100,
		},
		{
			name: "output derived from the reported total",
			metadata: `{"promptTokenCount":100,"totalTokenCount":1900,` +
				`"thoughtsTokenCount":800}`,
			wantPrompt: 100, wantOutput: 1000, wantReasoning: 800, wantTotal: 1900,
		},
		{
			name: "cached tokens are carried over",
			metadata: `{"promptTokenCount":100,"candidatesTokenCount":1000,` +
				`"cachedContentTokenCount":40,"candidatesTokenDetails":{"reasoningTokens":800}}`,
			wantPrompt: 100, wantOutput: 1000, wantReasoning: 800, wantTotal: 1900,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta GeminiUsageMetadata
			if err := json.Unmarshal([]byte(tt.metadata), &meta); err != nil {
				t.Fatalf("unmarshal usage metadata: %v", err)
			}
			got := geminiUsageFromMetadata(&meta)
			if got.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", got.PromptTokens, tt.wantPrompt)
			}
			if got.CompletionTokens != tt.wantOutput {
				t.Errorf("CompletionTokens = %d, want %d", got.CompletionTokens, tt.wantOutput)
			}
			if got.ReasoningTokens != tt.wantReasoning {
				t.Errorf("ReasoningTokens = %d, want %d", got.ReasoningTokens, tt.wantReasoning)
			}
			if got.TotalTokens != tt.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", got.TotalTokens, tt.wantTotal)
			}
		})
	}
}

// TestGeminiUsageWithoutMetadataIsEmpty guards the nil path: a response
// without a usage block must not produce a phantom total.
func TestGeminiUsageWithoutMetadataIsEmpty(t *testing.T) {
	got := geminiUsageFromMetadata(nil)
	if got.TotalTokens != 0 || got.PromptTokens != 0 || got.CompletionTokens != 0 || got.ReasoningTokens != 0 {
		t.Errorf("usage from a nil metadata = %+v, want the zero value", got)
	}
	if _, present := got.openAIUsageMap()["total_tokens"]; present {
		t.Error("the usage map carries a total the upstream never reported")
	}
}

// TestGeminiStreamLaneUnaffected pins the other half of the contract: the
// Gemini STREAM lane must keep its old behaviour.
//
// A stream chunk reports no reasoning slice (the Gemini usage block on a
// chunk carries no reasoning detail), so the reasoning count is zero and the
// marker stays false there — the stream lane must not be dragged along by the
// non-stream fix.
func TestGeminiStreamLaneUnaffected(t *testing.T) {
	chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP","index":0}],` +
		`"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1000}}`)

	state := &GeminiStreamState{MessageId: "chatcmpl-fixed", Model: "gemini-3-pro-preview"}
	out, err := TranslateGeminiChunkToOpenAI(chunk, state)
	if err != nil {
		t.Fatalf("TranslateGeminiChunkToOpenAI: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("TranslateGeminiChunkToOpenAI returned no payload")
	}
	if state.Usage == nil {
		t.Fatal("the stream state carries no usage")
	}
	if state.Usage.ReasoningTokens() != 0 {
		t.Errorf("ReasoningTokens = %d, want 0 on the stream lane", state.Usage.ReasoningTokens())
	}
	if state.Usage.ReasoningIncludedInOutput {
		t.Error("ReasoningIncludedInOutput = true, want false: the stream lane has no reasoning slice")
	}
}
