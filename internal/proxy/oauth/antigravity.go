package oauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"axonrouter/proxy/internal/providers"
)

func init() {
	Register("antigravity", refreshAntigravity)
}

// refreshAntigravity refreshes an Antigravity token through the dual
// OAuth client config that mirrors the real `agy` CLI's RunLogin
// (fragment 970).
//
// The PRIMARY config (providers.KnownOAuthConfigs["antigravity"]) is the
// only client sign-in and code exchange use, and the first one tried
// here. A refresh token is bound to the client that minted it, so once
// the primary slot flips to agy's sign-in client, tokens minted before
// the flip can only be redeemed by the pre-flip client: when — and only
// when — the primary attempt is rejected as a client mismatch (401, or
// 400 invalid_client / invalid_grant), exactly ONE rescue attempt runs
// under providers.AntigravityLegacyOAuthConfig. If the rescue also
// fails, the PRIMARY attempt's error is the one returned: it describes
// the current client, and the caller's grant-dead handling must see the
// same error it would have seen with no rescue configured.
//
// Every other failure (transport, 5xx, quota) returns as-is: none of
// them says anything about which client owns the grant, and a second
// send would only spend another call against the token endpoint.
func refreshAntigravity(ctx context.Context, p *Params) (*TokenResult, error) {
	primary := providers.KnownOAuthConfigs["antigravity"]
	result, err := refreshWithConfig(ctx, p, primary)
	if err == nil {
		return result, nil
	}
	if !isAntigravityClientMismatch(err) {
		return nil, err
	}
	legacy := providers.AntigravityLegacyOAuthConfig
	if legacy.ClientID == "" || legacy.ClientID == primary.ClientID {
		// Both slots name the same client: a rescue would resend the
		// identical grant under the identical client and collect the
		// identical rejection. Skip it.
		return nil, err
	}
	rescue, rescueErr := refreshWithConfig(ctx, p, legacy)
	if rescueErr != nil {
		return nil, err
	}
	return rescue, nil
}

// refreshWithConfig performs one standard refresh_token grant under an
// explicit client config.
func refreshWithConfig(ctx context.Context, p *Params, cfg providers.OAuthClientConfig) (*TokenResult, error) {
	return NewStandardRefresher(cfg.TokenURL, cfg.ClientID, cfg.ClientSecret)(ctx, p)
}

// isAntigravityClientMismatch reports whether a token endpoint rejection
// says the grant does not belong to the client that presented it: 401, or
// 400 whose body names invalid_client / invalid_grant — the shape of
// Google's answer when a refresh token is redeemed under a different
// client than the one that minted it. Deliberately narrower than
// providers.IsRefreshGrantDead: invalid_request and friends are terminal
// for the grant itself, not evidence about client ownership, so they
// never trigger the rescue.
func isAntigravityClientMismatch(err error) bool {
	var refreshErr *providers.OAuthRefreshError
	if !errors.As(err, &refreshErr) {
		return false
	}
	if refreshErr.Status == http.StatusUnauthorized {
		return true
	}
	if refreshErr.Status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(refreshErr.Body)
	return strings.Contains(body, "invalid_client") || strings.Contains(body, "invalid_grant")
}
