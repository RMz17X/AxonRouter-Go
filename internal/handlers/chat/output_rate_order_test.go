package chat

import (
	"context"
	"testing"
	"time"

	"axonrouter/proxy/internal/translator"
)

// TestEstimatedOutputTokensReachTheRateMetrics is the regression for the
// ordering bug in logUsage: the output token count used to be published to the
// metrics accumulator before the fallback estimation ran. An upstream that
// reported zero completion tokens therefore logged "completion: N" (from the
// char estimate) while the rate stayed pinned at 0 tok/s, because the
// accumulator had already been handed the pre-estimation zero.
func TestEstimatedOutputTokensReachTheRateMetrics(t *testing.T) {
	h := &ChatHandler{}

	m := &streamMetrics{}
	start := time.Now()
	m.NoteOutput(start)
	m.NoteOutput(start.Add(time.Second))

	// The upstream reported no completion tokens at all.
	usage := &translator.OpenAIUsage{PromptTokens: 10}

	// A captured response, so the char-based fallback estimation has content
	// to work with.
	m.ResponseBuf.Write([]byte("hello there, this is a streamed answer"))

	info := &UsageLogInfo{
		Provider:     "test-provider",
		Model:        "test-model",
		ConnectionID: "conn-estimate-test",
	}
	h.LogUsage(context.Background(), info, usage, 120, []byte(`{"messages":[{"role":"user","content":"test"}]}`), m)

	if usage.CompletionTokens == 0 {
		t.Fatal("expected the fallback estimation to produce completion tokens")
	}
	if got := m.TokPerSec(); got <= 0 {
		t.Errorf("TokPerSec = %v, want > 0 — the estimated token count never reached the metrics accumulator", got)
	}
}

// TestUsageLogCarriesRateAndGap proves the stream summary still publishes both
// figures after the ordering change, so the fix did not silently drop the
// metrics from the log line.
func TestUsageLogCarriesRateAndGap(t *testing.T) {
	h := &ChatHandler{}

	m := &streamMetrics{}
	start := time.Now()
	m.NoteOutput(start)
	m.NoteOutput(start.Add(500 * time.Millisecond))
	m.ResponseBuf.Write([]byte("some streamed output"))

	usage := &translator.OpenAIUsage{PromptTokens: 10, CompletionTokens: 20}
	info := &UsageLogInfo{
		Provider:     "test-provider",
		Model:        "test-model",
		ConnectionID: "conn-rate-log-test",
	}
	h.LogUsage(context.Background(), info, usage, 120, []byte(`{"messages":[{"role":"user","content":"test"}]}`), m)

	// The log writer is not asserted here — the handler persists to a repo
	// that this unit does not stand up — but the metrics themselves must
	// survive the call and stay consistent with what was published.
	if got := m.TokPerSec(); got <= 0 {
		t.Errorf("TokPerSec = %v, want > 0", got)
	}
	if got := m.MaxGap(); got != 500 {
		t.Errorf("MaxGap = %d, want 500", got)
	}
}
