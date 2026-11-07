package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"axonrouter/proxy/internal/providers"
)

// CodeBuddy-family token refresh (codebuddy-cn, codebuddy-intl,
// workbuddy).
//
// The family does not speak the standard form-POST refresh grant: the
// refresh token travels in the X-Refresh-Token HEADER, the body is an
// empty JSON object, and the endpoint lives on each brand's own host
// under /v2/plugin/auth/token/refresh (upstream
// open-sse/services/tokenRefresh/providers.js). Registering these
// refreshers from init() also fences off the generic path for the whole
// family: RegisterAll skips any provider that already has a refresher,
// so the env-gated KnownOAuthConfigs entries for these providers can
// never produce a form-POST refresh with the wrong contract.
type codebuddyRefreshTarget struct {
	url       string
	userAgent string
	domain    string
}

var codebuddyRefreshTargets = map[string]codebuddyRefreshTarget{
	"codebuddy-cn": {
		url:       "https://copilot.tencent.com/v2/plugin/auth/token/refresh",
		userAgent: "CLI/2.63.2 CodeBuddy/2.63.2",
		domain:    "copilot.tencent.com",
	},
	"codebuddy-intl": {
		url:       "https://www.codebuddy.ai/v2/plugin/auth/token/refresh",
		userAgent: "IDE/2.63.2 CodeBuddy/2.63.2",
		domain:    "www.codebuddy.ai",
	},
	"workbuddy": {
		url:       "https://www.workbuddy.ai/v2/plugin/auth/token/refresh",
		userAgent: "IDE/2.63.2 WorkBuddy/2.63.2",
		domain:    "www.workbuddy.ai",
	},
}

func init() {
	for provider, target := range codebuddyRefreshTargets {
		t := target
		Register(provider, func(ctx context.Context, p *Params) (*TokenResult, error) {
			return refreshCodebuddy(ctx, p, t)
		})
	}
}

func refreshCodebuddy(ctx context.Context, p *Params, target codebuddyRefreshTarget) (*TokenResult, error) {
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("codebuddy refresh: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", target.userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", target.domain)
	req.Header.Set("X-Refresh-Token", p.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("X-Product", "SaaS")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy refresh: POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("codebuddy refresh: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &providers.OAuthRefreshError{Status: resp.StatusCode, Body: truncateBody(body)}
	}

	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int    `json:"expiresIn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("codebuddy refresh: parse response: %w", err)
	}
	if envelope.Code != 0 || envelope.Data.AccessToken == "" {
		msg := envelope.Msg
		if msg == "" {
			msg = fmt.Sprintf("code %d", envelope.Code)
		}
		return nil, fmt.Errorf("codebuddy refresh: %s", msg)
	}

	// The endpoint rotates the refresh token only sometimes; when it
	// does not, the stored token stays valid and is carried through so
	// the persisted row keeps it (upstream returns new || old).
	refreshToken := envelope.Data.RefreshToken
	if refreshToken == "" {
		refreshToken = p.RefreshToken
	}
	return &TokenResult{
		AccessToken:  envelope.Data.AccessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    envelope.Data.ExpiresIn,
	}, nil
}
