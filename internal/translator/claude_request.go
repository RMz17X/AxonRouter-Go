package translator

// OpenAI → Claude *request* translator, ported from AxonRouter
// open-sse/translator/request/openai-to-claude.js. It exists for the
// translator playground's step 3 (POST /api/translator/translate,
// batch 10 item 5b): the provider-request construction preview.
//
// The chat pipeline does not call it — providers keep their own
// request shaping (internal/proxy/executor) — and nothing here
// mutates shared state. The port is faithful to the JS, including
// its quirks: the fixed Claude Code system prompt is always
// prepended, system text joins with "\n", tool_result blocks are
// split into their own user message, and the final pass merges
// consecutive same-role messages.
//
// Two deliberate deviations, both invisible in the step-3 output:
//   - The JS result's _toolNameMap is not emitted (Axon's tool-name
//     prefix is "" so the map is always empty, and the step-3 route
//     deletes the key before building anyway).
//   - The thinking-budget rule of adjustMaxTokens is carried by a
//     Claude-specific variant below; the package's existing
//     adjustMaxTokens (gemini/antigravity callers) is untouched.

import (
	json "encoding/json/v2"
	"strings"
)

// claudeCodeSystemPrompt is Axon's CLAUDE_SYSTEM_PROMPT
// (open-sse/config/appConstants.js), prepended to every
// OpenAI → Claude translation.
const claudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

func claudeToolsFromOpenAI(rawTools []any) []any {
	out := make([]any, 0, len(rawTools))
	for i, rawTool := range rawTools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			out = append(out, rawTool)
			continue
		}
		if t, hasType := tool["type"]; hasType && t != "function" {
			out = append(out, tool)
			continue
		}
		fn, hasFn := tool["function"].(map[string]any)
		if !hasFn {
			fn = tool
		}
		// A function tool without a name is uncallable in every dialect
		// and a whole-request rejection on strict Claude-format
		// upstreams (see sanitizeClaudeToolDefs); drop it here too so
		// the conversion path cannot mint one.
		if name, _ := fn["name"].(string); strings.TrimSpace(name) == "" {
			continue
		}
		description, _ := fn["description"].(string)
		schema := fn["parameters"]
		if schema == nil {
			schema = fn["input_schema"]
		}
		if _, isObj := schema.(map[string]any); !isObj {
			schema = map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
		}
		converted := map[string]any{
			"name": fn["name"], "description": description, "input_schema": schema,
		}
		if i == len(rawTools)-1 {
			converted["cache_control"] = map[string]any{"type": "ephemeral", "ttl": "1h"}
		}
		out = append(out, converted)
	}
	return out
}

// claudeToolChoiceFromOpenAI mirrors the tool_choice mapping of
// openai-to-claude.js.
func claudeToolChoiceFromOpenAI(tc any) any {
	switch v := tc.(type) {
	case string:
		if v == "required" {
			return map[string]any{"type": "any"}
		}
		return map[string]any{"type": "auto"}
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			return map[string]any{"type": "tool", "name": fn["name"]}
		}
		switch v["type"] {
		case "auto", "any", "tool", "none":
			return v
		}
		return map[string]any{"type": "auto"}
	}
	return map[string]any{"type": "auto"}
}

// model's declared max output tokens (0 = unknown).
func TranslateOpenAIRequestToClaude(model string, body []byte, stream bool, maxOutputCeiling int) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}

	rawMessages, _ := in["messages"].([]any)
	messages, systemParts := claudeMessagesFromOpenAI(rawMessages)

	// response_format → system prompt guidance (JS order: after
	// message conversion, appended to systemParts before assembly).
	if rawFmt, ok := in["response_format"].(map[string]any); ok {
		switch rawFmt["type"] {
		case "json_schema":
			name := "response"
			if n, ok := rawFmt["name"].(string); ok && n != "" {
				name = n
			} else if js, ok := rawFmt["json_schema"].(map[string]any); ok {
				if n, ok := js["name"].(string); ok && n != "" {
					name = n
				}
			}
			schema := rawFmt["schema"]
			if schema == nil {
				if js, ok := rawFmt["json_schema"].(map[string]any); ok {
					schema = js["schema"]
				}
			}
			if schema == nil {
				schema = map[string]any{}
			}
			schemaJSON, _ := json.Marshal(schema)
			systemParts = append(systemParts, "You are an expert at structured data extraction. Extract data from the conversation and return it as JSON matching this schema.\n\nSchema name: "+name+"\nSchema: "+string(schemaJSON)+"\n\nRespond with ONLY the JSON object, no other text.")
		case "json_object":
			systemParts = append(systemParts, "You must respond with valid JSON only. No other text, no markdown code fences.")
		}
	}

	result := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}

	maxTokens, _ := in["max_tokens"].(float64)
	rawTools, hasTools := in["tools"].([]any)
	var thinkingBudget float64
	if thinking, ok := in["thinking"].(map[string]any); ok {
		thinkingBudget, _ = thinking["budget_tokens"].(float64)
	}
	result["max_tokens"] = adjustMaxTokensForClaude(maxTokens, hasTools && len(rawTools) > 0, thinkingBudget, float64(maxOutputCeiling))

	if temp, ok := in["temperature"]; ok {
		result["temperature"] = temp
	}

	// System: the Claude Code prompt always comes first; the
	// request's own system text follows as a cached block.
	system := []any{map[string]any{"type": "text", "text": claudeCodeSystemPrompt}}
	if len(systemParts) > 0 {
		system = append(system, map[string]any{
			"type":          "text",
			"text":          strings.Join(systemParts, "\n"),
			"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		})
	}
	result["system"] = system

	if hasTools && len(rawTools) > 0 {
		result["tools"] = claudeToolsFromOpenAI(rawTools)
	}
	if tc, ok := in["tool_choice"]; ok && tc != nil {
		result["tool_choice"] = claudeToolChoiceFromOpenAI(tc)
	}

	addClaudeCacheControl(messages)

	return json.Marshal(result)
}
