package executor

import (
	json "encoding/json/v2"
)

// reasoningHistoryFields are the vendor-specific thinking fields clients
// replay on assistant turns. Strict OpenAI-compatible validators reject them
// with 400 ("unrecognized request argument") or 422 ("extra_forbidden"):
// Mistral's hosted DeepSeek/GLM lanes answer 422 on the second turn of any
// conversation where the client echoes the previous assistant's thinking.
//
// The field names mirror upstream decolua/9router's paramSupport
// dropMessageFields rule (PR #4220). A provider opts into the quirk through
// stripReasoningContentProviders instead of ad-hoc model-string checks: the
// decision is made from the registered provider id at the dispatch choke
// point, so no sibling provider's history is touched.
var reasoningHistoryFields = [...]string{
	"reasoning_content",
	"reasoning",
	"reasoning_details",
}

// stripReasoningContentProviders is the set of providers whose upstream
// rejects reasoning fields inside assistant history. Mistral Vibe hosts
// DeepSeek/GLM models on the public Mistral endpoint, whose strict validator
// answers 422 on a replayed reasoning trace. Native deepseek/glm lanes are
// deliberately absent: their own API requires the field back on assistant
// turns (the InjectReasoningContent placeholder satisfies that), so stripping
// there would turn a working lane into a 400. Ports upstream PR #4220's
// dropMessageFields rule without collapsing the two contracts together.
var stripReasoningContentProviders = map[string]struct{}{
	"mistral-vibe": {},
}

// StripReasoningContentFromHistory removes replayed reasoning fields from
// assistant messages before the body goes upstream. Non-assistant turns are
// left alone, and any other message field (tool_calls, content, refusal) is
// passed through untouched.
//
// The input is a JSON request body; a body that is not an object, carries no
// messages array, or needs no edit is returned unchanged (same slice), so the
// hot path stays allocation-free apart from the unmarshal.
func StripReasoningContentFromHistory(body []byte, provider string) []byte {
	if _, ok := stripReasoningContentProviders[provider]; !ok {
		return body
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	msgs, ok := req["messages"].([]any)
	if !ok {
		return body
	}

	changed := false
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		for _, key := range reasoningHistoryFields {
			if _, exists := msg[key]; exists {
				delete(msg, key)
				changed = true
			}
		}
	}
	if !changed {
		return body
	}

	out, err := marshalStable(req)
	if err != nil {
		return body
	}
	return out
}
