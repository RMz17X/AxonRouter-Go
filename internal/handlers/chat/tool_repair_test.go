package chat

import "testing"

func TestRepairToolCallIDsInMap(t *testing.T) {
	t.Run("pairs missing tool_call_id with assistant tool call", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{"id": "call_123", "type": "function", "function": map[string]any{"name": "bash"}},
					},
				},
				map[string]any{
					"role":    "tool",
					"content": "output text",
					// tool_call_id is missing!
				},
			},
		}

		repairToolCallIDsInMap(body)

		msgs := body["messages"].([]any)
		toolMsg := msgs[1].(map[string]any)
		if toolMsg["tool_call_id"] != "call_123" {
			t.Errorf("expected tool_call_id 'call_123', got %v", toolMsg["tool_call_id"])
		}
	})

	t.Run("preserves existing tool_call_id when already matching", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{"id": "call_existing", "type": "function", "function": map[string]any{"name": "bash"}},
					},
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": "call_existing",
					"content":      "output text",
				},
			},
		}

		repairToolCallIDsInMap(body)

		msgs := body["messages"].([]any)
		toolMsg := msgs[1].(map[string]any)
		if toolMsg["tool_call_id"] != "call_existing" {
			t.Errorf("expected 'call_existing', got %v", toolMsg["tool_call_id"])
		}
	})

	t.Run("demotes orphan tool message to user instead of minting a dangling id", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{
					"role":    "tool",
					"content": "orphan output",
				},
			},
		}

		repairToolCallIDsInMap(body)

		msgs := body["messages"].([]any)
		toolMsg := msgs[0].(map[string]any)
		if toolMsg["role"] != "user" {
			t.Errorf("expected orphan demoted to user, got role %v", toolMsg["role"])
		}
		if _, ok := toolMsg["tool_call_id"]; ok {
			t.Errorf("expected no tool_call_id on demoted message, got %v", toolMsg["tool_call_id"])
		}
		if toolMsg["content"] != "orphan output" {
			t.Errorf("expected content preserved, got %v", toolMsg["content"])
		}
	})

	t.Run("demotes tool message whose id no declared call carries", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{
					"role":    "assistant",
					"content": "working on it",
				},
				map[string]any{
					"role":         "tool",
					"tool_call_id": "call_tool_2",
					"content":      "edit applied",
				},
			},
		}

		repairToolCallIDsInMap(body)

		msgs := body["messages"].([]any)
		toolMsg := msgs[1].(map[string]any)
		if toolMsg["role"] != "user" {
			t.Errorf("expected dangling tool message demoted to user, got role %v", toolMsg["role"])
		}
	})
}
