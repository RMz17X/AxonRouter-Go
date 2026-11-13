package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axonrouter/proxy/internal/translator"
)

// claudeStreamWithCache is a Claude Messages SSE turn whose prompt cache was
// warm: Anthropic reports a large cache read alongside a small uncached input
// and a cache write. The figures only exist in two different frames, so a
// reader that takes one frame at a time sees none of them.
const claudeStreamWithCache = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":500}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// assertClaudeCacheUsage checks the turn was logged with its real cache
// figures: a warm cache read of 500, a cache write of 20, and a
// cache-inclusive prompt count of 620.
func assertClaudeCacheUsage(t *testing.T, ctx context.Context) {
	t.Helper()
	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("expected usage in context, got nil")
	}
	if usage.CachedTokens != 500 {
		t.Errorf("CachedTokens = %d, want 500", usage.CachedTokens)
	}
	if usage.GetCachedTokens() != 500 {
		t.Errorf("GetCachedTokens() = %d, want 500", usage.GetCachedTokens())
	}
	if usage.CacheCreationInputTokens != 20 {
		t.Errorf("CacheCreationInputTokens = %d, want 20", usage.CacheCreationInputTokens)
	}
	if usage.PromptTokens != 620 {
		t.Errorf("PromptTokens = %d, want 620 (100 + 500 + 20)", usage.PromptTokens)
	}
	if usage.CompletionTokens != 25 {
		t.Errorf("CompletionTokens = %d, want 25", usage.CompletionTokens)
	}
}

// TestHandleClaudeMessagesStream_CapturesCacheUsage covers the Claude-client
// passthrough: the turn is relayed in the client's own wire format, so the
// frames are never translated and their usage has to be read as a stream.
//
// The passthrough branch is taken on TranslateResp (the upstream client wants
// Claude frames back); the old code there wrapped each payload in
// {"message": …} and asked ParseClaudeUsage for a top-level `usage`, which no
// Claude stream frame carries — so the branch never captured anything.
func TestHandleClaudeMessagesStream_CapturesCacheUsage(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	req := &Request{Ctx: ctx, TranslateResp: true}
	rec := httptest.NewRecorder()
	if err := handleClaudeMessagesStream(rec, req, strings.NewReader(claudeStreamWithCache)); err != nil {
		t.Fatalf("handleClaudeMessagesStream: %v", err)
	}
	assertClaudeCacheUsage(t, ctx)
}

// TestSSEStream_ClaudePassthroughWithToolMapCapturesCacheUsage covers the
// other passthrough branch: translate=false with a decloaker tool map, i.e. a
// Claude-native combo member whose tool names were fitted. That branch used to
// skip usage capture entirely, so every such turn logged cached_tokens=0.
func TestSSEStream_ClaudePassthroughWithToolMapCapturesCacheUsage(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	var ttft int64
	err := sseStream(sseStreamOpts{
		W:           rec,
		Upstream:    strings.NewReader(claudeStreamWithCache),
		Translate:   false,
		StartTime:   time.Now(),
		TTFT:        &ttft,
		Ctx:         ctx,
		ToolNameMap: map[string]string{"Read": "Read"},
	})
	if err != nil {
		t.Fatalf("sseStream: %v", err)
	}
	assertClaudeCacheUsage(t, ctx)
}

// TestCaptureUsageFromSSEChunk_AccumulatesAcrossChunks covers the third
// streamed path: the SSECopy passthrough, whose chunks arrive one at a time.
// The counters are split across two chunks, so the accumulator has to survive
// between them.
func TestCaptureUsageFromSSEChunk_AccumulatesAcrossChunks(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())

	start := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":500}}}\n\n"
	delta := "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n"

	var capture *usageCapture
	capture = captureUsageFromSSEChunk(ctx, capture, []byte(start))
	capture = captureUsageFromSSEChunk(ctx, capture, []byte(delta))

	assertClaudeCacheUsage(t, ctx)
}
