package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"axonrouter/proxy/internal/proxy"
)

// The Kimi Web token lifecycle and chat surface (spec sections 2, 3
// and 5). The connection carries a long-lived refresh token; access
// tokens live ~15–30 minutes, so the executor exchanges the refresh
// token lazily, right before a turn that finds the cached access
// token expired, and keeps the result in a per-connection in-process
// cache. A refresh response may rotate the refresh token, and the
// rotated value replaces the cached one for the next exchange.

// kimiWebTokenState is one connection's current token pair.
type kimiWebTokenState struct {
	accessToken  string
	refreshToken string
	expiresAt    int64 // unix seconds; 0 = no usable access token
}

var (
	kimiWebTokenCache sync.Map // connection key -> kimiWebTokenState
	kimiWebRefreshMu  sync.Mutex
)

// kimiWebTokenKey identifies a connection's cache entry. The refresh
// token itself is the fallback key for calls without a connection id:
// it is unique per account and never leaves this process.
func kimiWebTokenKey(req *Request, cred kimiWebCred) string {
	if req.ConnectionID != "" {
		return req.ConnectionID
	}
	return "rt:" + cred.refreshToken
}

// kimiWebAccessToken returns a live access token for the connection,
// refreshing first when the cached one is missing or inside its last
// 30 seconds.
func kimiWebAccessToken(ctx context.Context, doer kimiWebDoer, req *Request, cred kimiWebCred) (string, error) {
	key := kimiWebTokenKey(req, cred)
	now := time.Now().Unix()

	state, cached := kimiWebTokenCache.Load(key)
	seed, _ := state.(kimiWebTokenState)
	if !cached {
		seed = kimiWebTokenState{
			accessToken:  cred.accessToken,
			refreshToken: cred.refreshToken,
			expiresAt:    cred.tokenExpiresAt,
		}
	}
	if seed.refreshToken == "" {
		seed.refreshToken = cred.refreshToken
	}
	if seed.accessToken != "" && seed.expiresAt > now+30 {
		return seed.accessToken, nil
	}

	// Serialize refreshes: a burst of concurrent turns on one account
	// must exchange the refresh token once, not race rotations.
	kimiWebRefreshMu.Lock()
	defer kimiWebRefreshMu.Unlock()
	if state, ok := kimiWebTokenCache.Load(key); ok {
		if fresh, _ := state.(kimiWebTokenState); fresh.accessToken != "" && fresh.expiresAt > time.Now().Unix()+30 {
			return fresh.accessToken, nil
		} else if fresh.refreshToken != "" {
			seed = fresh
		}
	}

	resp, err := doer.do(ctx, http.MethodGet, kimiWebBaseURL+"/api/auth/token/refresh",
		kimiWebHeaders(cred, seed.refreshToken), nil)
	if err != nil {
		return "", proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: token refresh failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", kimiWebStatusFailure(resp.StatusCode, string(body))
	}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &refreshed); err != nil || refreshed.AccessToken == "" {
		return "", proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: token refresh returned no access_token")
	}
	if refreshed.ExpiresIn <= 0 {
		refreshed.ExpiresIn = 1500 // spec: access tokens live ~15–30 minutes
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = seed.refreshToken
	}
	fresh := kimiWebTokenState{
		accessToken:  refreshed.AccessToken,
		refreshToken: refreshed.RefreshToken,
		expiresAt:    time.Now().Unix() + refreshed.ExpiresIn,
	}
	kimiWebTokenCache.Store(key, fresh)
	return fresh.accessToken, nil
}

// kimiWebCreateChat opens a fresh chat session and returns its id. A
// new chat per gateway request keeps the gateway stateless: the full
// message list rides in the completion call.
func kimiWebCreateChat(ctx context.Context, doer kimiWebDoer, cred kimiWebCred, accessToken string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"name":       "未命名会话",
		"is_example": false,
	})
	if err != nil {
		return "", proxy.UpstreamFailure(http.StatusInternalServerError, "kimi-web: marshal chat: "+err.Error())
	}
	resp, err := doer.do(ctx, http.MethodPost, kimiWebBaseURL+"/api/chat",
		kimiWebHeaders(cred, accessToken), payload)
	if err != nil {
		return "", proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: create chat failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", kimiWebStatusFailure(resp.StatusCode, string(body))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		return "", proxy.UpstreamFailure(http.StatusBadGateway, "kimi-web: create chat returned no id")
	}
	return created.ID, nil
}

// kimiWebDeleteChat removes the throwaway chat the turn created,
// best-effort: a failure here must never surface to the client.
func kimiWebDeleteChat(doer kimiWebDoer, cred kimiWebCred, accessToken, chatID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := doer.do(ctx, http.MethodDelete, kimiWebBaseURL+"/api/chat/"+chatID,
		kimiWebHeaders(cred, accessToken), nil)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// kimiWebStatusFailure maps a refused call onto the failure the
// router acts on, with the message the operator can do something
// about. Shared by the refresh, create and completion calls.
func kimiWebStatusFailure(status int, body string) error {
	switch {
	case grokIsCloudflareChallenge(body):
		return proxy.UpstreamFailure(status,
			"kimi-web returned a Cloudflare challenge — the egress IP is being refused. Route this account through a proxy pool, or wait and retry.")
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return proxy.UpstreamFailure(status,
			"kimi-web auth failed — the refresh_token may be expired or revoked. Re-copy it from kimi.moonshot.cn Local Storage (refresh_token), or re-sync the account from Chroma.")
	case status == http.StatusTooManyRequests:
		return proxy.UpstreamFailure(status,
			"kimi-web rate limited. Wait a moment and retry, or rotate accounts.")
	}
	first := strings.TrimSpace(body)
	if len(first) > 200 {
		first = first[:200]
	}
	return proxy.UpstreamFailure(status, fmt.Sprintf("kimi-web returned HTTP %d %s", status, first))
}
