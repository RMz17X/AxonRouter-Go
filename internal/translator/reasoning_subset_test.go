package translator

import (
	json "encoding/json/v2"
	"testing"
)

// TestReasoningSubsetDetection covers the family check behind upstream
// decolua/9router #5634.
//
// OpenAI Chat Completions and the Responses API count reasoning_tokens INSIDE
// completion_tokens/output_tokens, so pricing has to take the slice out before
// it applies the output rate. A few OpenAI-compatible upstreams (xAI) report
// the reasoning ON TOP of the completion count instead, and those are
// recognisable because total_tokens equals prompt + completion + reasoning.
//
// The heuristic mirrors reasoningIsSubsetOfOutput in upstream
// open-sse/utils/usageTracking.js.
func TestReasoningSubsetDetection(t *testing.T) {
	tests := []struct {
		name string
		usage OpenAIUsage
		want bool
	}{
		{
			name: "OpenAI chat nests the reasoning slice in the output",
			usage: OpenAIUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
				CompletionTokensDetails: &CompletionTokensDetails{
					ReasoningTokens: 300,
				},
			},
			want: true,
		},
		{
			name: "xAI-style reporting keeps the reasoning on top",
			usage: OpenAIUsage{
				PromptTokens:     1000,
				CompletionTokens: 200,
				TotalTokens:      1500,
				CompletionTokensDetails: &CompletionTokensDetails{
					ReasoningTokens: 300,
				},
			},
			want: false,
		},
		{
			name: "no reasoning reported is not a subset",
			usage: OpenAIUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
			want: false,
		},
		{
			name: "no total keeps the subset reading",
			usage: OpenAIUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				CompletionTokensDetails: &CompletionTokensDetails{
					ReasoningTokens: 300,
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.ReasoningIsSubsetOfOutput()
			if got != tt.want {
				t.Errorf("ReasoningIsSubsetOfOutput() = %v, want %v", got, tt.want)
			}
			tt.usage.MarkReasoningSubset()
			if tt.usage.ReasoningIncludedInOutput != tt.want {
				t.Errorf("ReasoningIncludedInOutput = %v, want %v", tt.usage.ReasoningIncludedInOutput, tt.want)
			}
		})
	}
}

// TestParseResponseUsageMarksReasoningSubset pins the marker where the OpenAI
// usage is parsed: a Chat Completions body that reports reasoning inside the
// completion count must come out marked, so pricing bills the slice once.
func TestParseResponseUsageMarksReasoningSubset(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,` +
		`"completion_tokens_details":{"reasoning_tokens":300}}}`)

	usage := ParseResponseUsage(body)
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}
	if usage.TotalTokens != 1500 {
		t.Errorf("TotalTokens = %d, want 1500", usage.TotalTokens)
	}
	if !usage.ReasoningIncludedInOutput {
		t.Error("ReasoningIncludedInOutput = false, want true: the reasoning slice sits inside the output count")
	}
}

// TestParseResponsesUsageMarksReasoningSubset pins the same marker on the
// Responses envelope, whose output_tokens always includes the reasoning slice.
func TestParseResponsesUsageMarksReasoningSubset(t *testing.T) {
	body := []byte(`{"usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500,` +
		`"output_tokens_details":{"reasoning_tokens":300}}}`)

	usage := ParseResponsesUsage(body)
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}
	if usage.ReasoningTokens() != 300 {
		t.Errorf("ReasoningTokens() = %d, want 300", usage.ReasoningTokens())
	}
	if !usage.ReasoningIncludedInOutput {
		t.Error("ReasoningIncludedInOutput = false, want true: the Responses output count includes the reasoning slice")
	}
}

// TestOpenAIUsageOmitsUnreportedTotal pins the wire shape of the usage
// struct.
//
// The struct is marshaled straight to the client on the translated lanes (the
// Claude-to-Chat chunk writer in claude_response.go), so a field the upstream
// never reported must not reach the client as a zero. A "total_tokens": 0
// beside a non-zero prompt and completion count contradicts them and pins the
// client's context meter to zero — the exact symptom the reported-total
// detection in PatchPromptUsageFloor works around.
//
// The marshaling here is encoding/json/v2, the one the production code uses:
// under it omitempty does NOT skip a zero number, so the field needs omitzero.
// A test written against encoding/json/v1 would pass with omitempty and hide
// the leak.
func TestOpenAIUsageOmitsUnreportedTotal(t *testing.T) {
	encoded, err := json.Marshal(&OpenAIUsage{PromptTokens: 208, CompletionTokens: 11})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	if _, present := doc["total_tokens"]; present {
		t.Errorf("usage JSON carries a total the upstream never reported: %s", encoded)
	}

	// A total the upstream DID report has to survive the round trip.
	encoded, err = json.Marshal(&OpenAIUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	if got, _ := doc["total_tokens"].(float64); int(got) != 1500 {
		t.Errorf("total_tokens = %v, want 1500: the reported total must survive", doc["total_tokens"])
	}
}
