package translator

// Gemini → OpenAI and Antigravity → OpenAI *request* translators,
// ported from AxonRouter open-sse/translator/request/gemini-to-openai.js
// and antigravity-to-openai.js (the translator registry's dedicated
// antigravity translator — separate from the gemini one: it unwraps the
// request envelope and additionally handles thought parts, topK,
// thinkingConfig → reasoning_effort and schema-type normalization).
//
// These exist for the translator playground's step 2
// (POST /api/translator/translate, batch 9 item 5a). The chat pipeline
// does not call them — providers keep their own request shaping — and
// nothing here mutates shared state.

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
)

// gemini4ModelPattern is Axon's isGemini4Model
// (open-sse/providers/capabilities.js): /(?:^|\/)gemini-4(?:$|[-.])/i.
// The package's existing isGemini4Model serves the Gemini-native chat
// path with a looser substring rule; the playground translators follow
// the Axon translator's own gate, so they use this pattern instead.
var gemini4ModelPattern = regexp.MustCompile(`(?i)(?:^|/)gemini-4(?:$|[-.])`)

func isGemini4ModelAxon(model string) bool {
	return gemini4ModelPattern.MatchString(model)
}

// adjustMaxTokens mirrors open-sse/translator/formats/maxTokens.js
// with the default ceiling (DEFAULT_MAX_TOKENS 64000,
// DEFAULT_MIN_TOKENS 32000 from open-sse/config/runtimeConfig.js). The
// gemini/antigravity callers pass {max_tokens, tools} only, so the
// Claude thinking-budget rule of the JS helper can never fire here.
func adjustMaxTokens(maxTokens float64, hasTools bool) float64 {
	if maxTokens == 0 {
		maxTokens = 64000
	}
	if hasTools && maxTokens < 32000 {
		maxTokens = 32000
	}
	if maxTokens > 64000 {
		maxTokens = 64000
	}
	return maxTokens
}

// budgetToEffort mirrors open-sse/translator/concerns/thinking.js.
func budgetToEffort(budget float64) string {
	if budget <= 0 {
		return ""
	}
	if budget <= 2048 {
		return "low"
	}
	if budget <= 16384 {
		return "medium"
	}
	return "high"
}

// jsTruthy mirrors JS truthiness for JSON-derived values: nil, false,
// 0 and "" are falsy; every object/array (even empty) is truthy.
func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	}
	return true
}

// jsStringArg renders a functionCall args value the way
// JSON.stringify(args || {}) does in the JS translators.
func jsStringifyArgs(args any) string {
	if !jsTruthy(args) {
		args = map[string]any{}
	}
	out, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// jsStringifyValue renders the functionResponse content the way
// JSON.stringify(response?.result || response || {}) does.
func jsStringifyResponse(response any) string {
	var val any
	if m, ok := response.(map[string]any); ok {
		val = m["result"]
	}
	if !jsTruthy(val) {
		val = response
	}
	if !jsTruthy(val) {
		val = map[string]any{}
	}
	out, err := json.Marshal(val)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// collapseGeminiTextParts mirrors collapseTextParts in
// open-sse/translator/concerns/message.js (distinct from the claude
// path's collapseTextParts in request.go, whose rules differ).
func collapseGeminiTextParts(parts []map[string]any) any {
	if len(parts) == 0 {
		return ""
	}
	allText := true
	for _, p := range parts {
		if p["type"] != openaiBlockText {
			allText = false
			break
		}
		if _, isStr := p["text"].(string); !isStr {
			allText = false
			break
		}
	}
	if allText {
		texts := make([]string, 0, len(parts))
		for _, p := range parts {
			texts = append(texts, p["text"].(string))
		}
		return strings.Join(texts, "\n")
	}
	if len(parts) == 1 && parts[0]["type"] == openaiBlockText {
		return parts[0]["text"]
	}
	return parts
}

// extractGeminiText mirrors extractGeminiText / extractText in the JS
// translators: a bare string is itself; an object contributes the
// concatenation of its parts' text.
func extractGeminiText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	m, ok := content.(map[string]any)
	if !ok {
		return ""
	}
	parts, ok := m["parts"].([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok {
			if s, ok := pm["text"].(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

func geminiImagePart(inlineData map[string]any) map[string]any {
	mime, _ := inlineData["mimeType"].(string)
	data, _ := inlineData["data"].(string)
	return map[string]any{
		"type": openaiBlockImageURL,
		"image_url": map[string]any{
			"url": "data:" + mime + ";base64," + data,
		},
	}
}

func geminiToolCall(fc map[string]any) map[string]any {
	name, _ := fc["name"].(string)
	id, _ := fc["id"].(string)
	if id == "" {
		id = "call_" + name
	}
	return map[string]any{
		"id":   id,
		"type": openaiBlockFunction,
		"function": map[string]any{
			"name":      name,
			"arguments": jsStringifyArgs(fc["args"]),
		},
	}
}

func geminiToolResult(fr map[string]any) map[string]any {
	name, _ := fr["name"].(string)
	id, _ := fr["id"].(string)
	if id == "" {
		id = "call_" + name
	}
	return map[string]any{
		"role":         roleTool,
		"tool_call_id": id,
		"content":      jsStringifyResponse(fr["response"]),
	}
}

// geminiGenerationConfig applies the generationConfig subset both JS
// translators share (maxOutputTokens, temperature, topP) onto result.
// req is the (possibly unwrapped) request object tools are read from.
func geminiGenerationConfig(result map[string]any, req map[string]any, model string) {
	config, ok := req["generationConfig"].(map[string]any)
	if !ok {
		return
	}
	if raw, present := config["maxOutputTokens"]; present && jsTruthy(raw) {
		if n, isNum := raw.(float64); isNum {
			tools, _ := req["tools"].([]any)
			result["max_tokens"] = adjustMaxTokens(n, len(tools) > 0)
		}
	}
	allowSampling := !isGemini4ModelAxon(model)
	if !allowSampling {
		return
	}
	if v, present := config["temperature"]; present {
		result["temperature"] = v
	}
	if v, present := config["topP"]; present {
		result["top_p"] = v
	}
}

// geminiTools converts Gemini functionDeclarations into OpenAI tools.
// normalizeSchemas selects the antigravity variant (schema types
// lowercased, enumDescriptions stripped); the gemini variant passes
// parameters through untouched.
func geminiTools(req map[string]any, normalizeSchemas bool) ([]any, bool) {
	tools, ok := req["tools"].([]any)
	if !ok {
		return nil, false
	}
	out := []any{}
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		decls, ok := tool["functionDeclarations"].([]any)
		if !ok {
			continue
		}
		for _, d := range decls {
			fn, ok := d.(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			description, _ := fn["description"].(string)
			params := fn["parameters"]
			if normalizeSchemas {
				params = normalizeAntigravitySchema(params)
			}
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			out = append(out, map[string]any{
				"type": openaiBlockFunction,
				"function": map[string]any{
					"name":        name,
					"description": description,
					"parameters":  params,
				},
			})
		}
	}
	return out, true
}

// TranslateGeminiRequestToOpenAI converts a Gemini generateContent
// request body into an OpenAI chat-completions body, mirroring
// geminiToOpenAIRequest(model, body, stream). The model/stream pair is
// the playground route's parse of the client body (parseModel +
// stream !== false), exactly as the JS registry receives them.
func TranslateGeminiRequestToOpenAI(model string, body []byte, stream bool) ([]byte, error) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("translator.TranslateGeminiRequestToOpenAI: %w", err)
	}
	if parsed == nil {
		parsed = map[string]any{}
	}

	result := map[string]any{
		"messages": []any{},
		"stream":   stream,
	}
	if model != "" {
		result["model"] = model
	}
	messages := []any{}

	geminiGenerationConfig(result, parsed, model)

	if _, present := parsed["systemInstruction"]; present {
		if text := extractGeminiText(parsed["systemInstruction"]); text != "" {
			messages = append(messages, map[string]any{
				"role":    roleSystem,
				"content": text,
			})
		}
	}

	if contents, ok := parsed["contents"].([]any); ok {
		for _, c := range contents {
			content, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if converted := convertGeminiContent(content); converted != nil {
				messages = append(messages, converted)
			}
		}
	}
	result["messages"] = messages

	if tools, present := geminiTools(parsed, false); present {
		result["tools"] = tools
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("translator.TranslateGeminiRequestToOpenAI: %w", err)
	}
	return out, nil
}

// convertGeminiContent mirrors convertGeminiContent in
// gemini-to-openai.js. A functionResponse part short-circuits the whole
// content into a single tool message, as in the JS.
func convertGeminiContent(content map[string]any) map[string]any {
	role := "assistant"
	if content["role"] == "user" {
		role = roleUser
	}
	rawParts, ok := content["parts"].([]any)
	if !ok {
		return nil
	}

	parts := []map[string]any{}
	toolCalls := []map[string]any{}
	for _, p := range rawParts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if text, present := part["text"]; present {
			parts = append(parts, map[string]any{
				"type": openaiBlockText,
				"text": text,
			})
		}
		if inline, ok := part["inlineData"].(map[string]any); ok {
			parts = append(parts, geminiImagePart(inline))
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			toolCalls = append(toolCalls, geminiToolCall(fc))
		}
		if fr, ok := part["functionResponse"].(map[string]any); ok {
			return geminiToolResult(fr)
		}
	}

	if len(toolCalls) > 0 {
		msg := map[string]any{"role": "assistant"}
		if len(parts) > 0 {
			if len(parts) == 1 {
				// JS: content = parts[0].text — undefined (key dropped)
				// when the single part carries no text.
				if text, present := parts[0]["text"]; present {
					msg["content"] = text
				}
			} else {
				msg["content"] = parts
			}
		}
		msg["tool_calls"] = toolCalls
		return msg
	}

	if len(parts) > 0 {
		return map[string]any{
			"role":    role,
			"content": collapseGeminiTextParts(parts),
		}
	}
	return nil
}

// TranslateAntigravityRequestToOpenAI converts an Antigravity request
// envelope ({request: {...}, model, ...}) into an OpenAI
// chat-completions body, mirroring antigravityToOpenAIRequest(model,
// body, stream).
func TranslateAntigravityRequestToOpenAI(model string, body []byte, stream bool) ([]byte, error) {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("translator.TranslateAntigravityRequestToOpenAI: %w", err)
	}
	if parsed == nil {
		parsed = map[string]any{}
	}
	req := parsed
	if inner, ok := parsed["request"].(map[string]any); ok {
		req = inner
	}

	result := map[string]any{
		"messages": []any{},
		"stream":   stream,
	}
	if model != "" {
		result["model"] = model
	}
	messages := []any{}

	geminiGenerationConfig(result, req, model)
	if config, ok := req["generationConfig"].(map[string]any); ok {
		allowSampling := !isGemini4ModelAxon(model)
		if allowSampling {
			if v, present := config["topK"]; present {
				result["top_k"] = v
			}
		}
		if thinking, ok := config["thinkingConfig"].(map[string]any); ok {
			budget, _ := thinking["thinkingBudget"].(float64)
			if effort := budgetToEffort(budget); effort != "" {
				result["reasoning_effort"] = effort
			}
		}
	}

	if _, present := req["systemInstruction"]; present {
		if text := extractGeminiText(req["systemInstruction"]); text != "" {
			messages = append(messages, map[string]any{
				"role":    roleSystem,
				"content": text,
			})
		}
	}

	if contents, ok := req["contents"].([]any); ok {
		for _, c := range contents {
			content, ok := c.(map[string]any)
			if !ok {
				continue
			}
			for _, converted := range convertAntigravityContent(content) {
				messages = append(messages, converted)
			}
		}
	}
	result["messages"] = messages

	if tools, present := geminiTools(req, true); present {
		result["tools"] = tools
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("translator.TranslateAntigravityRequestToOpenAI: %w", err)
	}
	return out, nil
}

// convertAntigravityContent mirrors convertContent in
// antigravity-to-openai.js; it returns zero or more messages (a
// content carrying functionResponse parts yields one tool message per
// response, plus an assistant message for co-located calls/text).
func convertAntigravityContent(content map[string]any) []map[string]any {
	var role any
	switch content["role"] {
	case "model":
		role = "assistant"
	case "user":
		role = roleUser
	default:
		role = content["role"]
	}
	rawParts, ok := content["parts"].([]any)
	if !ok {
		return nil
	}

	textParts := []map[string]any{}
	toolCalls := []map[string]any{}
	toolResults := []map[string]any{}
	reasoningContent := ""

	for _, p := range rawParts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		// Thinking content (thought: true).
		if part["thought"] == true {
			if s, isStr := part["text"].(string); isStr && s != "" {
				reasoningContent += s
				continue
			}
		}
		// Text with thoughtSignature = regular text after thinking.
		if jsTruthy(part["thoughtSignature"]) {
			if text, present := part["text"]; present {
				if jsTruthy(text) {
					textParts = append(textParts, map[string]any{
						"type": openaiBlockText,
						"text": text,
					})
				}
				continue
			}
		}
		// Regular text.
		if text, present := part["text"]; present && text != "" {
			textParts = append(textParts, map[string]any{
				"type": openaiBlockText,
				"text": text,
			})
		}
		// Inline data (images).
		if inline, ok := part["inlineData"].(map[string]any); ok {
			textParts = append(textParts, geminiImagePart(inline))
		}
		// Function call.
		if fc, ok := part["functionCall"].(map[string]any); ok {
			toolCalls = append(toolCalls, geminiToolCall(fc))
		}
		// Function response → one tool message each.
		if fr, ok := part["functionResponse"].(map[string]any); ok {
			toolResults = append(toolResults, geminiToolResult(fr))
		}
	}

	if len(toolResults) > 0 {
		if len(toolCalls) > 0 || len(textParts) > 0 || reasoningContent != "" {
			assistantMsg := map[string]any{"role": "assistant"}
			if len(textParts) > 0 {
				assistantMsg["content"] = collapseGeminiTextParts(textParts)
			}
			if reasoningContent != "" {
				assistantMsg["reasoning_content"] = reasoningContent
			}
			if len(toolCalls) > 0 {
				assistantMsg["tool_calls"] = toolCalls
			}
			return append(toolResults, assistantMsg)
		}
		return toolResults
	}

	if len(toolCalls) > 0 {
		msg := map[string]any{"role": "assistant"}
		if len(textParts) > 0 {
			msg["content"] = collapseGeminiTextParts(textParts)
		}
		if reasoningContent != "" {
			msg["reasoning_content"] = reasoningContent
		}
		msg["tool_calls"] = toolCalls
		return []map[string]any{msg}
	}

	if len(textParts) > 0 || reasoningContent != "" {
		msg := map[string]any{}
		if role != nil {
			msg["role"] = role
		}
		if len(textParts) > 0 {
			msg["content"] = collapseGeminiTextParts(textParts)
		}
		if reasoningContent != "" {
			msg["reasoning_content"] = reasoningContent
		}
		return []map[string]any{msg}
	}
	return nil
}

// normalizeAntigravitySchema mirrors normalizeSchemaTypes in
// antigravity-to-openai.js: string type values lowercased,
// enumDescriptions stripped, properties/items recursed.
func normalizeAntigravitySchema(schema any) any {
	switch v := schema.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, val := range v {
			out[key] = val
		}
		if t, isStr := out["type"].(string); isStr {
			out["type"] = strings.ToLower(t)
		}
		delete(out, "enumDescriptions")
		if props, ok := out["properties"].(map[string]any); ok {
			normalized := make(map[string]any, len(props))
			for key, val := range props {
				normalized[key] = normalizeAntigravitySchema(val)
			}
			out["properties"] = normalized
		}
		if items, present := out["items"]; present {
			out["items"] = normalizeAntigravitySchema(items)
		}
		return out
	case []any:
		out := make([]any, len(v))
		copy(out, v)
		return out
	}
	return schema
}
