package translator

import (
	"bytes"
)

func stopThinkingBlock(state *StreamState, results *[]map[string]any) {
	if !state.ThinkingBlockStarted {
		return
	}
	*results = append(*results, map[string]any{
		"type":  "content_block_stop",
		"index": state.ThinkingBlockIndex,
	})
	state.ThinkingBlockStarted = false
}

func stopTextBlock(state *StreamState, results *[]map[string]any) {
	if !state.TextBlockStarted || state.TextBlockClosed {
		return
	}
	state.TextBlockClosed = true
	*results = append(*results, map[string]any{
		"type":  "content_block_stop",
		"index": state.TextBlockIndex,
	})
	state.TextBlockStarted = false
}

// claudeStopReason maps an OpenAI finish_reason to its Anthropic stop_reason.
func claudeStopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

// claudeFinalUsage renders the client-facing usage block for the terminal
// message_delta. Anthropic and OpenAI disagree on the prompt count: OpenAI's
// prompt_tokens is cache-INCLUSIVE while Anthropic's input_tokens counts only
// uncached input, with cache reads reported separately. Emitting the inclusive
// count AND cache_read_input_tokens made Claude Code (which sums them) read
// the context meter high, so the inclusive count is split here. When the
// upstream never reported prompt tokens at all (a chat upstream that ignores
// stream_options.include_usage), the seeded body-size estimate stands in so
// the client's meter still moves and auto-compact can fire.
func claudeFinalUsage(state *StreamState) map[string]any {
	input, output, cached, cacheCreation := 0, 0, 0, 0
	if state.Usage != nil {
		output = state.Usage.CompletionTokens
		cached = state.Usage.GetCachedTokens()
		cacheCreation = state.Usage.CacheCreationInputTokens
		input = state.Usage.PromptTokens - cached
		if input < 0 {
			input = 0
		}
	}
	if input == 0 && state.PromptEstimate > 0 {
		input = state.PromptEstimate
	}
	finalUsage := map[string]any{
		"input_tokens":  input,
		"output_tokens": output,
	}
	if cached > 0 {
		finalUsage["cache_read_input_tokens"] = cached
	}
	if cacheCreation > 0 {
		finalUsage["cache_creation_input_tokens"] = cacheCreation
	}
	return finalUsage
}

// claudeTerminalEvents builds the terminal message_delta + message_stop pair.
func claudeTerminalEvents(state *StreamState) []map[string]any {
	return []map[string]any{
		{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   claudeStopReason(state.FinishReason),
				"stop_sequence": nil,
			},
			"usage": claudeFinalUsage(state),
		},
		{"type": "message_stop"},
	}
}

// flushDeferredTerminal emits the terminal pair for a session that saw its
// finish_reason but whose usage chunk never arrived before the stream ended.
// It returns nil when there is nothing deferred.
func flushDeferredTerminal(sessionKey string) []byte {
	if sessionKey == "" {
		return nil
	}
	statesMu.Lock()
	defer statesMu.Unlock()
	state, ok := states[sessionKey]
	if !ok || state == nil || state.TerminalSent || state.FinishReason == "" {
		return nil
	}
	state.TerminalSent = true
	var buf bytes.Buffer
	for _, res := range claudeTerminalEvents(state) {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes()
}

// IsClaudeMessageBody reports whether the body is already an Anthropic
// Messages response (`type:"message"`, `role:"assistant"`). A Chat Completions
// body never carries that pair — its discriminator is `object:"chat.completion"`
// — so the check cannot sweep an OpenAI reply in.
