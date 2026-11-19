package providers

import (
	"net/http"
	"strings"
)

// minimaxCodeCreditExhaustedCooldownMs parks a credit-exhausted MiniMax Code
// account for 2 days (48h) so it stops absorbing requests until daily check-in
// claims or a top-up replenish it (which auto-releases the lock earlier).
const minimaxCodeCreditExhaustedCooldownMs = 48 * 60 * 60 * 1000 // 2 days

var minimaxCodeAuthFailureMarkers = []string{
	"auth failed",
	"unauthorized",
	"invalid_grant",
	"token revoked",
	"refresh_token_reused",
}

var minimaxCodeCreditExhaustedMarkers = []string{
	"usage limit reached",
	"余额不足",
	"积分不足",
	"额度不足",
	"额度已用完",
	"额度已用尽",
	"insufficient balance",
	"insufficient credit",
	"insufficient quota",
	"insufficient_quota",
	"credits exhausted",
	"credit exhausted",
	"out of credits",
	"quota exhausted",
	"quota exceeded",
	"no remaining credit",
}

// classifyMinimaxCode holds the provider-scoped verdicts for minimax-code and
// minimax-code-global (aliases mmc / mmg), consulted before the generic rule
// list in ClassifyErrorForProvider.
//
// Two verdicts need provider-scoped handling:
//
//   - Dead auth (HTTP 401): MiniMax's mavis gateway answers a dead or revoked
//     bearer token with HTTP 401 `{"code":401,"message":"auth failed"}`. By
//     the time an error reaches ClassifyErrorForProvider,
//     tryForwardWithConnection has already attempted a reactive
//     forceRefreshOAuthToken + single retry; a 401 that still arrives here
//     cannot be revived by waiting two minutes, so the connection is
//     deactivated outright.
//   - Credit exhaustion (HTTP 429/402/403): ForwardMinimaxCode normalizes
//     402/403 credit refusals into HTTP 429 `"usage limit reached: <msg>"`.
//     Short CJK messages (e.g. 余额不足 / 积分不足) would otherwise fall through
//     to the generic 2s→5m 429 backoff; parking them for 1 hour (Exhausted)
//     stops thrashing the spent account while letting daily check-in claims or
//     top-ups recover it automatically.
func classifyMinimaxCode(statusCode int, lowerError string, backoffLevel int, provider string) (ErrorClassification, bool) {
	if provider != "minimax-code" && provider != "minimax-code-global" {
		return ErrorClassification{}, false
	}
	if statusCode == http.StatusUnauthorized {
		for _, marker := range minimaxCodeAuthFailureMarkers {
			if strings.Contains(lowerError, marker) {
				return ErrorClassification{
					ShouldFallback:  true,
					DeactivateKey:   true,
					CooldownMs:      0,
					NewBackoffLevel: backoffLevel,
				}, true
			}
		}
		return ErrorClassification{}, false
	}
	if statusCode == http.StatusTooManyRequests || statusCode == http.StatusPaymentRequired || statusCode == http.StatusForbidden {
		for _, marker := range minimaxCodeCreditExhaustedMarkers {
			if strings.Contains(lowerError, marker) {
				return ErrorClassification{
					ShouldFallback:  true,
					Exhausted:       true,
					CooldownMs:      minimaxCodeCreditExhaustedCooldownMs,
					NewBackoffLevel: backoffLevel,
				}, true
			}
		}
	}
	return ErrorClassification{}, false
}
