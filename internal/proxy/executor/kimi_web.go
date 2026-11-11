package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/proxy"
)

// kimi.moonshot.cn's web chat, implemented from
// docs/KIMI_WEB_PROVIDER_SPEC.md. Chroma (or a manual paste) supplies
// the refresh_token a browser login leaves in Local Storage; the
// executor exchanges it for short-lived access tokens, opens a
// throwaway chat per request, posts the message list, and translates
// the SSE completion stream back to OpenAI chunks. This provider is a
// distinct deployment from the API-key `kimi` and `kimi-coding`
// providers and is never aliased onto them (AGENTS.md section 3.A).

// kimiWebDefaultUA is the User-Agent the spec pins when the
// connection does not carry the browser's own.
const kimiWebDefaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

// kimiWebTurnTimeout bounds one completion turn; the stall gate on
// the response body (ProgressReader) catches a wedged upstream.
const kimiWebTurnTimeout = 300 * time.Second

// kimiWebRoute is the wire translation for one client-facing model.
type kimiWebRoute struct {
	upstreamModel string
	useSearch     bool
}

// kimiWebModelMap maps the public names the registry advertises to
// the upstream model id and the search flag the completion call
// carries. The upstream ids are the ones kimi.moonshot.cn actually
// accepts — proven live 2026-11-11 against real connections: the
// completion endpoint rejects anything outside [kimi k1 k1.5 k2
// k1.5-thinking] with a 422 validation error, so the spec section 7
// names (kimi-latest, kimi-search, kimi-k2.5) are client-facing only
// and must translate here.
var kimiWebModelMap = map[string]kimiWebRoute{
	"kimi-latest":        {upstreamModel: "k2"},
	"kimi-search":        {upstreamModel: "kimi", useSearch: true},
	"kimi-k2.5":          {upstreamModel: "k2"},
	"kimi-k2":            {upstreamModel: "k2"},
	"kimi-k1.5":          {upstreamModel: "k1.5"},
	"kimi-k1.5-thinking": {upstreamModel: "k1.5-thinking"},
	"kimi-k1":            {upstreamModel: "k1"},
	"kimi":               {upstreamModel: "kimi"},
	"kimi-web":           {upstreamModel: "k2"},
}

func kimiWebResolveModel(model string) kimiWebRoute {
	if route, ok := kimiWebModelMap[model]; ok {
		return route
	}
	log.Info("kimi-web", "unmapped model, defaulting to kimi", "model", model)
	return kimiWebModelMap["kimi-web"]
}

// ——— Credential resolution (spec section 2) ———

// kimiWebCred is one connection's resolved identity: the long-lived
// refresh token, any access token the sync stored alongside it, and
// the browser UA the tokens were issued to.
type kimiWebCred struct {
	refreshToken   string
	accessToken    string
	tokenExpiresAt int64
	userAgent      string
}

// kimiWebRefreshTokenFromPaste pulls the refresh token out of what
// was pasted: the bare token, a refresh_token= pair, or the JSON
// object Chroma's storage carries.
func kimiWebRefreshTokenFromPaste(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			for _, key := range []string{"refreshToken", "refresh_token"} {
				if v, _ := obj[key].(string); strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	for _, part := range strings.Split(trimmed, ";") {
		part = strings.TrimSpace(part)
		for _, name := range []string{"refresh_token=", "refreshToken="} {
			if value, ok := strings.CutPrefix(part, name); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	if !strings.Contains(trimmed, "=") && !strings.Contains(trimmed, ";") {
		return trimmed // a bare token paste
	}
	return ""
}

// kimiWebResolveCred builds the credential from the connection: the
// pasted apiKey carries the refresh token; providerSpecificData
// carries the spec's stored fields.
func kimiWebResolveCred(req *Request) (kimiWebCred, error) {
	cred := kimiWebCred{userAgent: kimiWebDefaultUA}
	if v, _ := req.ConnData["userAgent"].(string); strings.TrimSpace(v) != "" {
		cred.userAgent = strings.TrimSpace(v)
	}
	if v, _ := req.ConnData["accessToken"].(string); strings.TrimSpace(v) != "" {
		cred.accessToken = strings.TrimSpace(v)
	}
	switch exp := req.ConnData["tokenExpiresAt"].(type) {
	case float64:
		cred.tokenExpiresAt = int64(exp)
	case int64:
		cred.tokenExpiresAt = exp
	}

	cred.refreshToken = kimiWebRefreshTokenFromPaste(req.APIKey)
	if cred.refreshToken == "" {
		if v, _ := req.ConnData["refreshToken"].(string); strings.TrimSpace(v) != "" {
			cred.refreshToken = strings.TrimSpace(v)
		}
	}
	if cred.refreshToken == "" {
		return cred, proxy.UpstreamFailure(http.StatusBadRequest,
			"kimi-web needs a refresh_token — paste the refresh_token value from kimi.moonshot.cn Local Storage, or sync the account from Chroma")
	}
	return cred, nil
}

// ——— Message building ———

// kimiWebBuildMessages renders the request's message list in the
// shape the completion call takes (spec section 5.3): user/assistant
// turns as-is, system/developer content folded into the first user
// turn (the web client has no system channel), tool results folded
// in as user text.
func kimiWebBuildMessages(messages []any) []map[string]any {
	var systemParts []string
	out := make([]map[string]any, 0, len(messages))
	firstUserIdx := -1
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		text := claudeWebExtractText(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch role {
		case "system", "developer":
			systemParts = append(systemParts, text)
		case "assistant":
			out = append(out, map[string]any{"role": "assistant", "content": text})
		default: // user, tool, anything else becomes a user turn
			if firstUserIdx < 0 {
				firstUserIdx = len(out)
			}
			out = append(out, map[string]any{"role": "user", "content": text})
		}
	}
	if len(systemParts) > 0 {
		systemText := strings.Join(systemParts, "\n\n")
		if firstUserIdx >= 0 {
			content, _ := out[firstUserIdx]["content"].(string)
			out[firstUserIdx]["content"] = systemText + "\n\n" + content
		} else {
			out = append([]map[string]any{{"role": "user", "content": systemText}}, out...)
		}
	}
	return out
}

// ForwardKimiWeb routes a chat completion through kimi.moonshot.cn's
// web client, per docs/KIMI_WEB_PROVIDER_SPEC.md.
func ForwardKimiWeb(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil || body == nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, "kimi-web: request body is not a JSON object")
	}
	rawMessages, _ := body["messages"].([]any)
	if len(rawMessages) == 0 {
		return proxy.UpstreamFailure(http.StatusBadRequest, "kimi-web: missing or empty messages array")
	}
	messages := kimiWebBuildMessages(rawMessages)
	if len(messages) == 0 {
		return proxy.UpstreamFailure(http.StatusBadRequest, "kimi-web: empty messages after processing")
	}

	modelName, _ := body["model"].(string)
	route := kimiWebResolveModel(modelName)

	cred, err := kimiWebResolveCred(req)
	if err != nil {
		return err
	}

	doer, err := kimiWebTransportFactory(req)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusServiceUnavailable, err.Error())
	}

	callCtx, cancel := context.WithTimeout(ctx, kimiWebTurnTimeout)
	defer cancel()

	accessToken, err := kimiWebAccessToken(callCtx, doer, req, cred)
	if err != nil {
		return err
	}

	chatID, err := kimiWebCreateChat(callCtx, doer, cred, accessToken)
	if err != nil {
		return err
	}
	// The chat is throwaway (the message list rode in the completion
	// call); remove it once the turn is over, off the client's path.
	defer func() { go kimiWebDeleteChat(doer, cred, accessToken, chatID) }()

	payload, err := json.Marshal(map[string]any{
		"messages":   messages,
		"refs":       []any{},
		"use_search": route.useSearch,
		"model":      route.upstreamModel,
	})
	if err != nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "kimi-web: marshal payload: "+err.Error())
	}

	log.Info("kimi-web", "query", "model", modelName, "upstream", route.upstreamModel, "search", route.useSearch)

	target := fmt.Sprintf("%s/api/chat/%s/completion/stream", kimiWebBaseURL, chatID)
	response, err := doer.do(callCtx, http.MethodPost, target, kimiWebHeaders(cred, accessToken), payload)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: connection failed: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	response.Body = log.ProgressReader(callCtx, response.Body)

	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return kimiWebStatusFailure(response.StatusCode, string(snippet))
	}

	responseID := "chatcmpl-kweb-" + claudeWebUUID()[:12]
	if req.IsStream {
		return kimiWebStreamToClient(w, req, response.Body, modelName, responseID)
	}
	return kimiWebCollectToClient(w, req, response.Body, modelName, responseID, messages)
}
