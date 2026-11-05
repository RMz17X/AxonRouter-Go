package chat

import (
	"reflect"
	"testing"

	"axonrouter/proxy/internal/db"
)

func TestInjectSystemPrompt_OpenAI(t *testing.T) {
	t.Run("prepend when no system message exists", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "hello"},
			},
		}
		injectSystemPrompt(body, "INJECTED_SYSTEM", "prepend")
		msgs := body["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("len = %d, want 2", len(msgs))
		}
		m0 := msgs[0].(map[string]any)
		if m0["role"] != "system" || m0["content"] != "INJECTED_SYSTEM" {
			t.Errorf("m0 = %+v", m0)
		}
	})

	t.Run("prepend when system message exists", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "ORIGINAL_SYSTEM"},
				map[string]any{"role": "user", "content": "hello"},
			},
		}
		injectSystemPrompt(body, "INJECTED_SYSTEM", "prepend")
		msgs := body["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("len = %d, want 3", len(msgs))
		}
		m0 := msgs[0].(map[string]any)
		if m0["role"] != "system" || m0["content"] != "INJECTED_SYSTEM" {
			t.Errorf("m0 = %+v", m0)
		}
		m1 := msgs[1].(map[string]any)
		if m1["role"] != "system" || m1["content"] != "ORIGINAL_SYSTEM" {
			t.Errorf("m1 = %+v", m1)
		}
	})

	t.Run("append when system message exists", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "ORIGINAL_SYSTEM"},
				map[string]any{"role": "user", "content": "hello"},
			},
		}
		injectSystemPrompt(body, "INJECTED_SYSTEM", "append")
		msgs := body["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("len = %d, want 3", len(msgs))
		}
		m0 := msgs[0].(map[string]any)
		if m0["role"] != "system" || m0["content"] != "ORIGINAL_SYSTEM" {
			t.Errorf("m0 = %+v", m0)
		}
		m1 := msgs[1].(map[string]any)
		if m1["role"] != "system" || m1["content"] != "INJECTED_SYSTEM" {
			t.Errorf("m1 = %+v", m1)
		}
	})

	t.Run("idempotent on duplicate injection", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "INJECTED_SYSTEM\n\nORIGINAL_SYSTEM"},
			},
		}
		injectSystemPrompt(body, "INJECTED_SYSTEM", "prepend")
		msgs := body["messages"].([]any)
		m0 := msgs[0].(map[string]any)
		if m0["content"] != "INJECTED_SYSTEM\n\nORIGINAL_SYSTEM" {
			t.Errorf("re-injected! got %q", m0["content"])
		}
	})
}

func TestInjectSystemPrompt_Claude(t *testing.T) {
	t.Run("string system prepend", func(t *testing.T) {
		body := map[string]any{
			"system": "ORIGINAL",
		}
		injectSystemPrompt(body, "PREFIX", "prepend")
		if body["system"] != "PREFIX\n\nORIGINAL" {
			t.Errorf("got %q", body["system"])
		}
	})

	t.Run("string system append", func(t *testing.T) {
		body := map[string]any{
			"system": "ORIGINAL",
		}
		injectSystemPrompt(body, "SUFFIX", "append")
		if body["system"] != "ORIGINAL\n\nSUFFIX" {
			t.Errorf("got %q", body["system"])
		}
	})

	t.Run("block array prepend", func(t *testing.T) {
		body := map[string]any{
			"system": []any{
				map[string]any{"type": "text", "text": "BLOCK1"},
			},
		}
		injectSystemPrompt(body, "BLOCK0", "prepend")
		sys := body["system"].([]any)
		if len(sys) != 2 {
			t.Fatalf("len = %d, want 2", len(sys))
		}
		if sys[0].(map[string]any)["text"] != "BLOCK0" {
			t.Errorf("sys[0] = %+v", sys[0])
		}
	})

	t.Run("block array append", func(t *testing.T) {
		body := map[string]any{
			"system": []any{
				map[string]any{"type": "text", "text": "BLOCK1"},
			},
		}
		injectSystemPrompt(body, "BLOCK2", "append")
		sys := body["system"].([]any)
		if len(sys) != 2 {
			t.Fatalf("len = %d, want 2", len(sys))
		}
		if sys[1].(map[string]any)["text"] != "BLOCK2" {
			t.Errorf("sys[1] = %+v", sys[1])
		}
	})
}

func TestInjectSystemPrompt_Gemini(t *testing.T) {
	t.Run("parts prepend", func(t *testing.T) {
		body := map[string]any{
			"system_instruction": map[string]any{
				"parts": []any{
					map[string]any{"text": "ORIGINAL"},
				},
			},
		}
		injectSystemPrompt(body, "PREFIX", "prepend")
		si := body["system_instruction"].(map[string]any)
		parts := si["parts"].([]any)
		if len(parts) != 2 {
			t.Fatalf("len = %d, want 2", len(parts))
		}
		if parts[0].(map[string]any)["text"] != "PREFIX" {
			t.Errorf("parts[0] = %+v", parts[0])
		}
	})

	t.Run("parts append", func(t *testing.T) {
		body := map[string]any{
			"system_instruction": map[string]any{
				"parts": []any{
					map[string]any{"text": "ORIGINAL"},
				},
			},
		}
		injectSystemPrompt(body, "SUFFIX", "append")
		si := body["system_instruction"].(map[string]any)
		parts := si["parts"].([]any)
		if len(parts) != 2 {
			t.Fatalf("len = %d, want 2", len(parts))
		}
		if parts[1].(map[string]any)["text"] != "SUFFIX" {
			t.Errorf("parts[1] = %+v", parts[1])
		}
	})
}

func TestInjectChatTurns(t *testing.T) {
	t.Run("injects fake history and merges consecutive user role", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "SYS"},
				map[string]any{"role": "user", "content": "Real user question"},
			},
		}
		turns := []db.ChatTurn{
			{Role: "user", Content: "Fake user turn"},
			{Role: "assistant", Content: "Fake assistant agreement"},
			{Role: "user", Content: "Fake user followup"},
		}
		injectChatTurns(body, turns)
		msgs := body["messages"].([]any)

		// msgs[0] = system: SYS
		// msgs[1] = user: Fake user turn
		// msgs[2] = assistant: Fake assistant agreement
		// msgs[3] = user: Fake user followup\n\nReal user question (merged!)
		if len(msgs) != 4 {
			t.Fatalf("len = %d, want 4", len(msgs))
		}

		m1 := msgs[1].(map[string]any)
		if m1["role"] != "user" || m1["content"] != "Fake user turn" {
			t.Errorf("m1 mismatch: %+v", m1)
		}

		m2 := msgs[2].(map[string]any)
		if m2["role"] != "assistant" || m2["content"] != "Fake assistant agreement" {
			t.Errorf("m2 mismatch: %+v", m2)
		}

		m3 := msgs[3].(map[string]any)
		wantM3 := "Fake user followup\n\nReal user question"
		if m3["role"] != "user" || m3["content"] != wantM3 {
			t.Errorf("m3 mismatch: %+v, want content %q", m3, wantM3)
		}
	})

	t.Run("injects without merge when roles alternate cleanly", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": "SYS"},
				map[string]any{"role": "user", "content": "Real user question"},
			},
		}
		turns := []db.ChatTurn{
			{Role: "user", Content: "Fake user question"},
			{Role: "assistant", Content: "Fake assistant answer"},
		}
		injectChatTurns(body, turns)
		msgs := body["messages"].([]any)

		// msgs[0] = system: SYS
		// msgs[1] = user: Fake user question
		// msgs[2] = assistant: Fake assistant answer
		// msgs[3] = user: Real user question
		if len(msgs) != 4 {
			t.Fatalf("len = %d, want 4", len(msgs))
		}

		m2 := msgs[2].(map[string]any)
		if m2["role"] != "assistant" || m2["content"] != "Fake assistant answer" {
			t.Errorf("m2 mismatch: %+v", m2)
		}

		m3 := msgs[3].(map[string]any)
		if m3["role"] != "user" || m3["content"] != "Real user question" {
			t.Errorf("m3 mismatch: %+v", m3)
		}
	})

	t.Run("idempotent when same turns already injected", func(t *testing.T) {
		body := map[string]any{
			"messages": []any{
				map[string]any{"role": "user", "content": "Turn 1"},
				map[string]any{"role": "assistant", "content": "Turn 2"},
				map[string]any{"role": "user", "content": "Real question"},
			},
		}
		turns := []db.ChatTurn{
			{Role: "user", Content: "Turn 1"},
			{Role: "assistant", Content: "Turn 2"},
		}
		injectChatTurns(body, turns)
		msgs := body["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("re-injected! len = %d, want 3", len(msgs))
		}
	})
}

// Ensure unused import warning check
var _ = reflect.TypeOf
