package translator

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// extractReasoningText pulls reasoning out of a Chat delta across the three
// shapes vendors use: reasoning_content, reasoning, and reasoning_details.
func extractReasoningText(delta OpenAIDelta) string {
	if delta.ReasoningContent != "" {
		return delta.ReasoningContent
	}
	if delta.Reasoning != "" {
		return delta.Reasoning
	}

	var b strings.Builder
	for _, detail := range delta.ReasoningDetails {
		switch {
		case detail.Text != "":
			b.WriteString(detail.Text)
		case detail.Content != "":
			b.WriteString(detail.Content)
		}
	}
	return b.String()
}

func formatSSE(event map[string]any) string {
	eventType, _ := event["type"].(string)
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Sprintf("event: %s\ndata: {}\n\n", eventType)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(payload))
}

// CachedTokensFromJSON reads cached prompt tokens from every persisted and
// provider-facing compatibility shape. It returns the first explicit numeric
// value in precedence order and never treats JSON null as a value.
func CachedTokensFromJSON(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var usage struct {
		CachedTokens         *float64 `json:"cached_tokens"`
		CacheReadInputTokens *float64 `json:"cache_read_input_tokens"`
		// Ollama's native shape: prompt_eval_cached_count is a cache-read
		// SUBSET of prompt_eval_count, which Ollama reports cache-INCLUSIVE
		// — the same convention OpenAI and Gemini use. It is not subtracted
		// from the prompt total here; recording it is what lets the cache
		// analytics and the pricing tables see a real cache hit rate.
		PromptEvalCachedCount *float64 `json:"prompt_eval_cached_count"`
		PromptTokensDetails   *struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails *struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return 0
	}
	var promptCached *float64
	if usage.PromptTokensDetails != nil {
		promptCached = usage.PromptTokensDetails.CachedTokens
	}
	var inputCached *float64
	if usage.InputTokensDetails != nil {
		inputCached = usage.InputTokensDetails.CachedTokens
	}
	for _, candidate := range []*float64{
		usage.CachedTokens,
		usage.CacheReadInputTokens,
		promptCached,
		inputCached,
		usage.PromptEvalCachedCount,
	} {
		if candidate != nil && *candidate >= 0 {
			return int(*candidate)
		}
	}
	return 0
}

// ResolveTokensCounts extracts prompt and completion token counts following the
// upstream usage stats precedence:
// prompt: tokens.prompt_tokens || tokens.input_tokens || rowPrompt || 0
// completion: tokens.completion_tokens || tokens.output_tokens || rowCompletion || 0
func ResolveTokensCounts(raw []byte, rowPrompt, rowCompletion int) (int64, int64) {
	prompt := int64(rowPrompt)
	completion := int64(rowCompletion)
	if len(raw) == 0 {
		return prompt, completion
	}
	var usage struct {
		PromptTokens     *float64 `json:"prompt_tokens"`
		InputTokens      *float64 `json:"input_tokens"`
		CompletionTokens *float64 `json:"completion_tokens"`
		OutputTokens     *float64 `json:"output_tokens"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return prompt, completion
	}
	if usage.PromptTokens != nil && *usage.PromptTokens > 0 {
		prompt = int64(*usage.PromptTokens)
	} else if usage.InputTokens != nil && *usage.InputTokens > 0 {
		prompt = int64(*usage.InputTokens)
	}
	if usage.CompletionTokens != nil && *usage.CompletionTokens > 0 {
		completion = int64(*usage.CompletionTokens)
	} else if usage.OutputTokens != nil && *usage.OutputTokens > 0 {
		completion = int64(*usage.OutputTokens)
	}
	return prompt, completion
}

// NormalizeClaudeUsage converts a fresh Claude usage record to the canonical
// OpenAI shape. The source marker makes repeated calls safe.
func NormalizeClaudeUsage(usage *OpenAIUsage) *OpenAIUsage {
	if usage == nil || usage.PromptCacheIncluded {
		return usage
	}
	usage.PromptTokens += usage.GetCachedTokens() + usage.CacheCreationInputTokens
	usage.PromptCacheIncluded = true
	return usage
}

// ParseClaudeUsage extracts usage from a Claude-format response body. Claude
// prompt cache counters are separate from input_tokens, so canonical OpenAI
// prompt_tokens must include cache reads and creation.
func ParseClaudeUsage(body []byte) *OpenAIUsage {
	var raw struct {
		Usage jsontext.Value `json:"usage"`
	}
	if json.Unmarshal(body, &raw) != nil || len(raw.Usage) == 0 || string(raw.Usage) == "null" {
		return nil
	}
	var keys map[string]jsontext.Value
	if json.Unmarshal(raw.Usage, &keys) != nil {
		return nil
	}
	if _, ok := keys["input_tokens"]; !ok {
		return nil
	}
	var u struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(raw.Usage, &u); err != nil {
		return nil
	}
	return NormalizeClaudeUsage(&OpenAIUsage{
		PromptTokens:             u.InputTokens,
		CompletionTokens:         u.OutputTokens,
		CachedTokens:             CachedTokensFromJSON(raw.Usage),
		CacheCreationInputTokens: u.CacheCreationInputTokens,
	})
}

// ParseResponseUsage extracts usage from an OpenAI or Claude response body.
func ParseResponseUsage(body []byte) *OpenAIUsage {
	if u := ParseClaudeUsage(body); u != nil {
		return u
	}
	var raw struct {
		Usage jsontext.Value `json:"usage"`
	}
	if json.Unmarshal(body, &raw) != nil || len(raw.Usage) == 0 || string(raw.Usage) == "null" {
		return nil
	}
	var usage OpenAIUsage
	if json.Unmarshal(raw.Usage, &usage) != nil {
		return nil
	}
	usage.CachedTokens = CachedTokensFromJSON(raw.Usage)
	usage.PromptCacheIncluded = true
	// The OpenAI families count reasoning_tokens INSIDE completion_tokens, so
	// pricing has to know the slice is a subset before it bills it
	// (decolua/9router #5634). Claude carries no reasoning field, so this is a
	// no-op there.
	usage.MarkReasoningSubset()
	return &usage
}

// FloorPromptTokens substitutes the gateway's body-size prompt estimate
// for a usage record's prompt total when that total is not credible
// against the request that was actually forwarded: reported as zero, or
// less than a quarter of the estimate. Some providers (MiniMax's mavis
// gateway, notably) count only the newest segment of a conversation, so
// a several-hundred-thousand-token prompt can close with prompt_tokens
// in the hundreds and every client's context meter reads ~0% on the
// turn. The estimate, derived from the exact forwarded body, is the
// truthful figure there. Credible totals are never overwritten: an
// honest reporter's count tracks the body within tokenizer variance,
// far above the quarter-estimate line. It reports whether it changed
// the record.
func FloorPromptTokens(usage *OpenAIUsage, estimate int) bool {
	if usage == nil || estimate <= 0 || usage.PromptTokens*4 >= estimate {
		return false
	}
	usage.PromptTokens = estimate
	if usage.TotalTokens < estimate+usage.CompletionTokens {
		usage.TotalTokens = estimate + usage.CompletionTokens
	}
	return true
}

// PatchPromptUsageFloor rewrites an OpenAI-shaped response body's usage
// block under the FloorPromptTokens rule: the upstream's prompt total
// was zero, omitted, or not credible against the forwarded request, so
// the gateway's body-size prompt estimate stands in.
func PatchPromptUsageFloor(body []byte, estimate int) ([]byte, bool) {
	if estimate <= 0 || len(body) == 0 {
		return body, false
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return body, false
	}
	usage, ok := doc["usage"].(map[string]any)
	if !ok {
		return body, false
	}
	if v, present := usageNumber(usage, "prompt_tokens"); present && v*4 >= estimate {
		return body, false
	}
	usage["prompt_tokens"] = estimate
	completion, _ := usageNumber(usage, "completion_tokens")
	if v, present := usageNumber(usage, "total_tokens"); !present || v < estimate+completion {
		usage["total_tokens"] = estimate + completion
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body, false
	}
	return out, true
}

// usageNumber reads an integer-valued field from a decoded usage block.
func usageNumber(usage map[string]any, key string) (int, bool) {
	v, ok := usage[key].(float64)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// ParseOllamaUsage extracts usage from an Ollama native response body, whose
// counters sit at the TOP level rather than under `usage`.
//
// It returns nil for a body that is not an Ollama completion: the `done` flag is
// what distinguishes the final chunk from the intermediate NDJSON lines, and a
// false positive here would fabricate a usage record for an OpenAI-shaped body.
func ParseOllamaUsage(body []byte) *OpenAIUsage {
	var raw struct {
		Done                  bool `json:"done"`
		PromptEvalCount       *int `json:"prompt_eval_count"`
		EvalCount             *int `json:"eval_count"`
		PromptEvalCachedCount *int `json:"prompt_eval_cached_count"`
	}
	if json.Unmarshal(body, &raw) != nil || !raw.Done {
		return nil
	}
	if raw.PromptEvalCount == nil && raw.EvalCount == nil {
		// `done` also ends a turn that published no counters at all. Nothing
		// to account for.
		return nil
	}
	prompt, completion := 0, 0
	if raw.PromptEvalCount != nil {
		prompt = *raw.PromptEvalCount
	}
	if raw.EvalCount != nil {
		completion = *raw.EvalCount
	}
	usage := &OpenAIUsage{
		PromptTokens:        prompt,
		CompletionTokens:    completion,
		PromptCacheIncluded: true,
	}
	if raw.PromptEvalCachedCount != nil {
		usage.CachedTokens = *raw.PromptEvalCachedCount
	}
	return usage
}

func IsClaudeMessageBody(body []byte) bool {
	var probe struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &probe) != nil {
		return false
	}
	return probe.Type == "message" && probe.Role == "assistant"
}

// TranslateOpenAIToClaude translates an OpenAI non-streaming response into a Claude non-streaming response.
func TranslateOpenAIToClaude(openaiResp []byte) ([]byte, *OpenAIUsage, error) {
	trimmed := bytes.TrimSpace(openaiResp)
	if len(trimmed) == 0 {
		return nil, nil, fmt.Errorf("empty response body")
	}

	var resp OpenAIResponse
	if err := json.Unmarshal(openaiResp, &resp); err != nil {
		return nil, nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	// An upstream that answers the Messages wire (an anthropic-compatible node,
	// minimax-code, a claude member inside a /v1/messages combo) reaches this
	// translate=true lane with a body that is already the shape the client
	// asked for. Relay it unchanged — "no choices" here turned a good answer
	// into a 502 and pushed the combo to the next member.
	if IsClaudeMessageBody(openaiResp) {
		return openaiResp, ParseClaudeUsage(openaiResp), nil
	}

	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("no choices in OpenAI response")
	}

	choice := resp.Choices[0]
	msg := choice.Message

	msgID := resp.ID
	if msgID == "" {
		msgID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	} else {
		msgID = strings.Replace(msgID, "chatcmpl-", "", 1)
		if msgID == "" || msgID == "chat" {
			msgID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
		}
	}
	modelName := resp.Model
	if modelName == "" {
		modelName = "claude-3-5-sonnet"
	}

	var contentBlocks []map[string]any

	// Reasoning
	reasoning := msg.ReasoningContent
	if reasoning == "" {
		reasoning = msg.Reasoning
	}
	cleanContent := msg.Content
	if strings.Contains(cleanContent, "<think>") {
		thinkPart, stripped := ExtractAndStripThink(cleanContent)
		cleanContent = stripped
		if reasoning == "" {
			reasoning = thinkPart
		}
	}
	if reasoning != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":     "thinking",
			"thinking": reasoning,
		})
	}

	// Text content
	if cleanContent != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": cleanContent,
		})
	}

	// Tool calls — don't fallback to ID when Function.Name is empty (see decolua/9router#2077, #3685)
	// Using call_ ID as tool name causes "No such tool available: call_..." in Claude Code.
	for _, tc := range msg.ToolCalls {
		if tc.Function == nil || tc.Function.Name == "" {
			continue
		}
		toolName := UncloakToolName(tc.Function.Name, nil)
		if toolName == "" {
			continue
		}
		var input jsontext.Value
		if tc.Function != nil && tc.Function.Arguments != "" {
			sanitized := sanitizeToolArgs(toolName, tc.Function.Arguments)
			input = SafeToolCallInput(sanitized)
		} else {
			input = jsontext.Value("{}")
		}
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  toolName,
			"input": input,
		})
	}

	if len(contentBlocks) == 0 {
		contentBlocks = []map[string]any{}
	}

	// Stop reason
	claudeStop := "end_turn"
	if choice.FinishReason != nil {
		switch *choice.FinishReason {
		case "stop":
			claudeStop = "end_turn"
		case "length":
			claudeStop = "max_tokens"
		case "tool_calls":
			claudeStop = "tool_use"
		case "content_filter":
			claudeStop = "refusal"
		}
	}

	// Usage
	inputTokens, outputTokens, cachedTokens, cacheCreationTokens := 0, 0, 0, 0
	totalTokens := 0
	var details *CompletionTokensDetails
	if resp.Usage != nil {
		inputTokens = resp.Usage.PromptTokens
		outputTokens = resp.Usage.CompletionTokens
		totalTokens = resp.Usage.TotalTokens
		details = resp.Usage.CompletionTokensDetails
		cachedTokens = resp.Usage.GetCachedTokens()
		cacheCreationTokens = resp.Usage.CacheCreationInputTokens
	}
	usage := &OpenAIUsage{
		PromptTokens:             inputTokens,
		CompletionTokens:         outputTokens,
		TotalTokens:              totalTokens,
		CachedTokens:             cachedTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CompletionTokensDetails:  details,
		PromptCacheIncluded:      true,
	}
	// The upstream usage was OpenAI-shaped, so the reasoning slice it carries
	// sits inside the output count (decolua/9router #5634).
	usage.MarkReasoningSubset()
	// Anthropic's input_tokens counts only uncached input; OpenAI's
	// prompt_tokens already includes the cached subset. Splitting them here
	// keeps Claude Code (which sums input + cache_read + cache_creation)
	// from double-counting the cached tokens.
	uncachedInput := inputTokens - cachedTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	claudeUsageMap := map[string]any{
		"input_tokens":  uncachedInput,
		"output_tokens": outputTokens,
	}
	if cachedTokens > 0 {
		claudeUsageMap["cache_read_input_tokens"] = cachedTokens
	}
	if cacheCreationTokens > 0 {
		claudeUsageMap["cache_creation_input_tokens"] = cacheCreationTokens
	}

	result := map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       contentBlocks,
		"stop_reason":   claudeStop,
		"stop_sequence": nil,
		"usage":         claudeUsageMap,
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal Claude response: %w", err)
	}
	return out, usage, nil
}

// UnwrapClineEnvelope unwraps {"success":true,"data":{...}} envelope from Cline/Clinepass
// non-streaming responses (parity with open-sse/shared/clineEnvelope.js #122f23ee).
func UnwrapClineEnvelope(body []byte) []byte {
	var env struct {
		Success bool           `json:"success"`
		Data    jsontext.Value `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Success && len(env.Data) > 0 && env.Data[0] == '{' {
		return []byte(env.Data)
	}
	return body
}
