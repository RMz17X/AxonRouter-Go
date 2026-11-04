package handlers

// Batch 10 item 5b — translator step 3: provider-request
// construction for POST /api/translator/translate, ported from
// AxonRouter src/app/api/translator/translate/route.ts (case 3)
// over open-sse's executor surface (buildUrl / buildHeaders /
// transformRequest in executors/base.js + executors/default.js).
//
// Owner order 2026-11-04 22:05 WIB ("selesaikan semua") lifted the
// batch-6 deferral. The implementation is ADDITIVE and build-only:
// the three construction steps are re-implemented here from the
// registry data (internal/providers) and the active connection's
// stored credentials; nothing is sent anywhere, and the live chat
// path (internal/proxy/executor) is not touched — where the chat
// path's private helpers do the same job (e.g. its OpenAI→Claude
// message conversion), the logic is duplicated in
// internal/translator/claude_request.go rather than moved.
//
// Scope, mirroring Axon's executor registry (executors/index.js):
// providers Axon serves with the Base/DefaultExecutor get the full
// construction. Providers with a SPECIALIZED executor (its own
// buildUrl/buildHeaders/transformRequest) keep a truthful
// {success:false, error} at 500 naming the executor — the same
// envelope Axon uses for its catch-all, never a fabricated result.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// step3SpecializedExecutors maps canonical provider ids to the
// Axon executor class that owns their request construction
// (open-sse/executors/index.js; a provider is listed when its
// executor defines at least one of buildUrl / buildHeaders /
// transformRequest). Aliases that Axon maps to a specialized
// executor and Go's alias map does not claim for another provider
// are listed too and are checked after canonicalization. "cc" is
// deliberately absent: in Go's registry cc is the alias of the
// "claude" provider (DefaultExecutor in Axon), not of commandcode.
var step3SpecializedExecutors = map[string]string{
	"antigravity":      "AntigravityExecutor",
	"azure":            "AzureOpenAIExecutor",
	"bedrock":          "BedrockExecutor",
	"bedrock-xai":      "BedrockExecutor",
	"codebuddy-cn":     "CodeBuddyCnExecutor",
	"codebuddy-intl":   "CodeBuddyCnExecutor",
	"codex":            "CodexExecutor",
	"commandcode":      "CommandCodeExecutor",
	"cursor":           "CursorExecutor",
	"freebuff":         "FreebuffExecutor",
	"gemini-cli":       "GeminiCLIExecutor",
	"github":           "GitHubExecutor",
	"grok-cli":         "GrokCliExecutor",
	"iflow":            "IFlowExecutor",
	"kilocode-free":    "KilocodeFreeExecutor",
	"kimchi":           "KimchiExecutor",
	"kiro":             "KiroExecutor",
	"llmtech-free":     "LlmtechFreeExecutor",
	"ollama-local":     "OllamaLocalExecutor",
	"opencode":         "OpenCodeExecutor",
	"opencode-go":      "OpenCodeGoExecutor",
	"opencode-zen":     "OpenCodeZenExecutor",
	"openrouter":       "OpenRouterExecutor",
	"qoder":            "QoderExecutor",
	"tokenharbor":      "TokenHarborExecutor",
	"trae":             "TraeExecutor",
	"vertex":           "VertexExecutor",
	"vertex-partner":   "VertexExecutor",
	"windsurf":         "WindsurfExecutor",
	"workbuddy":        "WorkbuddyExecutor",
	"xiaomi-mimo":      "XiaomiMimoExecutor",
	"xiaomi-tokenplan": "XiaomiTokenPlanExecutor",
	// Axon executor-registry aliases for the same classes.
	"cu": "CursorExecutor", "kcf": "KilocodeFreeExecutor",
	"kf": "KilocodeFreeExecutor", "gcli": "GeminiCLIExecutor",
	"gb": "GeminiCLIExecutor", "ltf": "LlmtechFreeExecutor",
	"th": "TokenHarborExecutor", "zen": "OpenCodeZenExecutor",
	"ocz": "OpenCodeZenExecutor",
}

// step3Credentials is the subset of a connection's stored data the
// construction reads — the same fields Axon's route assembles from
// getActiveProviderConnection (apiKey, accessToken, refreshToken,
// copilotToken, projectId, providerSpecificData). copilotToken has
// no field in Go's stored connection data and is only consumed by
// the GitHub executor (specialized, gated above), so it is not
// carried here.
type step3Credentials struct {
	APIKey               string         `json:"apiKey"`
	AccessToken          string         `json:"accessToken"`
	BaseURL              string         `json:"baseUrl"`
	ProviderSpecificData map[string]any `json:"providerSpecificData"`
}

func (c *step3Credentials) psdString(key string) string {
	if c.ProviderSpecificData == nil {
		return ""
	}
	s, _ := c.ProviderSpecificData[key].(string)
	return s
}

// connectionBaseURL is the connection's own upstream base: the
// top-level baseUrl the dashboard stores, else the
// providerSpecificData.baseUrl Axon's compat branches read.
func (c *step3Credentials) connectionBaseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return c.psdString("baseUrl")
}

// step3TargetFormat mirrors Axon's getTargetFormat
// (open-sse/services/provider.js) as far as Go's registry carries
// it: providers whose wire auth is x-api-key are Axon's
// format:"claude" providers (anthropic, claude) and translate to
// Claude Messages; everything else defers to the batch-6 helper
// (which itself never returns "openai-responses" — Go's registry
// carries no per-node responses apiType distinction, the same
// limitation batch 6 recorded for step 1).
func step3TargetFormat(provider string, cfg providers.ProviderConfig, known bool) string {
	if known && cfg.AuthHeader == "x-api-key" {
		return "claude"
	}
	return translatorTargetFormat(provider)
}

// translatorStep3 runs Axon's route case 3: translate the OpenAI
// intermediate body to the provider's target format, resolve the
// provider's active connection, then build {url, headers, body}.
// Returns (status, result, errorMessage); errorMessage is "" on
// success.
func translatorStep3(provider, model string, clientBody []byte, clientMap map[string]any) (int, map[string]any, string) {
	// Canonicalize the way the rest of the gateway does: Axon's
	// registry and Go's connection store are both keyed by the
	// canonical provider id.
	if _, ok := providers.KnownProviders[provider]; !ok {
		if canonical, ok := providers.ProviderAliasMap[provider]; ok {
			provider = canonical
		}
	}

	if executor, gated := step3SpecializedExecutors[provider]; gated {
		return http.StatusInternalServerError, nil, fmt.Sprintf(
			"step 3 is not available for provider %q in the Go port: Axon builds its request with the specialized %s executor, whose build-only construction surface is not mirrored here",
			provider, executor)
	}

	// Effective registry config, mirroring Axon's
	// getProviderConfig: compat node prefixes spread the base
	// provider's config; an unknown provider falls back to the
	// openai config.
	cfg, known := providers.KnownProviders[provider]
	switch {
	case strings.HasPrefix(provider, "openai-compatible-"):
		cfg, known = providers.KnownProviders["openai"], true
	case strings.HasPrefix(provider, "anthropic-compatible-"):
		cfg, known = providers.KnownProviders["anthropic"], true
	case !known:
		cfg = providers.KnownProviders["openai"]
	}

	stream := true
	if v, ok := clientMap["stream"].(bool); ok {
		stream = v
	}

	// translateRequest(OPENAI → targetFormat).
	var translated []byte
	switch step3TargetFormat(provider, cfg, known) {
	case "openai":
		translated = clientBody
	case "claude":
		ceiling := providers.GetCapabilitiesDetailForModel(provider, model).MaxOutput
		out, err := translator.TranslateOpenAIRequestToClaude(model, clientBody, stream, ceiling)
		if err != nil {
			return http.StatusInternalServerError, nil, err.Error()
		}
		translated = out
	case "gemini":
		out, err := translator.TranslateOpenAIToGemini(clientBody)
		if err != nil {
			return http.StatusInternalServerError, nil, err.Error()
		}
		translated = out
	default:
		return http.StatusInternalServerError, nil, fmt.Sprintf(
			"step 3 is not available for provider %q in the Go port: no OpenAI request translator is registered for its target format", provider)
	}

	// getActiveProviderConnection: the first (highest-priority)
	// active connection.
	sqlDB, err := db.GetConnection()
	if err != nil {
		return http.StatusInternalServerError, nil, err.Error()
	}
	conns, err := db.NewRepo(sqlDB).GetProviderConnections(provider, true)
	if err != nil {
		return http.StatusInternalServerError, nil, err.Error()
	}
	if len(conns) == 0 {
		return http.StatusBadRequest, nil, "No active connection for provider: " + provider
	}
	var creds step3Credentials
	_ = json.Unmarshal([]byte(conns[0].Data), &creds)

	url, err := step3BuildURL(provider, cfg, model, stream, &creds)
	if err != nil {
		return http.StatusInternalServerError, nil, err.Error()
	}
	headers := step3BuildHeaders(provider, cfg, &creds, stream)
	finalBody := step3TransformRequest(provider, translated)

	var bodyValue any
	if err := json.Unmarshal(finalBody, &bodyValue); err != nil {
		return http.StatusInternalServerError, nil, err.Error()
	}
	return http.StatusOK, map[string]any{
		"url": url, "headers": headers, "body": bodyValue,
	}, ""
}

// step3BuildURL mirrors DefaultExecutor.buildUrl (which overrides
// BaseExecutor's for every provider in step-3 scope): compat node
// prefixes resolve against the connection's base URL with Axon's
// fallback bases, Gemini-native providers append
// /{model}:{action}, a registry URL carrying {accountId} is
// substituted from providerSpecificData (missing → Axon's
// CONFIG_ERROR, surfaced by the route's catch as a 500), and every
// other provider's registry URL is already the full endpoint.
func step3BuildURL(provider string, cfg providers.ProviderConfig, model string, stream bool, creds *step3Credentials) (string, error) {
	if strings.HasPrefix(provider, "openai-compatible-") {
		base := creds.connectionBaseURL()
		if base == "" {
			base = "https://api.openai.com/v1" // OPENAI_COMPAT_BASE
		}
		// Axon appends /responses instead when the node's apiType
		// resolves to responses; Go's registry carries no per-node
		// apiType, so the chat-completions suffix always applies.
		return strings.TrimSuffix(base, "/") + "/chat/completions", nil
	}
	if strings.HasPrefix(provider, "anthropic-compatible-") {
		base := creds.connectionBaseURL()
		if base == "" {
			base = "https://api.anthropic.com/v1" // ANTHROPIC_COMPAT_BASE
		}
		return strings.TrimSuffix(base, "/") + "/messages", nil
	}
	if cfg.IsGeminiNative() {
		action := "generateContent"
		if stream {
			action = "streamGenerateContent?alt=sse"
		}
		return strings.TrimSuffix(cfg.BaseURL, "/") + "/" + model + ":" + action, nil
	}
	url := cfg.BaseURL
	if strings.Contains(url, "{accountId}") {
		accountID := creds.psdString("accountId")
		if accountID == "" {
			return "", fmt.Errorf("%s requires accountId in providerSpecificData", provider)
		}
		url = strings.Replace(url, "{accountId}", accountID, 1)
	}
	return url, nil
}

// step3BuildHeaders mirrors DefaultExecutor.buildHeaders: the
// registry's static headers (config.headers ≡ Go's StaticHeaders)
// land after Content-Type, auth resolves through the descriptor
// the provider's wire format implies — combined descriptors take
// apiKey || accessToken (applyAuth), the anthropic-compatible
// split descriptor takes the apiKey branch before the oauth
// branch — anthropic-version is pinned when the descriptor calls
// for it and no static header already set it, and streaming asks
// for an event stream. Deliberately not ported (recorded in the
// fragment): Axon's Anthropic-Beta selection
// (selectAnthropicBeta) and its header hooks
// (kimi/muse/cline/kilocodeOrg) — Go's registry already bakes the
// beta/UA strings a provider needs into StaticHeaders (e.g. the
// claude entry), and the hook-bearing providers are all
// specialized-executor providers gated above.
func step3BuildHeaders(provider string, cfg providers.ProviderConfig, creds *step3Credentials, stream bool) map[string]string {
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	setAuth := func(header, token string, bearer bool) {
		// Axon skips empty tokens and the synthetic "public"
		// token minted for no-auth providers.
		if token == "" || token == "public" {
			return
		}
		if bearer {
			token = "Bearer " + token
		}
		headers[header] = token
	}
	anthropicVersion := func() {
		if _, ok := headers["anthropic-version"]; !ok {
			headers["anthropic-version"] = "2023-06-01" // ANTHROPIC_API_VERSION
		}
	}
	switch {
	case strings.HasPrefix(provider, "anthropic-compatible-"):
		// Split descriptor {apiKey: x-api-key raw, oauth:
		// Authorization bearer} + anthropicVersion.
		if creds.APIKey != "" {
			setAuth("x-api-key", creds.APIKey, false)
		} else if creds.AccessToken != "" {
			setAuth("Authorization", creds.AccessToken, true)
		}
		anthropicVersion()
	case cfg.AuthHeader == "x-api-key":
		// Combined x-api-key descriptor (Axon format "claude").
		token := creds.APIKey
		if token == "" {
			token = creds.AccessToken
		}
		setAuth("x-api-key", token, false)
		anthropicVersion()
	default:
		// Combined bearer descriptor.
		token := creds.APIKey
		if token == "" {
			token = creds.AccessToken
		}
		setAuth("Authorization", token, true)
	}
	if stream {
		headers["Accept"] = "text/event-stream"
	}
	return headers
}

// step3TransformRequest mirrors DefaultExecutor.transformRequest
// for the part that is provider-generic in Go's data: the
// json_schema → json_object fallback for openai-compatible-*
// nodes (applyJsonSchemaFallback injects the schema as a system
// prompt and downgrades response_format). Not ported, recorded in
// the fragment: Nous attribution tags (fire only for a specific
// proxy host's compat nodes), the dropClientMetadata quirk (Go's
// registry carries no per-provider quirks), stripUnsupportedParams
// (Axon's per-provider parameter tables have no Go counterpart),
// and injectReasoningContent (per-provider reasoning-injection
// rules have no Go counterpart). Every other provider's
// DefaultExecutor transform is the identity, as here.
func step3TransformRequest(provider string, body []byte) []byte {
	if !strings.HasPrefix(provider, "openai-compatible-") {
		return body
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	format, ok := parsed["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		return body
	}
	schema := format["json_schema"]
	if schema == nil {
		schema = format["schema"]
	}
	if schema == nil {
		return body
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return body
	}
	prompt := "Respond in JSON format matching this schema: " + string(schemaJSON)
	messages, _ := parsed["messages"].([]any)
	foundSystem := false
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]any)
		if !ok || msg["role"] != "system" {
			continue
		}
		if content, ok := msg["content"].(string); ok {
			msg["content"] = content + "\n" + prompt
			foundSystem = true
		}
		break
	}
	if !foundSystem {
		parsed["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, messages...)
	}
	parsed["response_format"] = map[string]any{"type": "json_object"}
	out, err := json.Marshal(parsed)
	if err != nil {
		return body
	}
	return out
}
