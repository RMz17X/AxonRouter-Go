package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"axonrouter/proxy/internal/proxy"
)

// The claude.ai REST surface the spec defines: organization discovery,
// conversation create/delete, and the completion call itself. Every
// call rides the impersonating transport from claude_web_client.go and
// the header set the spec pins.

// claudeWebJSONAccept is the Accept header for the REST calls.
const claudeWebJSONAccept = "application/json, text/plain, */*"

// claudeWebSSEAccept is the Accept header the spec pins for completion.
const claudeWebSSEAccept = "text/event-stream, text/plain, */*"

// claudeWebNullUUID is the parent_message_uuid a first turn carries:
// the conversation has no parent message yet.
const claudeWebNullUUID = "00000000-0000-0000-0000-000000000000"

// claudeWebDiscoverOrg resolves the account's organization UUID via
// GET /api/organizations when the connection does not carry one.
func claudeWebDiscoverOrg(ctx context.Context, doer claudeWebDoer, cred claudeWebCred) (string, error) {
	resp, err := doer.do(ctx, http.MethodGet, claudeWebBaseURL+"/api/organizations",
		claudeWebHeaders(cred, claudeWebJSONAccept), nil)
	if err != nil {
		return "", fmt.Errorf("claude-web: organization discovery failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", claudeWebStatusFailure(resp.StatusCode, string(body))
	}
	var orgs []struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(body, &orgs); err != nil || len(orgs) == 0 || orgs[0].UUID == "" {
		return "", fmt.Errorf("claude-web: organization discovery returned no organization")
	}
	return orgs[0].UUID, nil
}

// claudeWebCreateConversation opens a fresh conversation and returns
// its UUID. A new conversation per gateway request keeps the gateway
// stateless: the whole flattened transcript rides in the one prompt,
// exactly the way the sibling web providers treat their one-shot
// endpoints.
func claudeWebCreateConversation(ctx context.Context, doer claudeWebDoer, cred claudeWebCred, orgUUID string) (string, error) {
	chatUUID := claudeWebUUID()
	payload, err := json.Marshal(map[string]any{
		"uuid": chatUUID,
		"name": "",
	})
	if err != nil {
		return "", fmt.Errorf("claude-web: marshal conversation: %w", err)
	}
	target := fmt.Sprintf("%s/api/organizations/%s/chat_conversations", claudeWebBaseURL, orgUUID)
	resp, err := doer.do(ctx, http.MethodPost, target, claudeWebHeaders(cred, claudeWebJSONAccept), payload)
	if err != nil {
		return "", fmt.Errorf("claude-web: create conversation failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", claudeWebStatusFailure(resp.StatusCode, string(body))
	}
	var created struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(body, &created); err == nil && created.UUID != "" {
		return created.UUID, nil
	}
	return chatUUID, nil
}

// claudeWebDeleteConversation removes the throwaway conversation the
// turn created, best-effort: the spec marks cleanup optional, and a
// failure here must never surface to the client.
func claudeWebDeleteConversation(doer claudeWebDoer, cred claudeWebCred, orgUUID, chatUUID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	target := fmt.Sprintf("%s/api/organizations/%s/chat_conversations/%s", claudeWebBaseURL, orgUUID, chatUUID)
	resp, err := doer.do(ctx, http.MethodDelete, target, claudeWebHeaders(cred, claudeWebJSONAccept), nil)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// claudeWebThinking is the extended-thinking block the spec defines
// for the thinking model variant.
type claudeWebThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// claudeWebCompletionPayload is the completion request body: the spec's
// field set plus parent_message_uuid, which the site's own client
// always sends and a first turn sets to the null UUID.
type claudeWebCompletionPayload struct {
	Prompt            string             `json:"prompt"`
	Timezone          string             `json:"timezone"`
	Model             string             `json:"model"`
	ParentMessageUUID string             `json:"parent_message_uuid"`
	Attachments       []any              `json:"attachments"`
	Files             []any              `json:"files"`
	RenderingMode     string             `json:"rendering_mode"`
	Thinking          *claudeWebThinking `json:"thinking,omitempty"`
}

// claudeWebTimezone names the gateway's local zone for the payload's
// timezone field — the field is cosmetic (claude.ai stamps the
// conversation with it), so an unresolvable zone degrades to UTC.
func claudeWebTimezone() string {
	loc := time.Now().Location().String()
	if loc == "" || loc == "Local" {
		return "UTC"
	}
	return loc
}

// claudeWebStatusFailure maps a refused call onto the failure the
// router acts on, with the message the operator can do something
// about. Shared by the discovery, create and completion calls.
func claudeWebStatusFailure(status int, body string) error {
	switch {
	case grokIsCloudflareChallenge(body):
		return proxy.UpstreamFailure(status,
			"claude-web returned a Cloudflare challenge — the cf_clearance in the pasted cookie no longer matches this machine. Re-copy the full cookie header from a logged-in claude.ai browser on this machine.")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return proxy.UpstreamFailure(status,
			"claude-web auth failed — the sessionKey may be expired. Re-paste it from claude.ai DevTools → Cookies → sessionKey.")
	case status == http.StatusTooManyRequests:
		return proxy.UpstreamFailure(status,
			"claude-web rate limited. Wait a moment and retry, or rotate accounts.")
	}
	first := strings.TrimSpace(body)
	if len(first) > 200 {
		first = first[:200]
	}
	return proxy.UpstreamFailure(status, fmt.Sprintf("claude-web returned HTTP %d %s", status, first))
}
