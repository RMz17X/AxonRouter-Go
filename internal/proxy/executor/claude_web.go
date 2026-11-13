package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/log"
	"axonrouter/proxy/internal/proxy"
)

// claude.ai's web chat, implemented from docs/CLAUDE_WEB_PROVIDER_SPEC.md.
// Chroma (or a manual paste) supplies a sessionKey cookie; the gateway
// discovers the organization, opens a throwaway conversation, posts the
// flattened transcript as one prompt, and translates the SSE completion
// stream back to OpenAI chunks. This provider is a distinct deployment
// from the API-key `claude`/`anthropic` providers and is never aliased
// onto them (AGENTS.md section 3.A).

// claudeWebDefaultUA is the User-Agent the spec pins when the
// connection does not carry the browser's own.
const claudeWebDefaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

// claudeWebTurnTimeout bounds one completion turn. Thinking turns run
// long; the stall gate on the response body (ProgressReader) is what
// actually catches a wedged upstream.
const claudeWebTurnTimeout = 300 * time.Second

// claudeWebRoute is the wire translation for one client-facing model.
type claudeWebRoute struct {
	upstreamModel string
	thinking      bool
}

// claudeWebModelMap is the spec's catalog (section 7): the public
// names the registry advertises, mapped to the model IDs claude.ai
// expects in the completion body.
var claudeWebModelMap = map[string]claudeWebRoute{
	"claude-3-7-sonnet":   {upstreamModel: "claude-3-7-sonnet-20250219"},
	"claude-3-7-thinking": {upstreamModel: "claude-3-7-sonnet-20250219", thinking: true},
	"claude-3-5-sonnet":   {upstreamModel: "claude-3-5-sonnet-20241022"},
	"claude-3-5-haiku":    {upstreamModel: "claude-3-5-haiku-20241022"},
	"claude-web":          {upstreamModel: "claude-3-7-sonnet-20250219"},
}

func claudeWebResolveModel(model string) claudeWebRoute {
	if route, ok := claudeWebModelMap[model]; ok {
		return route
	}
	log.Info("claude-web", "unmapped model, defaulting to claude-3-7-sonnet", "model", model)
	return claudeWebModelMap["claude-web"]
}

func claudeWebUUID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// ——— Credential resolution (spec section 2) ———

// claudeWebCred is one connection's resolved identity: the Cookie
// header to send, the session key inside it, and the optional fields
// Chroma syncs alongside (organization, browser UA).
type claudeWebCred struct {
	cookieHeader string
	sessionKey   string
	orgUUID      string
	userAgent    string
}

// claudeWebExtractCookieValue pulls one cookie's value out of whatever
// was pasted: a bare value, a single pair, or a full DevTools blob.
func claudeWebExtractCookieValue(raw, cookieName string) string {
	trimmed := strings.TrimSpace(raw)
	if lower := strings.ToLower(trimmed); strings.HasPrefix(lower, "cookie:") {
		trimmed = strings.TrimSpace(trimmed[len("cookie:"):])
	}
	if trimmed == "" {
		return ""
	}
	for _, part := range strings.Split(trimmed, ";") {
		part = strings.TrimSpace(part)
		if value, ok := strings.CutPrefix(part, cookieName+"="); ok {
			return strings.TrimSpace(value)
		}
	}
	// A bare paste (no pair syntax at all) can only be the primary
	// credential itself — never some other cookie's value.
	if cookieName == "sessionKey" && !strings.Contains(trimmed, "=") && !strings.Contains(trimmed, ";") {
		return trimmed
	}
	return ""
}

// claudeWebResolveCred builds the credential from the connection: the
// pasted apiKey carries the cookie (a full header, a sessionKey pair,
// or the bare key), providerSpecificData carries the spec's extras.
func claudeWebResolveCred(req *Request) (claudeWebCred, error) {
	cred := claudeWebCred{userAgent: claudeWebDefaultUA}
	if v, _ := req.ConnData["userAgent"].(string); strings.TrimSpace(v) != "" {
		cred.userAgent = strings.TrimSpace(v)
	}
	if v, _ := req.ConnData["organizationUuid"].(string); strings.TrimSpace(v) != "" {
		cred.orgUUID = strings.TrimSpace(v)
	}

	sessionKey := claudeWebExtractCookieValue(req.APIKey, "sessionKey")
	if sessionKey == "" {
		if v, _ := req.ConnData["sessionKey"].(string); strings.TrimSpace(v) != "" {
			sessionKey = strings.TrimSpace(v)
		}
	}
	if sessionKey == "" {
		return cred, proxy.UpstreamFailure(http.StatusBadRequest,
			"claude-web needs a sessionKey — paste the sessionKey cookie value from a logged-in claude.ai browser (DevTools → Cookies → sessionKey), or sync the account from Chroma")
	}
	cred.sessionKey = sessionKey

	// A full pasted header is forwarded as-is (it may carry cf_clearance
	// the session needs); otherwise the header is just the session key.
	paste := strings.TrimSpace(req.APIKey)
	if strings.Contains(paste, "sessionKey=") {
		if lower := strings.ToLower(paste); strings.HasPrefix(lower, "cookie:") {
			paste = strings.TrimSpace(paste[len("cookie:"):])
		}
		cred.cookieHeader = paste
	} else {
		cred.cookieHeader = "sessionKey=" + sessionKey
		if cf := claudeWebExtractCookieValue(req.APIKey, "cf_clearance"); cf != "" {
			cred.cookieHeader += "; cf_clearance=" + cf
		}
	}
	return cred, nil
}

// ——— Prompt flattening ———

// claudeWebExtractText renders one message's content as plain text:
// a string body, or the text parts of a content array.
func claudeWebExtractText(msg map[string]any) string {
	switch content := msg["content"].(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			if part == nil {
				continue
			}
			if text, _ := part["text"].(string); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// claudeWebFlattenMessages renders the message list as the one prompt
// claude.ai takes: every turn role-prefixed except the last user turn,
// which stands bare as the live question — the same convention the
// sibling web providers use for their one-shot endpoints.
func claudeWebFlattenMessages(messages []any) string {
	type turn struct{ role, text string }
	extracted := make([]turn, 0, len(messages))
	for _, raw := range messages {
		msg, _ := raw.(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = "user"
		}
		if role == "developer" {
			role = "system"
		}
		text := claudeWebExtractText(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		extracted = append(extracted, turn{role: role, text: text})
	}
	lastUserIdx := -1
	for i := len(extracted) - 1; i >= 0; i-- {
		if extracted[i].role == "user" {
			lastUserIdx = i
			break
		}
	}
	parts := make([]string, 0, len(extracted))
	for i, t := range extracted {
		if i == lastUserIdx {
			parts = append(parts, t.text)
		} else {
			parts = append(parts, t.role+": "+t.text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ForwardClaudeWeb routes a chat completion through claude.ai's web
// client, per docs/CLAUDE_WEB_PROVIDER_SPEC.md.
func ForwardClaudeWeb(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil || body == nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, "claude-web: request body is not a JSON object")
	}
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		return proxy.UpstreamFailure(http.StatusBadRequest, "claude-web: missing or empty messages array")
	}
	prompt := claudeWebFlattenMessages(messages)
	if strings.TrimSpace(prompt) == "" {
		return proxy.UpstreamFailure(http.StatusBadRequest, "claude-web: empty prompt after processing")
	}

	modelName, _ := body["model"].(string)
	route := claudeWebResolveModel(modelName)

	cred, err := claudeWebResolveCred(req)
	if err != nil {
		return err
	}

	doer, err := claudeWebTransportFactory(req)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusServiceUnavailable, err.Error())
	}
	// Snapshot the upstream this turn talks to. The throwaway-
	// conversation cleanup below runs on its own goroutine after the
	// turn returned, so it must not read the package global: the test
	// suite swaps that global at a fake site and restores it in a
	// cleanup, which raced with the goroutine.
	claudeBaseURL := claudeWebBaseURL

	callCtx, cancel := context.WithTimeout(ctx, claudeWebTurnTimeout)
	defer cancel()

	orgUUID := cred.orgUUID
	if orgUUID == "" {
		orgUUID, err = claudeWebDiscoverOrg(callCtx, doer, cred)
		if err != nil {
			return err
		}
	}

	chatUUID, err := claudeWebCreateConversation(callCtx, doer, cred, orgUUID)
	if err != nil {
		return err
	}
	// The conversation is throwaway (the transcript rode in the
	// prompt); remove it once the turn is over, off the client's path.
	defer func() { go claudeWebDeleteConversation(doer, cred, claudeBaseURL, orgUUID, chatUUID) }()

	payload := claudeWebCompletionPayload{
		Prompt:            prompt,
		Timezone:          claudeWebTimezone(),
		Model:             route.upstreamModel,
		ParentMessageUUID: claudeWebNullUUID,
		Attachments:       []any{},
		Files:             []any{},
		RenderingMode:     "messages",
	}
	if route.thinking {
		payload.Thinking = &claudeWebThinking{Type: "enabled", BudgetTokens: 4096}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusInternalServerError, "claude-web: marshal payload: "+err.Error())
	}

	log.Info("claude-web", "query", "model", modelName, "upstream", route.upstreamModel, "chars", len(prompt))

	target := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s/completion",
		claudeBaseURL, orgUUID, chatUUID)
	response, err := doer.do(callCtx, http.MethodPost, target, claudeWebHeaders(cred, claudeWebSSEAccept), encoded)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, "claude-web: connection failed: "+err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	response.Body = log.ProgressReader(callCtx, response.Body)

	if response.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return claudeWebStatusFailure(response.StatusCode, string(snippet))
	}

	responseID := "chatcmpl-cweb-" + claudeWebUUID()[:12]
	if req.IsStream {
		return claudeWebStreamToClient(w, req, response.Body, modelName, responseID)
	}
	return claudeWebCollectToClient(w, req, response.Body, modelName, responseID, prompt)
}
