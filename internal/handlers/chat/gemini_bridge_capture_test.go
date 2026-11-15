package chat

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"axonrouter/proxy/internal/translator"
)

// The Gemini lane serves a /v1/responses client through the Responses bridge:
// a Gemini event becomes an OpenAI chunk here and that chunk becomes a
// Responses event in the bridge. Before the fix the chunk was written into
// the capture buffer by the Gemini lane AND by the bridge, so the buffer
// held every chunk twice — the logged response body was doubled and the
// memory it occupied was doubled for nothing.
//
// The count is asserted through the buffer length: one chunk in, one chunk
// captured.

// geminiChunkFixture is one Gemini streaming event carrying a text delta.
const geminiChunkFixture = `data: {"candidates":[{"content":{"parts":[{"text":"hello"}],"role":"model"},"index":0}]}` + "\n\n"

// TestGeminiStreamWithResponsesBridgeCapturesChunkOnce proves the Gemini
// lane leaves the capture to the bridge when one is active: the chunk is
// stored once, not twice.
func TestGeminiStreamWithResponsesBridgeCapturesChunkOnce(t *testing.T) {
	h := &ChatHandler{}

	rec := httptest.NewRecorder()
	ctx := translator.WithResponsesBridge(context.Background())

	metrics := &streamMetrics{}
	upstream := strings.NewReader(geminiChunkFixture)

	if err := h.handleGeminiStream(ctx, rec, upstream, false, metrics); err != nil {
		t.Fatalf("handleGeminiStream: %v", err)
	}

	body := metrics.ResponseBuf.String()
	if body == "" {
		t.Fatal("capture buffer is empty: the chunk was never captured")
	}

	// With a bridge active the capture holds the Responses event stream only.
	// The bug was that the Gemini lane wrote the raw OpenAI chunk into the
	// buffer as well, so the buffer mixed the OpenAI wire form with the
	// Responses wire form — every chunk present twice in two shapes.
	if strings.Contains(body, `"choices"`) {
		t.Errorf("capture buffer holds raw OpenAI chunks alongside the Responses events: the Gemini lane captured the chunk twice; buffer=%q", body)
	}
	if !strings.Contains(body, "response.output_text.delta") {
		t.Errorf("capture buffer missing the Responses events: buffer=%q", body)
	}
}

// TestGeminiStreamWithoutBridgeStillCapturesChunk proves the non-Responses
// path is unaffected: with no bridge the Gemini lane captures the chunk
// itself, exactly as before.
func TestGeminiStreamWithoutBridgeStillCapturesChunk(t *testing.T) {
	h := &ChatHandler{}

	rec := httptest.NewRecorder()
	ctx := context.Background()

	metrics := &streamMetrics{}
	upstream := strings.NewReader(geminiChunkFixture)

	if err := h.handleGeminiStream(ctx, rec, upstream, false, metrics); err != nil {
		t.Fatalf("handleGeminiStream: %v", err)
	}

	body := metrics.ResponseBuf.String()
	if !strings.Contains(body, "hello") {
		t.Fatalf("capture buffer missing the chunk: %q", body)
	}
	if n := strings.Count(body, "hello"); n != 1 {
		t.Errorf("chunk captured %d times, want 1; buffer=%q", n, body)
	}
}
