package handlers

// Batch 6 — POST /api/translator/translate, ported from AxonRouter
// src/app/api/translator/translate/route.ts. Step 1 identifies the
// source format of a client body (detectFormat mirrors
// open-sse/services/provider.js heuristics) and the provider's target
// format; step 2 converts the body to OpenAI shape; step 3 builds
// the provider request (URL + headers + target body) — implemented
// in batch10_translator_step3.go (batch 10 item 5b) as additive
// build-only constructors over the registry + active connection,
// without touching the live executor path.
//
// PORT STATUS (recorded in .changes/917, step 2 completed in
// .changes/926, step 3 in .changes/929): steps 1-2 are implemented
// for every source format detectTranslatorFormat can return —
// openai = identity, claude = the real Claude→OpenAI request
// translator, openai-responses = the chat layer's Responses→Chat
// request translator, gemini / antigravity = the dedicated request
// translators in internal/translator (gemini_request.go). Step 3
// covers the providers Axon serves with its Base/DefaultExecutor;
// specialized-executor providers answer Axon's catch shape
// {success:false, error} at 500 with the executor named.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/handlerutil"
	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/translator"
)

// HandleTranslatorTranslate serves POST /api/translator/translate.
func HandleTranslatorTranslate(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Step     *int            `json:"step"`
		Body     json.RawMessage `json:"body"`
		Provider string          `json:"provider"`
		Model    string          `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		translatorFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reqBody.Step == nil || len(reqBody.Body) == 0 {
		translatorFail(w, http.StatusBadRequest, "Step and body required")
		return
	}
	step := *reqBody.Step
	if step < 1 || step > 3 {
		translatorFail(w, http.StatusBadRequest, "Invalid step (1-3)")
		return
	}

	// clientBody = body.body || body
	clientBody := reqBody.Body
	var probe map[string]any
	if err := json.Unmarshal(reqBody.Body, &probe); err == nil {
		if inner, ok := probe["body"]; ok && inner != nil {
			if raw, err := json.Marshal(inner); err == nil {
				clientBody = raw
			}
		}
	}
	var clientMap map[string]any
	_ = json.Unmarshal(clientBody, &clientMap)
	if clientMap == nil {
		clientMap = map[string]any{}
	}

	switch step {
	case 1:
		modelStr, _ := clientMap["model"].(string)
		provider, model := parseTranslatorModel(modelStr)
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result": map[string]any{
				"provider":     provider,
				"model":        model,
				"sourceFormat": detectTranslatorFormat(clientMap, r.UserAgent()),
				"targetFormat": translatorTargetFormat(provider),
			},
		})
	case 2:
		sourceFormat := detectTranslatorFormat(clientMap, r.UserAgent())
		// Axon's step 2 hands the registry the parseModel'd model and
		// stream = clientBody.stream !== false (default true).
		modelStr, _ := clientMap["model"].(string)
		_, parsedModel := parseTranslatorModel(modelStr)
		model, _ := parsedModel.(string)
		stream := true
		if s, ok := clientMap["stream"].(bool); ok && !s {
			stream = false
		}
		converted, errStr := translateToOpenAI(sourceFormat, clientBody, model, stream)
		if errStr != "" {
			translatorFail(w, http.StatusInternalServerError, errStr)
			return
		}
		var resultMap map[string]any
		if err := json.Unmarshal(converted, &resultMap); err != nil {
			translatorFail(w, http.StatusInternalServerError, err.Error())
			return
		}
		delete(resultMap, "_toolNameMap")
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"body": resultMap},
		})
	case 3:
		if reqBody.Provider == "" || reqBody.Model == "" {
			translatorFail(w, http.StatusBadRequest, "provider and model required")
			return
		}
		status, result, errMsg := translatorStep3(reqBody.Provider, reqBody.Model, clientBody, clientMap)
		if errMsg != "" {
			translatorFail(w, status, errMsg)
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  result,
		})
	}
}

// parseTranslatorModel mirrors open-sse/services/model.js parseModel
// for the provider/model and bare forms: a slash form counts only when
// its prefix is a known provider id or alias; anything else is a bare
// model alias with a null provider.
func parseTranslatorModel(modelStr string) (any, any) {
	if modelStr == "" {
		return nil, nil
	}
	if slash := strings.Index(modelStr, "/"); slash >= 0 {
		prefix, rest := modelStr[:slash], modelStr[slash+1:]
		resolved := providers.GetProviderAlias(prefix)
		if _, known := providers.KnownProviders[prefix]; known {
			return prefix, rest
		}
		if _, known := providers.KnownProviders[resolved]; known || resolved != prefix {
			return resolved, rest
		}
	}
	return nil, modelStr
}

// translatorTargetFormat derives Axon's getTargetFormat from the Go
// provider registry: anthropic-compatible → "claude", gemini-native →
// "gemini", antigravity → "antigravity", everything else "openai"
// (the registry carries no per-node responses apiType distinction).
func translatorTargetFormat(provider any) string {
	id, _ := provider.(string)
	if id == "" {
		return "openai"
	}
	if id == "antigravity" {
		return "antigravity"
	}
	if id == "anthropic" || strings.HasPrefix(id, "anthropic-compatible") {
		return "claude"
	}
	if cfg, ok := providers.KnownProviders[id]; ok {
		if cfg.UpstreamIsAnthropic {
			return "claude"
		}
		if cfg.IsGeminiNative() {
			return "gemini"
		}
	}
	return "openai"
}

// detectTranslatorFormat mirrors detectFormat in
// open-sse/services/provider.js, heuristic for heuristic.
func detectTranslatorFormat(body map[string]any, userAgent string) string {
	modelStr, _ := body["model"].(string)
	_, hasMessages := body["messages"]

	if _, hasInput := body["input"]; hasInput && !hasMessages {
		if _, isArr := body["input"].([]any); isArr {
			return "openai-responses"
		}
		if _, isStr := body["input"].(string); isStr {
			return "openai-responses"
		}
	}
	if reqMap, ok := body["request"].(map[string]any); ok {
		if _, hasContents := reqMap["contents"]; hasContents && userAgent == "antigravity" {
			return "antigravity"
		}
	}
	if _, hasContents := body["contents"].([]any); hasContents {
		return "gemini"
	}

	// OpenAI-specific indicators.
	for _, key := range []string{"stream_options", "response_format", "logit_bias", "user"} {
		if _, present := body[key]; present {
			return "openai"
		}
	}
	for _, key := range []string{"logprobs", "top_logprobs", "n", "presence_penalty", "frequency_penalty"} {
		if v, present := body[key]; present && v != nil {
			return "openai"
		}
	}

	// Claude message-structure heuristics.
	if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
		if first, ok := messages[0].(map[string]any); ok {
			if content, ok := first["content"].([]any); ok && len(content) > 0 {
				if firstPart, ok := content[0].(map[string]any); ok &&
					firstPart["type"] == "text" && !strings.Contains(modelStr, "/") {
					if _, hasSystem := body["system"]; hasSystem {
						return "claude"
					}
					if _, hasVersion := body["anthropic_version"]; hasVersion {
						return "claude"
					}
				}
				for _, item := range content {
					part, ok := item.(map[string]any)
					if !ok {
						continue
					}
					switch part["type"] {
					case "image":
						if src, ok := part["source"].(map[string]any); ok && src["type"] == "base64" {
							return "claude"
						}
					case "image_url":
						if iu, ok := part["image_url"].(map[string]any); ok {
							if _, hasURL := iu["url"]; hasURL {
								return "openai"
							}
						}
					case "tool_use", "tool_result":
						return "claude"
					}
				}
			}
		}
	}

	if _, hasSystem := body["system"]; hasSystem {
		return "claude"
	}
	if _, hasVersion := body["anthropic_version"]; hasVersion {
		return "claude"
	}
	return "openai"
}

// translateToOpenAI runs step 2 for every source format the detector
// can return; other sources report the missing translator.
func translateToOpenAI(sourceFormat string, clientBody []byte, model string, stream bool) ([]byte, string) {
	switch sourceFormat {
	case "openai":
		return clientBody, ""
	case "claude":
		out, err := translator.TranslateClaudeToOpenAI(clientBody)
		if err != nil {
			return nil, err.Error()
		}
		return out, ""
	case "openai-responses":
		res, err := translator.ResponsesToChatRequest(clientBody)
		if err != nil {
			return nil, err.Error()
		}
		return res.Body, ""
	case "gemini":
		out, err := translator.TranslateGeminiRequestToOpenAI(model, clientBody, stream)
		if err != nil {
			return nil, err.Error()
		}
		return out, ""
	case "antigravity":
		out, err := translator.TranslateAntigravityRequestToOpenAI(model, clientBody, stream)
		if err != nil {
			return nil, err.Error()
		}
		return out, ""
	default:
		return nil, fmt.Sprintf("no request translator registered for format %q -> \"openai\" in the Go translator registry", sourceFormat)
	}
}
