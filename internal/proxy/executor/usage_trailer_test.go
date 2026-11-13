package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axonrouter/proxy/internal/translator"
)

// The trailer shape an OpenAI-compatible supplier sends when it delivers the
// real usage on a later chunk than the finish chunk: the finish_reason is on
// one frame, the counters on the next one, whose delta is empty.
const trailerContent = `data: {"id":"chatcmpl-trailer","object":"chat.completion.chunk","created":1794467083,"model":"m","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null,"logprobs":null}]}`

const trailerFinish = `data: {"id":"chatcmpl-trailer","object":"chat.completion.chunk","created":1794467083,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"length","logprobs":null}],"usage":null}`

// The standard OpenAI include_usage trailer: choices EMPTY.
const trailerZeroChoice = `data: {"id":"chatcmpl-trailer","object":"chat.completion.chunk","created":1794467083,"model":"m","choices":[],"usage":{"prompt_tokens":1437,"prompt_tokens_details":{"cached_tokens":1344,"cache_write_tokens":0},"completion_tokens":5,"total_tokens":1442}}`

// The SinarCloud shape: choices present, delta empty.
const trailerEmptyDelta = `data: {"id":"chatcmpl-trailer","object":"chat.completion.chunk","created":1794467083,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":null,"logprobs":null}],"usage":{"prompt_tokens":1437,"prompt_tokens_details":{"cached_tokens":1344,"cache_write_tokens":0},"completion_tokens":5,"total_tokens":1442}}`

// splitReader hands out the stream in fixed-size blocks, so the frame that
// carries the usage block straddles a read boundary — the case the frame
// assembler exists for. The type is shared with stream_test.go.

func newSplitStream(data string, chunkSize int) io.Reader {
	return &splitReader{data: []byte(data), chunkSize: chunkSize}
}

// TestUsageTrailerSurvivesSplitFrame proves that the usage-only trailer is
// recorded even when the frame that carries it is split across two reads.
//
// Before the fix the capture parsed each read block on its own, so a trailer
// that straddled a boundary was never seen: the turn was billed as if the
// upstream reported nothing, and the cached input was charged at the full
// rate instead of the cached rate (upstream decolua/9router #5616).
func TestUsageTrailerSurvivesSplitFrame(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trailer string
	}{
		{"zero-choice", trailerZeroChoice},
		{"empty-delta", trailerEmptyDelta},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, chunkSize := range []int{0, 1, 7, 100, 4096} {
				ctx := translator.WithUsageCapture(context.Background())
				rec := httptest.NewRecorder()
				body := strings.Join([]string{trailerContent, trailerFinish, tc.trailer}, "\n\n") + "\n\n"

				var upstream io.Reader = strings.NewReader(body)
				if chunkSize > 0 {
					upstream = newSplitStream(body, chunkSize)
				}

				var ttft int64
				err := sseStream(sseStreamOpts{
					W: rec, Upstream: upstream, Translate: false,
					StartTime: time.Now(), TTFT: &ttft, Ctx: ctx,
				})
				if err != nil {
					t.Fatalf("chunkSize=%d: sseStream err: %v", chunkSize, err)
				}

				usage := translator.GetAndClearUsage(ctx)
				if usage == nil {
					t.Fatalf("chunkSize=%d: usage=nil — the trailer was lost", chunkSize)
				}
				if usage.PromptTokens != 1437 {
					t.Errorf("chunkSize=%d: PromptTokens = %d, want 1437", chunkSize, usage.PromptTokens)
				}
				if usage.CompletionTokens != 5 {
					t.Errorf("chunkSize=%d: CompletionTokens = %d, want 5", chunkSize, usage.CompletionTokens)
				}
				if usage.GetCachedTokens() != 1344 {
					t.Errorf("chunkSize=%d: CachedTokens = %d, want 1344 — the cache discount was lost", chunkSize, usage.GetCachedTokens())
				}
			}
		})
	}
}

// TestUsageTrailerReachesClient proves the trailer itself is relayed to the
// client: it is the standard OpenAI include_usage shape, so dropping it
// would leave the client's own context meter empty.
func TestUsageTrailerReachesClient(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	body := strings.Join([]string{trailerContent, trailerFinish, trailerZeroChoice}, "\n\n") + "\n\n"

	var ttft int64
	if err := sseStream(sseStreamOpts{
		W: rec, Upstream: strings.NewReader(body), Translate: false,
		StartTime: time.Now(), TTFT: &ttft, Ctx: ctx,
	}); err != nil {
		t.Fatalf("sseStream err: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `"cached_tokens":1344`) {
		t.Errorf("client did not receive the usage trailer:\n%s", rec.Body.String())
	}
}

// TestUsageEstimateDoesNotOverwriteRealTrailer proves that an estimate
// standing in for a missing prompt count never erases the cached-token
// count a later frame actually reported.
func TestUsageEstimateDoesNotOverwriteRealTrailer(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()

	// A first frame reports prompt_tokens: 0 — the upstream estimate
	// stands in for it.
	first := `data: {"id":"c","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":3}}`
	// A later frame carries the real counters, including the cache hit.
	second := `data: {"id":"c","choices":[],"usage":{"prompt_tokens":1437,"prompt_tokens_details":{"cached_tokens":1344},"completion_tokens":5,"total_tokens":1442}}`
	body := strings.Join([]string{first, second}, "\n\n") + "\n\n"

	var ttft int64
	if err := sseStream(sseStreamOpts{
		W: rec, Upstream: strings.NewReader(body), Translate: false,
		StartTime: time.Now(), TTFT: &ttft, Ctx: ctx,
	}); err != nil {
		t.Fatalf("sseStream err: %v", err)
	}

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("usage=nil")
	}
	if usage.GetCachedTokens() != 1344 {
		t.Errorf("CachedTokens = %d, want 1344", usage.GetCachedTokens())
	}
	if usage.PromptTokens != 1437 {
		t.Errorf("PromptTokens = %d, want 1437", usage.PromptTokens)
	}
}
