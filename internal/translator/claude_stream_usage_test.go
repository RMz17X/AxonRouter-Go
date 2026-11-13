package translator

import "testing"

// A streamed Claude turn reports its counters in two frames: message_start
// carries the input side (input_tokens, cache_read_input_tokens,
// cache_creation_input_tokens) under message.usage, and message_delta carries
// the output side at the top level. The accumulator has to fold both into one
// record, because a warm prompt cache read one frame at a time is invisible.
func TestAccumulateClaudeStreamUsage(t *testing.T) {
	startPayload := []byte(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","usage":{"input_tokens":1200,"cache_creation_input_tokens":500,"cache_read_input_tokens":45000}}}`)
	deltaPayload := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":85}}`)
	textPayload := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)

	var u *OpenAIUsage
	u = AccumulateClaudeStreamUsage(u, textPayload)
	if u != nil {
		t.Fatalf("expected nil for non-usage payload, got %+v", u)
	}

	u = AccumulateClaudeStreamUsage(u, startPayload)
	if u == nil {
		t.Fatal("expected non-nil usage after message_start")
	}
	if u.CachedTokens != 45000 {
		t.Errorf("CachedTokens = %d, want 45000", u.CachedTokens)
	}
	if u.CacheCreationInputTokens != 500 {
		t.Errorf("CacheCreationInputTokens = %d, want 500", u.CacheCreationInputTokens)
	}
	if u.PromptTokens != 46700 {
		t.Errorf("PromptTokens = %d, want 46700 (1200 + 45000 + 500)", u.PromptTokens)
	}
	if u.CompletionTokens != 0 {
		t.Errorf("CompletionTokens = %d, want 0", u.CompletionTokens)
	}
	if !u.PromptCacheIncluded {
		t.Error("PromptCacheIncluded = false, want true (prompt_tokens is cache-inclusive)")
	}

	before := u
	u = AccumulateClaudeStreamUsage(u, deltaPayload)
	if u == before {
		t.Error("message_delta returned the same pointer; a caller cannot tell an update from a no-op")
	}
	if u.CompletionTokens != 85 {
		t.Errorf("CompletionTokens = %d, want 85", u.CompletionTokens)
	}
	if u.CachedTokens != 45000 {
		t.Errorf("CachedTokens = %d, want 45000", u.CachedTokens)
	}
	if u.PromptTokens != 46700 {
		t.Errorf("PromptTokens = %d, want 46700 (message_delta must not clobber the prompt figures)", u.PromptTokens)
	}
}

// A frame that is not a Claude usage event must leave the running total
// untouched — and return the SAME pointer, so a caller can skip the write.
func TestAccumulateClaudeStreamUsageLeavesUnrelatedFramesAlone(t *testing.T) {
	startPayload := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":5}}}`)

	u := AccumulateClaudeStreamUsage(nil, startPayload)
	if u == nil {
		t.Fatal("expected non-nil usage after message_start")
	}
	for _, payload := range [][]byte{
		[]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`),
		[]byte(`{"type":"message_stop"}`),
		[]byte(`not json at all`),
		[]byte(``),
	} {
		got := AccumulateClaudeStreamUsage(u, payload)
		if got != u {
			t.Errorf("AccumulateClaudeStreamUsage(%q) returned a new pointer; a no-op must return the same one", payload)
		}
	}
}

// A message_delta with no usage object must not wipe the counters the
// message_start established.
func TestAccumulateClaudeStreamUsageKeepsCountersOnAnEmptyDelta(t *testing.T) {
	u := AccumulateClaudeStreamUsage(nil, []byte(`{"type":"message_start","message":{"usage":{"input_tokens":7,"cache_read_input_tokens":11}}}`))
	if u == nil {
		t.Fatal("expected non-nil usage after message_start")
	}
	next := AccumulateClaudeStreamUsage(u, []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`))
	if next == nil {
		t.Fatal("expected non-nil usage after message_delta")
	}
	if next.PromptTokens != 18 {
		t.Errorf("PromptTokens = %d, want 18 (7 + 11)", next.PromptTokens)
	}
	if next.CachedTokens != 11 {
		t.Errorf("CachedTokens = %d, want 11", next.CachedTokens)
	}
}
