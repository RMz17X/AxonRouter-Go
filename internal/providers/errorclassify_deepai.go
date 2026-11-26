package providers

import (
	"net/http"
	"strings"
)

// classifyDeepAIAuth handles DeepAI Code's 401 `auth_required` refusal.
//
// The body is `{"error":{"type":"auth_required","code":"auth_required",
// "message":"Sign in to DeepAI to use DeepAI Code."}}`. The generic auth
// markers say "unauthorized"/"auth failed"/"invalid api key", so this verdict
// used to fall through to a 60s cooldown: the account stayed active, was drawn
// again on the next request, and a whole pool of dead sessions burned ten
// attempts before the client saw a 401. A session that answers auth_required
// needs the operator to sign in again, so deactivate it on the first verdict
// and let the selector rotate to a healthy account.
func classifyDeepAIAuth(statusCode int, lowerError string, provider string) (ErrorClassification, bool) {
	if provider != "deepai" || statusCode != http.StatusUnauthorized {
		return ErrorClassification{}, false
	}
	if !strings.Contains(lowerError, "auth_required") &&
		!strings.Contains(lowerError, "auth required") &&
		!strings.Contains(lowerError, "sign in to deepai") {
		return ErrorClassification{}, false
	}
	return ErrorClassification{
		ShouldFallback: true,
		DeactivateKey:  true,
	}, true
}
