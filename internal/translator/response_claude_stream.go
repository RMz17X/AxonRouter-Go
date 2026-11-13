package translator

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	json "encoding/json/v2"
)

// TranslateOpenAIToClaudeStream converts a single OpenAI SSE chunk JSON payload to Claude SSE format.
// It keys the per-stream translation state by chunk.ID; callers with concurrent
// streams should prefer TranslateOpenAIToClaudeStreamSession to scope state per request.
//
// Without a session key the deferred terminal cannot be flushed at [DONE] (a
// [DONE] payload carries no chunk ID to look the state up by), so a stream
// whose finish deferred the terminal closes only via EnsureStreamClosed —
// which also needs the key. Production callers must use the Session variant.
func TranslateOpenAIToClaudeStream(openaiChunk []byte) ([]byte, error) {
	return TranslateOpenAIToClaudeStreamSession("", openaiChunk)
}

// TranslateOpenAIToClaudeStreamSession is like TranslateOpenAIToClaudeStream but
// scopes the translation state to sessionKey instead of chunk.ID. A stream that
// spans multiple calls MUST pass the same sessionKey every time, and SHOULD
// defer ClearStreamState(sessionKey) after the stream ends so state cannot
// collide with another request or leak.
func TranslateOpenAIToClaudeStreamSession(sessionKey string, openaiChunk []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(openaiChunk)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if string(trimmed) == "[DONE]" {
		if tail := flushDeferredTerminal(sessionKey); len(tail) > 0 {
			return append(tail, []byte("data: [DONE]\n\n")...), nil
		}
		return []byte("data: [DONE]\n\n"), nil
	}

	var isDone bool
	var dataPart []byte
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		dataStr := string(bytes.TrimSpace(trimmed[5:]))
		if dataStr == "[DONE]" {
			isDone = true
		} else {
			dataPart = []byte(dataStr)
		}
	} else {
		dataPart = trimmed
	}

	if isDone {
		if tail := flushDeferredTerminal(sessionKey); len(tail) > 0 {
			return append(tail, []byte("data: [DONE]\n\n")...), nil
		}
		return []byte("data: [DONE]\n\n"), nil
	}

	// Some upstreams (opencode free-tier) split one JSON object across multiple
	// SSE events. Rejoin any buffered tail of a previously-truncated payload
	// before parsing. Only possible with a stable sessionKey.
	if sessionKey != "" {
		statesMu.Lock()
		if prev, ok := pendingJSON[sessionKey]; ok {
			dataPart = append(prev.data, dataPart...)
			delete(pendingJSON, sessionKey)
		}
		statesMu.Unlock()
	}

	var chunk OpenAIChunk
	if err := json.Unmarshal(dataPart, &chunk); err != nil {
		if sessionKey != "" && isTruncatedJSON(err) && len(dataPart) < maxPendingJSON {
			statesMu.Lock()
			pendingJSON[sessionKey] = pendingFragment{data: dataPart, createdAt: time.Now()}
			statesMu.Unlock()
			return nil, nil // hold the fragment until the continuation arrives
		}
		return nil, fmt.Errorf("unmarshal stream chunk: %w", err)
	}

	stateKey := sessionKey
	if stateKey == "" {
		stateKey = chunk.ID
	}
	if stateKey == "" {
		stateKey = "default-session"
	}

	statesMu.Lock()
	pruneStaleStatesLocked()
	state, exists := states[stateKey]
	if !exists {
		// Zero-choice sidecars (OpenCode inference-cost, usage-only after finish)
		// must not bootstrap a fake Claude message.
		if len(chunk.Choices) == 0 {
			statesMu.Unlock()
			return nil, nil
		}
		cleanID := strings.Replace(chunk.ID, "chatcmpl-", "", 1)
		if cleanID == "" || cleanID == "chat" {
			cleanID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
		}
		modelName := chunk.Model
		if modelName == "" {
			modelName = "claude-3-5-sonnet"
		}
		state = &StreamState{
			CreatedAt:      time.Now(),
			MessageId:      cleanID,
			Model:          modelName,
			ToolCalls:      make(map[int]ToolCallState),
			ToolArgBuffers: make(map[int]string),
		}
		states[stateKey] = state
	}
	statesMu.Unlock()

	var results []map[string]any

	if chunk.Usage != nil {
		if state.Usage == nil {
			state.Usage = &OpenAIUsage{}
		}
		if chunk.Usage.PromptTokens > 0 {
			state.Usage.PromptTokens = chunk.Usage.PromptTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			state.Usage.CompletionTokens = chunk.Usage.CompletionTokens
		}
		if cached := chunk.Usage.GetCachedTokens(); cached > 0 {
			state.Usage.CachedTokens = cached
		}
		if chunk.Usage.CacheCreationInputTokens > 0 {
			state.Usage.CacheCreationInputTokens = chunk.Usage.CacheCreationInputTokens
		}
		if chunk.Usage.CompletionTokensDetails != nil {
			state.Usage.CompletionTokensDetails = chunk.Usage.CompletionTokensDetails
		}
		// The chunk's usage arrived straight from the wire, so the subset
		// relation has to be derived here (decolua/9router #5634).
		state.Usage.MarkReasoningSubset()
		// Deferred terminal: the finish chunk already went by without usage,
		// and this trailing chunk is where stream_options.include_usage
		// delivers it. Close the message now with the real numbers.
		if state.FinishReason != "" && !state.TerminalSent {
			state.TerminalSent = true
			results = append(results, claudeTerminalEvents(state)...)
		}
	}

	// 1. Message Start
	if !state.MessageStartSent {
		state.MessageStartSent = true
		state.PromptEstimate = promptEstimateFor(sessionKey)
		results = append(results, map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            state.MessageId,
				"type":          "message",
				"role":          "assistant",
				"model":         state.Model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		})
	}

	if len(chunk.Choices) == 0 {
		if len(results) > 0 {
			var buf bytes.Buffer
			for _, res := range results {
				buf.WriteString(formatSSE(res))
			}
			return buf.Bytes(), nil
		}
		return nil, nil
	}

	choice := chunk.Choices[0]
	delta := choice.Delta

	// 2. Reasoning
	reasoningContent := extractReasoningText(delta)

	content := delta.Content
	if content != "" {
		if state.ThinkFilter == nil {
			state.ThinkFilter = NewThinkStreamFilter()
		}
		thinkDelta, cleanDelta := state.ThinkFilter.Feed(content)
		if thinkDelta != "" {
			if reasoningContent == "" {
				reasoningContent = thinkDelta
			} else {
				reasoningContent += thinkDelta
			}
		}
		content = cleanDelta
	}

	if reasoningContent != "" {
		stopTextBlock(state, &results)
		if !state.ThinkingBlockStarted {
			state.ThinkingBlockIndex = state.NextBlockIndex
			state.NextBlockIndex++
			state.ThinkingBlockStarted = true
			results = append(results, map[string]any{
				"type":  "content_block_start",
				"index": state.ThinkingBlockIndex,
				"content_block": map[string]any{
					"type":     "thinking",
					"thinking": "",
				},
			})
		}
		results = append(results, map[string]any{
			"type":  "content_block_delta",
			"index": state.ThinkingBlockIndex,
			"delta": map[string]any{
				"type":     "thinking_delta",
				"thinking": reasoningContent,
			},
		})
	}

	// 3. Content
	if content != "" {
		stopThinkingBlock(state, &results)
		if !state.TextBlockStarted {
			state.TextBlockIndex = state.NextBlockIndex
			state.NextBlockIndex++
			state.TextBlockStarted = true
			state.TextBlockClosed = false
			results = append(results, map[string]any{
				"type":  "content_block_start",
				"index": state.TextBlockIndex,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
		}
		results = append(results, map[string]any{
			"type":  "content_block_delta",
			"index": state.TextBlockIndex,
			"delta": map[string]any{
				"type": "text_delta",
				"text": content,
			},
		})
	}

	// 4. Tool calls
	for _, tc := range delta.ToolCalls {
		idx := 0
		if tc.Index != nil {
			idx = *tc.Index
		}
		if tc.ID != "" {
			// If we already have this tool idx, don't create a new block — it's a delta for the same tool (e.g. Codex streaming where first chunk has name, second has arguments with same id)
			if _, exists := state.ToolCalls[idx]; exists {
				// Already have this tool, skip block creation, just buffer arguments below
			} else {
				// New tool — don't fallback to ID when Function.Name is empty (decolua/9router#2077)
				if tc.Function == nil || tc.Function.Name == "" {
					continue
				}
				toolName := UncloakToolName(tc.Function.Name, nil)
				if toolName == "" {
					continue
				}
				stopThinkingBlock(state, &results)
				stopTextBlock(state, &results)
				toolBlockIndex := state.NextBlockIndex
				state.NextBlockIndex++
				state.ToolCalls[idx] = ToolCallState{
					ID:         tc.ID,
					Name:       toolName,
					BlockIndex: toolBlockIndex,
				}
				results = append(results, map[string]any{
					"type":  "content_block_start",
					"index": toolBlockIndex,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  toolName,
						"input": map[string]any{},
					},
				})
			}
		}
		if tc.Function != nil && tc.Function.Arguments != "" {
			state.ToolArgBuffers[idx] = state.ToolArgBuffers[idx] + tc.Function.Arguments
		}
	}

	// 5. Finish reason
	if choice.FinishReason != nil {
		stopThinkingBlock(state, &results)
		stopTextBlock(state, &results)
		for idx, toolInfo := range state.ToolCalls {
			buffered := state.ToolArgBuffers[idx]
			sanitized := sanitizeToolArgs(toolInfo.Name, buffered)
			results = append(results, map[string]any{
				"type":  "content_block_delta",
				"index": toolInfo.BlockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": sanitized,
				},
			})
			results = append(results, map[string]any{
				"type":  "content_block_stop",
				"index": toolInfo.BlockIndex,
			})
		}
		state.FinishReason = *choice.FinishReason
		if state.Usage != nil {
			// Usage arrived on the finish chunk itself: close the message now.
			state.TerminalSent = true
			results = append(results, claudeTerminalEvents(state)...)
		}
		// Usage not known yet: the terminal is deferred. With
		// stream_options.include_usage an OpenAI stream carries usage on a
		// trailing zero-choice chunk AFTER the finish chunk, so emitting the
		// message_delta here would report zeros and the client's context
		// meter would never move. The usage chunk closes it (see the usage
		// block above); a stream that ends without one is closed by the
		// [DONE] flush or EnsureStreamClosed.
		// NOTE: the state is intentionally left in the map here so callers can
		// still read accumulated usage via GetStreamUsage(sessionKey) after the
		// finish chunk. Cleanup is the caller's job via ClearStreamState (defer).
	}

	var buf bytes.Buffer
	for _, res := range results {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes(), nil
}

// EnsureStreamClosed generates closing events (message_delta, message_stop) if a stream
// was interrupted or ended without an explicit terminal from upstream. That
// covers both a finish_reason that never arrived and the deferred terminal of
// a finish whose usage chunk never came.
func EnsureStreamClosed(sessionKey string) []byte {
	if sessionKey == "" {
		return nil
	}
	statesMu.Lock()
	state, exists := states[sessionKey]
	statesMu.Unlock()
	if !exists || state == nil {
		return nil
	}
	if state.TerminalSent {
		return nil // Already cleanly closed
	}

	if state.FinishReason == "" {
		state.FinishReason = "stop"
	}
	state.TerminalSent = true
	var results []map[string]any

	if !state.MessageStartSent {
		state.MessageStartSent = true
		state.PromptEstimate = promptEstimateFor(sessionKey)
		results = append(results, map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            state.MessageId,
				"type":          "message",
				"role":          "assistant",
				"model":         state.Model,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
				},
			},
		})
	}

	stopThinkingBlock(state, &results)
	stopTextBlock(state, &results)

	for idx, toolInfo := range state.ToolCalls {
		buffered := state.ToolArgBuffers[idx]
		sanitized := sanitizeToolArgs(toolInfo.Name, buffered)
		results = append(results, map[string]any{
			"type":  "content_block_delta",
			"index": toolInfo.BlockIndex,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": sanitized,
			},
		})
		results = append(results, map[string]any{
			"type":  "content_block_stop",
			"index": toolInfo.BlockIndex,
		})
	}

	results = append(results, claudeTerminalEvents(state)...)

	var buf bytes.Buffer
	for _, res := range results {
		buf.WriteString(formatSSE(res))
	}
	return buf.Bytes()
}
