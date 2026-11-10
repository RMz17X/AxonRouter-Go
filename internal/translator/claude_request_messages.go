package translator

// Message-conversion internals for the OpenAI → Claude request
// translator (claude_request.go): content-block conversion,
// system extraction, the tool_result split and same-role merge
// passes, and the trailing cache_control marker — the middle of
// Axon's open-sse/translator/request/openai-to-claude.js.

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
)

// claudeDataURIRe mirrors parseDataUri's DATA_URI_RE
// (open-sse/translator/concerns/image.js).
var claudeDataURIRe = regexp.MustCompile(`^data:([^;]+);base64,([\s\S]+)$`)

func parseClaudeDataURI(url string) (mimeType, base64 string, ok bool) {
	m := claudeDataURIRe.FindStringSubmatch(url)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// adjustMaxTokensForClaude mirrors open-sse/translator/formats/
// maxTokens.js with the model's real ceiling: a zero maxTokens
// falls back to DEFAULT_MAX_TOKENS (64000), tool calling raises to
// at least DEFAULT_MIN_TOKENS (32000), a Claude thinking budget
// forces maxTokens strictly above it, and the result never exceeds
// the ceiling (ceiling 0 = unknown → 64000, the JS default param).
func adjustMaxTokensForClaude(maxTokens float64, hasTools bool, thinkingBudget float64, ceiling float64) float64 {
	if ceiling == 0 {
		ceiling = 64000
	}
	if maxTokens == 0 {
		maxTokens = 64000
	}
	if hasTools && maxTokens < 32000 {
		maxTokens = 32000
	}
	if thinkingBudget > 0 && maxTokens <= thinkingBudget {
		maxTokens = thinkingBudget + 1024
	}
	if maxTokens > ceiling {
		maxTokens = ceiling
	}
	return maxTokens
}

// extractClaudeTextContent mirrors extractTextContent
// (open-sse/translator/formats/gemini.js) with the default ""
// separator: string content passes through, array content joins
// the text parts.
func extractClaudeTextContent(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	parts, ok := content.([]any)
	if !ok {
		return ""
	}
	var texts []string
	for _, part := range parts {
		m, ok := part.(map[string]any)
		if !ok || m["type"] != "text" {
			continue
		}
		if t, ok := m["text"].(string); ok {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "")
}

// claudeContentBlocks mirrors getContentBlocksFromMessage
// (openai-to-claude.js): converts one OpenAI message's content
// (plus tool_calls) into Claude content blocks.
func claudeContentBlocks(msg map[string]any) []any {
	role, _ := msg["role"].(string)
	var blocks []any
	content := msg["content"]

	if role == "user" || role == "tool" {
		if s, ok := content.(string); ok && s != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": s})
		}
		if parts, ok := content.([]any); ok {
			for _, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				switch part["type"] {
				case "text":
					blocks = append(blocks, map[string]any{"type": "text", "text": part["text"]})
				case "tool_result":
					// Already in Claude format — passthrough, apart from
					// the tool id: a foreign id (e.g. a Gemini "__ts__"
					// transport id) is refused by strict Claude upstreams,
					// so it gets the shared cleanup (cleanClaudeToolID).
					if id, isStr := part["tool_use_id"].(string); isStr {
						part["tool_use_id"] = cleanClaudeToolID(id)
					}
					blocks = append(blocks, part)
				case "image_url":
					img, _ := part["image_url"].(map[string]any)
					url, _ := img["url"].(string)
					if mimeType, data, ok := parseClaudeDataURI(url); ok {
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type": "base64", "media_type": mimeType, "data": data,
							},
						})
					} else if strings.HasPrefix(url, "http") {
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type": "url", "url": url,
							},
						})
					}
				case "image":
					if src, ok := part["source"].(map[string]any); ok {
						blocks = append(blocks, map[string]any{"type": "image", "source": src})
					}
				case "file":
					fileObj, _ := part["file"].(map[string]any)
					fileData, _ := fileObj["file_data"].(string)
					if mimeType, data, ok := parseClaudeDataURI(fileData); ok && mimeType == "application/pdf" {
						blocks = append(blocks, map[string]any{
							"type": "document",
							"source": map[string]any{
								"type": "base64", "media_type": "application/pdf", "data": data,
							},
						})
					}
				}
			}
		}
	}

	if role == "assistant" {
		if s, ok := content.(string); ok && s != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": s})
		}
		if parts, ok := content.([]any); ok {
			for _, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				switch part["type"] {
				case "text":
					blocks = append(blocks, map[string]any{"type": "text", "text": part["text"]})
				case "tool_use":
					if id, isStr := part["id"].(string); isStr {
						part["id"] = cleanClaudeToolID(id)
					}
					blocks = append(blocks, part)
				case "thinking":
					// Strip cache_control: Claude rejects it on
					// thinking blocks.
					rest := make(map[string]any, len(part))
					for k, v := range part {
						if k != "cache_control" {
							rest[k] = v
						}
					}
					blocks = append(blocks, rest)
				default:
					if _, hasThinking := part["thinking"]; hasThinking {
						rest := make(map[string]any, len(part))
						for k, v := range part {
							if k != "cache_control" {
								rest[k] = v
							}
						}
						blocks = append(blocks, rest)
					}
				}
			}
		}
		if toolCalls, ok := msg["tool_calls"].([]any); ok {
			for _, rawTC := range toolCalls {
				tc, ok := rawTC.(map[string]any)
				if !ok {
					continue
				}
				// JS gate: tc.type === "function" || tc.function.
				fn, hasFn := tc["function"].(map[string]any)
				if tc["type"] != "function" && !hasFn {
					continue
				}
				if !hasFn {
					fn = tc
				}
				// safeParseJSON(arguments, arguments): a string is
				// parsed, falling back to the raw string; a
				// non-string passes through unchanged. One deliberate
				// divergence from that fallback: a result that is not
				// a JSON object (an empty or unparseable string, a
				// scalar) can never be a valid tool_use input — strict
				// Claude upstreams refuse the whole request over it
				// ("tool_use.input: Input should be a valid
				// dictionary") — so it becomes the empty object, the
				// same default the executor-side converter applies.
				var input any = fn["arguments"]
				if argStr, ok := input.(string); ok {
					var parsed any
					if err := json.Unmarshal([]byte(argStr), &parsed); err == nil {
						input = parsed
					}
				}
				if _, isObj := input.(map[string]any); !isObj {
					input = map[string]any{}
				}
				tcID, _ := tc["id"].(string)
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": cleanClaudeToolID(tcID), "name": fn["name"], "input": input,
				})
			}
		}
	}

	// JS appends the envelope tool_result for EVERY role:"tool"
	// message, after whatever the content branch produced.
	if role == "tool" {
		contentStr, isStr := content.(string)
		if !isStr {
			contentStr = extractClaudeTextContent(content)
		}
		toolCallID, _ := msg["tool_call_id"].(string)
		blocks = append(blocks, map[string]any{
			"type": "tool_result", "tool_use_id": cleanClaudeToolID(toolCallID), "content": contentStr,
		})
	}
	return blocks
}

// claudeToolResultBlock mirrors the JS tool-role fallback in
// getContentBlocksFromMessage: a role:"tool" message whose content
// produced no blocks becomes a single tool_result block.
func claudeMessagesFromOpenAI(rawMessages []any) (merged []any, systemParts []string) {
	var result []map[string]any

	for _, rawMsg := range rawMessages {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)

		if role == "system" {
			content := msg["content"]
			if s, ok := content.(string); ok {
				systemParts = append(systemParts, s)
			} else {
				systemParts = append(systemParts, extractClaudeTextContent(content))
			}
			continue
		}

		blocks := claudeContentBlocks(msg)
		if len(blocks) == 0 {
			continue
		}

		// tool_result blocks are split into their own user message,
		// immediately after the message carrying the other blocks —
		// Claude requires tool_result to follow the tool_use message.
		hasToolResult := false
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
				hasToolResult = true
				break
			}
		}
		if hasToolResult {
			var toolResultBlocks, otherBlocks []any
			for _, b := range blocks {
				if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
					toolResultBlocks = append(toolResultBlocks, b)
				} else {
					otherBlocks = append(otherBlocks, b)
				}
			}
			if len(otherBlocks) > 0 {
				msgRole := "assistant"
				if role == "user" || role == "tool" {
					msgRole = "user"
				}
				result = append(result, map[string]any{"role": msgRole, "content": otherBlocks})
			}
			result = append(result, map[string]any{"role": "user", "content": toolResultBlocks})
			continue
		}

		convertedRole := "assistant"
		if role == "user" || role == "tool" {
			convertedRole = "user"
		}
		converted := map[string]any{"role": convertedRole, "content": blocks}

		// Merge with the previous message when roles match and
		// neither side carries tool_use (a tool_use message is
		// flushed immediately).
		hasToolUse := false
		for _, b := range blocks {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_use" {
				hasToolUse = true
				break
			}
		}
		if len(result) > 0 {
			prev := result[len(result)-1]
			prevBlocks, _ := prev["content"].([]any)
			prevHasToolUse := false
			for _, b := range prevBlocks {
				if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_use" {
					prevHasToolUse = true
					break
				}
			}
			if prev["role"] == convertedRole && !prevHasToolUse && !hasToolUse {
				prev["content"] = append(prevBlocks, blocks...)
				continue
			}
		}
		result = append(result, converted)
	}

	// Final pass: merge consecutive same-role messages.
	var out []map[string]any
	for _, m := range result {
		if len(out) > 0 {
			prev := out[len(out)-1]
			if prev["role"] == m["role"] {
				prevBlocks, _ := prev["content"].([]any)
				curBlocks, _ := m["content"].([]any)
				prev["content"] = append(prevBlocks, curBlocks...)
				continue
			}
		}
		out = append(out, m)
	}
	merged = make([]any, len(out))
	for i, m := range out {
		merged[i] = m
	}
	return merged, systemParts
}

// claudeToolsFromOpenAI mirrors the tools mapping of
// openai-to-claude.js: non-function tool types pass through
// untouched; function tools become {name, description,
// input_schema}; the last converted tool gets an ephemeral
// cache_control marker.

// addClaudeCacheControl mirrors the cache_control pass of
// openai-to-claude.js: the last cacheable block of the last
// assistant message gets an ephemeral marker.
func addClaudeCacheControl(messages []any) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		content, _ := msg["content"].([]any)
		for j := len(content) - 1; j >= 0; j-- {
			block, ok := content[j].(map[string]any)
			if !ok {
				continue
			}
			switch block["type"] {
			case "text", "tool_use", "tool_result", "image":
				block["cache_control"] = map[string]any{"type": "ephemeral"}
				return
			}
		}
		return
	}
}

// TranslateOpenAIRequestToClaude converts an OpenAI
// chat-completions request body into a Claude Messages request
// body (Axon's openaiToClaudeRequest). maxOutputCeiling is the
