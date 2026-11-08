package translator

import (
	"bytes"
	json "encoding/json/v2"
	"strings"
)

// ExtractAndStripThink parses out <think>...</think> blocks from content.
// It returns the extracted thinking text and the clean content with <think> blocks removed.
// If </think> is missing, all content after <think> is treated as thinking.
func ExtractAndStripThink(content string) (thinking string, cleanContent string) {
	if !strings.Contains(content, "<think>") {
		return "", content
	}

	var thinkParts []string
	var contentBuf strings.Builder
	remaining := content

	for {
		start := strings.Index(remaining, "<think>")
		if start == -1 {
			contentBuf.WriteString(remaining)
			break
		}
		contentBuf.WriteString(remaining[:start])
		afterStart := remaining[start+len("<think>"):]
		end := strings.Index(afterStart, "</think>")
		if end == -1 {
			thinkParts = append(thinkParts, afterStart)
			break
		}
		thinkParts = append(thinkParts, afterStart[:end])
		remaining = strings.TrimLeft(afterStart[end+len("</think>"):], "\r\n")
	}

	clean := contentBuf.String()
	clean = strings.TrimLeft(clean, "\r\n")

	return strings.Join(thinkParts, "\n"), clean
}

// ThinkStreamFilter maintains state across SSE chunks to split inline <think> tags
// out of delta.content into delta.reasoning_content.
type ThinkStreamFilter struct {
	inThinking bool
	tagBuf     string
}

// NewThinkStreamFilter initializes a new streaming think filter.
func NewThinkStreamFilter() *ThinkStreamFilter {
	return &ThinkStreamFilter{}
}

// InThinking reports whether the stream is currently within a <think> block.
func (f *ThinkStreamFilter) InThinking() bool {
	return f.inThinking
}

// HasBuffered reports whether a partial tag prefix is currently buffered.
func (f *ThinkStreamFilter) HasBuffered() bool {
	return len(f.tagBuf) > 0
}

// Longest-first prefix lists to greedily match trailing partial tags
var (
	openTagPrefixes  = []string{"<thin", "<thi", "<th", "<t", "<"}
	closeTagPrefixes = []string{"</thin", "</thi", "</th", "</t", "</", "<"}
)

// Feed processes an incoming content string delta, returning any thinking text
// and clean content text for the current chunk.
func (f *ThinkStreamFilter) Feed(content string) (thinkingDelta string, contentDelta string) {
	if content == "" && f.tagBuf == "" {
		return "", ""
	}

	if f.tagBuf != "" {
		content = f.tagBuf + content
		f.tagBuf = ""
	}

	var thinkBuf strings.Builder
	var contentBuf strings.Builder

	for content != "" {
		if !f.inThinking {
			start := strings.Index(content, "<think>")
			if start == -1 {
				// Check if trailing edge is a partial prefix of <think>
				for _, p := range openTagPrefixes {
					if strings.HasSuffix(content, p) {
						f.tagBuf = p
						content = content[:len(content)-len(p)]
						break
					}
				}
				contentBuf.WriteString(content)
				content = ""
				break
			}
			contentBuf.WriteString(content[:start])
			f.inThinking = true
			content = content[start+len("<think>"):]
		} else {
			end := strings.Index(content, "</think>")
			if end == -1 {
				// Check if trailing edge is a partial prefix of </think>
				for _, p := range closeTagPrefixes {
					if strings.HasSuffix(content, p) {
						f.tagBuf = p
						content = content[:len(content)-len(p)]
						break
					}
				}
				thinkBuf.WriteString(content)
				content = ""
				break
			}
			thinkBuf.WriteString(content[:end])
			f.inThinking = false
			content = strings.TrimLeft(content[end+len("</think>"):], "\r\n")
		}
	}

	return thinkBuf.String(), contentBuf.String()
}

// Flush emits any remaining buffered tag characters at stream termination.
func (f *ThinkStreamFilter) Flush() (thinkingDelta string, contentDelta string) {
	if f.tagBuf == "" {
		return "", ""
	}
	leftover := f.tagBuf
	f.tagBuf = ""
	if f.inThinking {
		return leftover, ""
	}
	return "", leftover
}

// FilterSSEPayload patches one OpenAI SSE data payload (JSON bytes).
// It normalizes reasoning aliases (e.g. Dahl's "reasoning" -> "reasoning_content")
// and filters <think> tags from delta.content into delta.reasoning_content.
func (f *ThinkStreamFilter) FilterSSEPayload(payload []byte) ([]byte, bool) {
	if !bytes.Contains(payload, []byte(`"choices"`)) {
		return payload, false
	}

	// Quick check: if not in thinking and no tag buffered and payload has no triggers, skip unmarshal
	if !f.inThinking && f.tagBuf == "" && !bytes.Contains(payload, []byte("<think")) && !bytes.Contains(payload, []byte(`"reasoning"`)) {
		return payload, false
	}

	var chunk map[string]any
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return payload, false
	}

	choices, ok := chunk["choices"].([]any)
	if !ok || len(choices) == 0 {
		return payload, false
	}

	changed := false
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}

		// 1. Lift vendor "reasoning" alias to "reasoning_content" if absent (Dahl DeepSeek)
		if r, ok := delta["reasoning"].(string); ok && r != "" {
			if rc, ok := delta["reasoning_content"].(string); !ok || rc == "" {
				delta["reasoning_content"] = r
				changed = true
			}
		}

		// 2. Filter inline <think> tags from delta["content"]
		cVal, hasContent := delta["content"]
		contentStr, isString := cVal.(string)

		if hasContent && isString && (contentStr != "" || f.inThinking || f.tagBuf != "") {
			thinkDelta, cleanDelta := f.Feed(contentStr)
			if thinkDelta != "" {
				if existing, ok := delta["reasoning_content"].(string); ok && existing != "" {
					delta["reasoning_content"] = existing + thinkDelta
				} else {
					delta["reasoning_content"] = thinkDelta
				}
				changed = true
			}
			if cleanDelta != contentStr {
				delta["content"] = cleanDelta
				changed = true
			}
		}
	}

	if !changed {
		return payload, false
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return payload, false
	}
	return out, true
}

// NormalizeOpenAIResponseJSON patches non-streaming chat.completion JSON response bytes.
// It extracts <think>...</think> from message.content into message.reasoning_content,
// and lifts message.reasoning to message.reasoning_content if absent.
func NormalizeOpenAIResponseJSON(body []byte) ([]byte, bool) {
	if !bytes.Contains(body, []byte("<think")) && !bytes.Contains(body, []byte(`"reasoning"`)) {
		return body, false
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, false
	}

	choices, ok := raw["choices"].([]any)
	if !ok || len(choices) == 0 {
		return body, false
	}

	changed := false
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}

		// Lift reasoning alias if present
		if r, ok := msg["reasoning"].(string); ok && r != "" {
			if rc, ok := msg["reasoning_content"].(string); !ok || rc == "" {
				msg["reasoning_content"] = r
				changed = true
			}
		}

		// Extract and strip <think> from message.content
		if cStr, ok := msg["content"].(string); ok && strings.Contains(cStr, "<think>") {
			thinking, clean := ExtractAndStripThink(cStr)
			msg["content"] = clean
			if rc, ok := msg["reasoning_content"].(string); !ok || rc == "" {
				msg["reasoning_content"] = thinking
			}
			changed = true
		}
	}

	if !changed {
		return body, false
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return body, false
	}
	return out, true
}
