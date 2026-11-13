package thinking

import "testing"

// Upstream #5611: a custom OpenAI-compatible node on the Responses wire must
// nest the effort under reasoning.effort, never send it top-level.
//
// The Responses API rejects a top-level reasoning_effort with 400 ("moved to
// reasoning.effort"), and the model's capability says "openai" (the Chat
// wire) for the whole gpt-5 family — so the wire the request goes out on has
// to win over the capability, or the whole turn dies.
//
// The provider isolation rule is the other half of the contract: the nesting
// applies to the RESPONSES lane only. A compatible node on the Chat wire
// keeps the top-level field, because that is what a Chat Completions
// endpoint accepts.

// TestOpenAICompatibleNode_ResponsesLaneNestsEffort pins the Responses lane:
// a compatible node whose model is gpt-5* reads reasoning:{effort,summary}.
func TestOpenAICompatibleNode_ResponsesLaneNestsEffort(t *testing.T) {
	out := apply(t, "openai-responses", "openai-compatible-responses-test", "gpt-5(high)",
		map[string]any{"model": "gpt-5(high)"}, nil)
	mustAbsent(t, out, "reasoning_effort")
	r, _ := out["reasoning"].(map[string]any)
	if r["effort"] != "high" {
		t.Fatalf("reasoning = %+v, want effort high", out["reasoning"])
	}
	if r["summary"] != "auto" {
		t.Fatalf("reasoning = %+v, want summary auto", out["reasoning"])
	}
}

// TestOpenAICompatibleNode_ChatLaneKeepsTopLevelEffort pins the other
// direction, the one that makes the fix safe to apply: a compatible node on
// the Chat Completions wire keeps the top-level reasoning_effort, because
// that is the field a Chat endpoint accepts. A fix that nested the effort
// everywhere would break this lane.
func TestOpenAICompatibleNode_ChatLaneKeepsTopLevelEffort(t *testing.T) {
	out := apply(t, "openai", "openai-compatible-chat-test", "gpt-5(high)",
		map[string]any{"model": "gpt-5(high)"}, nil)
	mustAbsent(t, out, "reasoning")
	if out["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", out["reasoning_effort"])
	}
}

// TestOpenAICompatibleNode_ClientReasoningStaysNested pins that a client
// that already sent reasoning.effort keeps it nested on the Responses lane.
func TestOpenAICompatibleNode_ClientReasoningStaysNested(t *testing.T) {
	out := apply(t, "openai-responses", "openai-compatible-responses-test", "gpt-5",
		map[string]any{"model": "gpt-5", "reasoning": map[string]any{"effort": "low"}}, nil)
	mustAbsent(t, out, "reasoning_effort")
	r, _ := out["reasoning"].(map[string]any)
	if r["effort"] != "low" {
		t.Fatalf("reasoning = %+v, want effort low", out["reasoning"])
	}
}

// TestOpenAICompatibleNode_ChatLaneIsUnaffectedByTheResponsesLane pins the
// isolation rule explicitly: the same provider on the Chat wire behaves
// exactly as it did before the Responses lane was recognised, so a fix that
// leaked across the lanes would be caught here.
func TestOpenAICompatibleNode_ChatLaneIsUnaffectedByTheResponsesLane(t *testing.T) {
	// The provider id says "responses" but the LANE is the Chat one: the
	// lane decides, not the id.
	out := apply(t, "openai", "openai-compatible-responses-test", "gpt-5(high)",
		map[string]any{"model": "gpt-5(high)"}, nil)
	mustAbsent(t, out, "reasoning")
	if out["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high — the Chat lane must keep the top-level field", out["reasoning_effort"])
	}
}
