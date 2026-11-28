package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestStripReasoningContentFromHistory(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		body         string
		wantStripped bool
	}{
		{
			name:     "mistral-vibe strips all reasoning aliases from assistant turns",
			provider: "mistral-vibe",
			body: `{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello","reasoning_content":"rc","reasoning":"r","reasoning_details":[{"text":"rd"}],"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"user","content":"again"}
			]}`,
			wantStripped: true,
		},
		{
			name:     "native deepseek keeps reasoning content it requires",
			provider: "deepseek",
			body:     `{"messages":[{"role":"assistant","content":"ok","reasoning_content":"trace"}]}`,
		},
		{
			name:     "native glm keeps reasoning content it requires",
			provider: "glm",
			body:     `{"messages":[{"role":"assistant","content":"ok","reasoning_content":"trace"}]}`,
		},
		{
			name:     "mistral is not affected",
			provider: "mistral",
			body:     `{"messages":[{"role":"assistant","content":"ok","reasoning_content":"trace"}]}`,
		},
		{
			name:     "AI Assistant is not affected",
			provider: "openrouter",
			body:     `{"messages":[{"role":"assistant","content":"ok","reasoning_content":"trace"}]}`,
		},
		{
			name:     "malformed body passes through",
			provider: "deepseek",
			body:     `not json`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := StripReasoningContentFromHistory([]byte(tt.body), tt.provider)
			if tt.body == "not json" {
				if string(out) != tt.body {
					t.Fatalf("malformed body changed: %s", out)
				}
				return
			}
			var parsed map[string]any
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("output is not valid JSON: %v\n%s", err, out)
			}
			msgs, ok := parsed["messages"].([]any)
			if !ok {
				// malformed input case: passthrough expected
				if tt.wantStripped {
					t.Fatalf("no messages in output: %s", out)
				}
				return
			}
			if len(msgs) > 1 {
				user, ok := msgs[len(msgs)-1].(map[string]any)
				if !ok {
					t.Fatalf("last message is not an object")
				}
				if _, exists := user["reasoning_content"]; exists {
					t.Error("non-assistant message lost its reasoning_content")
				}
			}
			for _, raw := range msgs {
				msg, ok := raw.(map[string]any)
				if !ok || msg["role"] != "assistant" {
					continue
				}
				for _, key := range reasoningHistoryFields {
					_, exists := msg[key]
					if tt.wantStripped && exists {
						t.Errorf("assistant message still has %q: %s", key, out)
					}
					if !tt.wantStripped && key == "reasoning_content" && !exists {
						t.Errorf("assistant message lost %q: %s", key, out)
					}
				}
				if tt.wantStripped {
					if _, ok := msg["tool_calls"]; ok {
						// tool_calls must survive the strip
						continue
					}
				}
			}
		})
	}
}

func TestStripReasoningContentPreservesOtherFields(t *testing.T) {
	body := []byte(`{"model":"zai-glm-5-3","temperature":0.2,"messages":[
		{"role":"assistant","content":"done","reasoning_content":"trace","refusal":null,"tool_calls":[{"id":"x","type":"function","function":{"name":"f","arguments":"{}"}}]}
	]}`)
	out := StripReasoningContentFromHistory(body, "mistral-vibe")
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["model"] != "zai-glm-5-3" {
		t.Errorf("model changed: %v", parsed["model"])
	}
	if parsed["temperature"] == nil {
		t.Error("temperature was dropped")
	}
	msg := parsed["messages"].([]any)[0].(map[string]any)
	if msg["content"] != "done" || msg["refusal"] != nil {
		t.Errorf("non-reasoning fields changed: %#v", msg)
	}
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("reasoning_content survived")
	}
	if _, ok := msg["tool_calls"]; !ok {
		t.Error("tool_calls was dropped")
	}
}
