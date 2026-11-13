//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"

	"axonrouter/proxy/internal/pricing"
)

// claudeSSEStreamWithCache is a Claude Messages event stream whose prompt
// cache was warm: the input side (input_tokens, cache_creation_input_tokens,
// cache_read_input_tokens) arrives in message_start and the output side
// (output_tokens) arrives in message_delta. No single frame carries the whole
// record, so a reader that takes one frame at a time sees none of the cache.
const claudeSSEStreamWithCache = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-20250514\",\"usage\":{\"input_tokens\":100,\"cache_creation_input_tokens\":20,\"cache_read_input_tokens\":500}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"upstream reply\"}}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"

// addClaudeNativeConnection stores a connection for the Anthropic provider
// whose baseUrl points at the fake upstream. The provider id keeps the request
// in the Claude dialect (the client speaks /v1/messages), and the override
// keeps the request local.
func addClaudeNativeConnection(t *testing.T, env *Env, id string, up *Upstream) {
	t.Helper()
	priority := 1
	data := `{"apiKey":"sk-upstream","baseUrl":"` + up.URL + `"}`
	if err := env.Repo.CreateProviderConnectionFull(id, "anthropic", "apikey", "Anthropic Local", &priority, data); err != nil {
		t.Fatalf("create connection %s: %v", id, err)
	}
	stored, err := env.Repo.GetProviderConnectionByID(id)
	if err != nil || stored == nil {
		t.Fatalf("read back connection %s: %v", id, err)
	}
	if !strings.Contains(stored.Data, up.URL) {
		t.Fatalf("connection %s stored data %q, want it to carry the fake upstream URL", id, stored.Data)
	}
}

// TestClaudeStreamedTurnRecordsCacheReadInUsageHistory is the end-to-end
// accounting check for the prompt-cache fix: a streamed Claude-native turn
// with a warm cache must leave a usageHistory row whose cached-token figure is
// non-zero, whose prompt count is cache-INCLUSIVE, and whose cost reflects the
// cache rate rather than the full input rate.
//
// Before the fix the streamed Claude paths read usage one frame at a time, so
// the row carried cached_tokens = 0 and a prompt count that fell back to the
// character estimate — the dashboard and the cache-hit analytics then read
// every Claude turn as a cold one.
func TestClaudeStreamedTurnRecordsCacheReadInUsageHistory(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(claudeSSEStreamWithCache))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})
	addClaudeNativeConnection(t, env, "conn-claude", upstream)

	res := env.Post(t, "/v1/messages", claudeMessagesBody("anthropic/claude-sonnet-4-20250514", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/messages = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("usageHistory rows = %d, want exactly 1 for a single request", len(rows))
	}
	row := rows[0]

	// The tokens JSON is what the cache analytics and the dashboard read.
	var tokens struct {
		PromptTokens             int `json:"prompt_tokens"`
		CompletionTokens         int `json:"completion_tokens"`
		CachedTokens             int `json:"cached_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal([]byte(row.Tokens), &tokens); err != nil {
		t.Fatalf("decode usage tokens %q: %v", row.Tokens, err)
	}

	if tokens.CachedTokens != 500 {
		t.Errorf("cached_tokens = %d, want 500 (the warm cache read the upstream reported)", tokens.CachedTokens)
	}
	if tokens.CacheCreationInputTokens != 20 {
		t.Errorf("cache_creation_input_tokens = %d, want 20", tokens.CacheCreationInputTokens)
	}
	// prompt_tokens is cache-INCLUSIVE: input + cache_read + cache_creation.
	if tokens.PromptTokens != 620 {
		t.Errorf("prompt_tokens = %d, want 620 (100 + 500 + 20)", tokens.PromptTokens)
	}
	if tokens.CompletionTokens != 25 {
		t.Errorf("completion_tokens = %d, want 25", tokens.CompletionTokens)
	}
	if row.PromptTokens != 620 {
		t.Errorf("usageHistory.promptTokens = %d, want 620", row.PromptTokens)
	}
	if row.CompletionTokens != 25 {
		t.Errorf("usageHistory.completionTokens = %d, want 25", row.CompletionTokens)
	}
	if !strings.Contains(row.Tokens, "cached_tokens") {
		t.Errorf("stored tokens %q do not carry the cache read", row.Tokens)
	}

	// The cost is what the dashboard bills, so the cache read has to be
	// charged at the cache rate. The same figures priced as a cold turn
	// (no cache read, no cache write) are strictly more expensive: the
	// whole point of the warm cache.
	warm := pricing.EstimateCost("anthropic", "claude-sonnet-4-20250514", pricing.TokenCounts{
		PromptTokens:        620,
		CompletionTokens:    25,
		CachedTokens:        500,
		CacheCreationTokens: 20,
	})
	cold := pricing.EstimateCost("anthropic", "claude-sonnet-4-20250514", pricing.TokenCounts{
		PromptTokens:     620,
		CompletionTokens: 25,
	})
	if warm <= 0 {
		t.Errorf("warm-cache cost = %v, want a positive priced cost", warm)
	}
	if warm >= cold {
		t.Errorf("warm-cache cost %v is not cheaper than the same turn priced cold (%v) — the cache read was not charged at the cache rate", warm, cold)
	}
	if row.Cost <= 0 {
		t.Errorf("usageHistory.cost = %v, want the priced cost of the turn", row.Cost)
	}
	if diff := row.Cost - warm; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("usageHistory.cost = %v, want %v (the same pricing call the usage logger makes)", row.Cost, warm)
	}
}
