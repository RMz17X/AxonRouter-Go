package executor

import (
	"context"
	"io"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axonrouter/proxy/internal/pricing"
	"axonrouter/proxy/internal/translator"
)

// TestUsageTrailerBilledAtCachedRate is the money test for upstream
// decolua/9router #5616.
//
// It drives the OpenAI passthrough relay with a supplier that sends the real
// usage on a LATER chunk than the finish chunk — the SinarCloud shape, where
// the finish_reason is on one frame and the counters on the next one with an
// empty delta — and then prices the turn with the very same call the usage
// logger makes (pricing.EstimateCost over pricing.TokenCounts).
//
// Before the fix the trailer was dropped, cached_tokens landed at 0, and the
// whole prompt was charged at the full input rate: 1437 tokens × $2.50/1M.
// With the trailer conserved, the 1344 cached tokens are charged at the
// cached rate ($1.25/1M) and only the 93 fresh tokens at the input rate, so
// the turn costs roughly a third of what it used to.
func TestUsageTrailerBilledAtCachedRate(t *testing.T) {
	const (
		promptTokens     = 1437
		cachedTokens     = 1344
		completionTokens = 5
		model            = "gpt-4o"
	)

	// The trailer shape: finish_reason on one frame, the counters on the next.
	body := strings.Join([]string{
		trailerContent,
		trailerFinish,
		trailerEmptyDelta,
	}, "\n\n") + "\n\n"

	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	var ttft int64
	if err := sseStream(sseStreamOpts{
		W: rec, Upstream: strings.NewReader(body), Translate: false,
		StartTime: time.Now(), TTFT: &ttft, Ctx: ctx,
	}); err != nil {
		t.Fatalf("sseStream err: %v", err)
	}

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("usage=nil — the trailer was dropped, so the turn bills as if the upstream reported nothing")
	}
	if usage.GetCachedTokens() != cachedTokens {
		t.Fatalf("CachedTokens = %d, want %d — the cache discount was lost",
			usage.GetCachedTokens(), cachedTokens)
	}

	// The same call the usage logger makes (internal/handlers/chat/usage.go).
	prices, ok := pricing.GetPricingForModel("openai", model)
	if !ok {
		t.Fatalf("no pricing for %s", model)
	}
	cost := pricing.EstimateCost("openai", model, pricing.TokenCounts{
		PromptTokens:              usage.PromptTokens,
		CompletionTokens:          usage.CompletionTokens,
		CachedTokens:              usage.GetCachedTokens(),
		CacheCreationTokens:       usage.CacheCreationInputTokens,
		ReasoningTokens:           usage.ReasoningTokens(),
		ReasoningIncludedInOutput: usage.ReasoningIncludedInOutput,
	})

	// What the turn costs when the trailer IS conserved: the cached slice at
	// the cached rate, the fresh slice at the input rate, the output at the
	// output rate.
	wantCached := float64(cachedTokens)/1_000_000*prices.CachedPer1M +
		float64(promptTokens-cachedTokens)/1_000_000*prices.InputPer1M +
		float64(completionTokens)/1_000_000*prices.OutputPer1M
	if math.Abs(cost-wantCached) > 1e-9 {
		t.Errorf("cost = %v, want %v", cost, wantCached)
	}

	// What the SAME turn cost before the fix: the trailer was dropped, so
	// cached_tokens was 0 and the whole prompt was charged at the input rate.
	fullInputRate := float64(promptTokens)/1_000_000*prices.InputPer1M +
		float64(completionTokens)/1_000_000*prices.OutputPer1M
	if fullInputRate <= cost {
		t.Fatalf("the full-rate cost %v is not above the cached-rate cost %v — the test proves nothing", fullInputRate, cost)
	}
	t.Logf("cached-rate cost = $%.9f, full-input-rate cost = $%.9f (%.1fx)",
		cost, fullInputRate, fullInputRate/cost)
}

// TestUsageTrailerBilledAtCachedRateSplitFrame is the same money test with
// the frame that carries the usage split across two reads — the case that
// loses the trailer entirely when the relay parses each read block on its own.
func TestUsageTrailerBilledAtCachedRateSplitFrame(t *testing.T) {
	body := strings.Join([]string{
		trailerContent,
		trailerFinish,
		trailerEmptyDelta,
	}, "\n\n") + "\n\n"

	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	var ttft int64
	if err := sseStream(sseStreamOpts{
		W: rec, Upstream: newSplitStream(body, 64), Translate: false,
		StartTime: time.Now(), TTFT: &ttft, Ctx: ctx,
	}); err != nil {
		t.Fatalf("sseStream err: %v", err)
	}

	usage := translator.GetAndClearUsage(ctx)
	if usage == nil {
		t.Fatal("usage=nil — the split trailer was dropped")
	}
	if usage.GetCachedTokens() != 1344 {
		t.Fatalf("CachedTokens = %d, want 1344", usage.GetCachedTokens())
	}
	cost := pricing.EstimateCost("openai", "gpt-4o", pricing.TokenCounts{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     usage.GetCachedTokens(),
	})
	prices, _ := pricing.GetPricingForModel("openai", "gpt-4o")
	want := float64(1344)/1_000_000*prices.CachedPer1M +
		float64(1437-1344)/1_000_000*prices.InputPer1M +
		float64(5)/1_000_000*prices.OutputPer1M
	if math.Abs(cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", cost, want)
	}
}

// TestUsageNeverBillableFromAnotherRequest proves the merge reads only the
// usage of ITS OWN request.
//
// PeekUsage falls back to the process-global last-usage when the context
// carries no per-request holder. A relay that merged against that global
// would inherit another request's cached-token count and misprice the turn.
// This test pins the per-request holder as the only source of truth.
func TestUsageNeverBillableFromAnotherRequest(t *testing.T) {
	// Another request records a large cache hit on the global fallback.
	translator.SetLastUsage(&translator.OpenAIUsage{
		PromptTokens: 999_999,
		CachedTokens: 999_999,
	})
	defer translator.SetLastUsage(nil)

	// This request's stream reports a turn with NO cache hit.
	ctx := translator.WithUsageCapture(context.Background())
	rec := httptest.NewRecorder()
	body := strings.Join([]string{
		trailerContent,
		trailerFinish,
		`data: {"id":"c","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2}}`,
	}, "\n\n") + "\n\n"

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
	if usage.GetCachedTokens() != 0 {
		t.Errorf("CachedTokens = %d, want 0 — the turn inherited another request's cache count",
			usage.GetCachedTokens())
	}
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", usage.PromptTokens)
	}
}

// TestUsageEstimateNeverOverwritesRealCounters proves the acceptance rule
// directly: when the upstream reports a real prompt count AND a real cache
// count, an estimate standing in for a missing count never displaces them.
func TestUsageEstimateNeverOverwritesRealCounters(t *testing.T) {
	ctx := translator.WithUsageCapture(context.Background())
	ctx = translator.WithPromptEstimate(ctx, 5000)

	rec := httptest.NewRecorder()
	// The FIRST frame reports prompt_tokens: 0 (the estimate stands in), and
	// the SECOND reports the real counters including the cache hit.
	body := strings.Join([]string{
		`data: {"id":"c","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":3}}`,
		`data: {"id":"c","choices":[],"usage":{"prompt_tokens":1437,"prompt_tokens_details":{"cached_tokens":1344},"completion_tokens":5,"total_tokens":1442}}`,
	}, "\n\n") + "\n\n"

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
		t.Errorf("PromptTokens = %d, want 1437 — an estimate displaced the real count", usage.PromptTokens)
	}
}

var _ = io.Discard
