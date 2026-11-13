package translator

import (
	"strings"
	"testing"
)

// A streamed Claude turn reports the uncached input in BOTH frames:
// message_start carries the full usage block (input + cache read + cache
// creation), and message_delta repeats the uncached input_tokens. The
// translator's prompt total is cache-INCLUSIVE, so adopting the delta's
// uncached count verbatim would drop the cache from the total — the streamed
// turn would be billed as a cold one even though the cache was warm.
//
// This is the same class of bug #1102 fixed on the passthrough lanes: the
// cache figures must survive the second frame.
func TestTranslateClaudeChunkToOpenAI_KeepsCacheInclusivePromptAcrossDelta(t *testing.T) {
	state := &ClaudeToOpenAIStreamState{}

	start := []byte(`{"type":"message_start","message":{"id":"msg_cache","role":"assistant","model":"claude-sonnet-4","content":[],"usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":500,"output_tokens":0}}}`)
	if _, err := TranslateClaudeChunkToOpenAI(start, state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Usage == nil {
		t.Fatal("message_start produced no usage")
	}
	if state.Usage.PromptTokens != 620 {
		t.Fatalf("prompt_tokens after message_start = %d, want 620 (100 + 500 + 20)", state.Usage.PromptTokens)
	}

	delta := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"output_tokens":25}}`)
	out, err := TranslateClaudeChunkToOpenAI(delta, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Usage.PromptTokens != 620 {
		t.Errorf("prompt_tokens after message_delta = %d, want 620 — the delta's uncached input must not drop the cache", state.Usage.PromptTokens)
	}
	if state.Usage.CachedTokens != 500 {
		t.Errorf("cached_tokens = %d, want 500", state.Usage.CachedTokens)
	}
	if state.Usage.CacheCreationInputTokens != 20 {
		t.Errorf("cache_creation_input_tokens = %d, want 20", state.Usage.CacheCreationInputTokens)
	}
	if state.Usage.CompletionTokens != 25 {
		t.Errorf("completion_tokens = %d, want 25", state.Usage.CompletionTokens)
	}
	if !strings.Contains(string(out), `"prompt_tokens":620`) {
		t.Errorf("terminal chunk does not carry the cache-inclusive prompt count: %s", out)
	}
}
