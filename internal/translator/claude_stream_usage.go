package translator

import (
	json "encoding/json/v2"
)

// AccumulateClaudeStreamUsage folds the usage of a Claude SSE stream into one
// running OpenAIUsage.
//
// Claude reports the counters of a streamed turn in two different frames:
// `message_start` carries the input side (input_tokens, cache_read_input_tokens,
// cache_creation_input_tokens) nested under `message.usage`, and `message_delta`
// carries the output side (output_tokens) at the top level. Read one frame at a
// time — as ParseClaudeUsage does — neither figure is ever complete: the
// prompt count loses the cache, and the output count loses the prompt. Every
// streamed Claude turn was therefore logged with cached_tokens = 0 and a
// character-estimate prompt count, which is what made a warm prompt cache look
// like a cold one in the usage history and in the cost the dashboard reports.
//
// The accumulated figures follow the canonical OpenAI convention the rest of
// the repository uses: prompt_tokens is cache-INCLUSIVE, so it is
// input + cache_read + cache_creation (see NormalizeClaudeUsage, which
// produces the same total for a non-streamed Claude body). CachedTokens holds
// the cache-read count on its own, so the pricing table can charge it at the
// cache rate.
//
// It returns a NEW usage on a recognised event (a copy of curr with that
// event's fields applied) and curr unchanged otherwise. The new pointer is
// what lets a caller tell "this frame changed the usage" apart from a no-op
// by identity, so a message_delta never clobbers the prompt and cache figures
// that message_start established.
func AccumulateClaudeStreamUsage(curr *OpenAIUsage, chunk []byte) *OpenAIUsage {
	if len(chunk) == 0 {
		return curr
	}
	var raw struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *struct {
				InputTokens              int `json:"input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(chunk, &raw) != nil {
		return curr
	}
	if raw.Type != "message_start" && raw.Type != "message_delta" {
		return curr
	}
	next := OpenAIUsage{PromptCacheIncluded: true}
	if curr != nil {
		next = *curr
		next.PromptCacheIncluded = true
	}
	switch raw.Type {
	case "message_start":
		if raw.Message != nil && raw.Message.Usage != nil {
			u := raw.Message.Usage
			next.CachedTokens = u.CacheReadInputTokens
			next.CacheCreationInputTokens = u.CacheCreationInputTokens
			next.PromptTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		}
	case "message_delta":
		if raw.Usage != nil {
			next.CompletionTokens = raw.Usage.OutputTokens
		}
	}
	return &next
}
