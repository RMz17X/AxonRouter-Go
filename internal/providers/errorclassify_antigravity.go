package providers

import (
	"net/http"
	"strings"
)

// classifyAntigravity holds the provider-scoped verdicts for
// antigravity (Google Code Assist consumer accounts), consulted before
// the generic rule list in ClassifyErrorForProvider.
//
// Two Google verdicts are systematically misread by the generic rules:
//
//   - A 429 whose body is the bare RESOURCE_EXHAUSTED sentence ("Resource
//     has been exhausted (e.g. check quota).") with no quota details is
//     the consumer endpoint's per-account CONCURRENCY cap answering, not
//     quota exhaustion: probed live (2026-11-15) with an account whose
//     weekly quota summary read 100% remaining — single requests passed
//     in every variation while a 12-way parallel burst drew this exact
//     429 on a third of the calls. Real quota verdicts additionally
//     carry explicit markers (QUOTA_EXHAUSTED, "Individual quota
//     reached", RATE_LIMIT_EXCEEDED — the same marker set the quota
//     subsystem trusts); those fall through to the generic rules. The
//     generic "resource_exhausted" text rule escalates the account
//     backoff and parks the whole account, punishing healthy accounts
//     for a momentary overlap, so the bare shape gets the
//     model_concurrency treatment instead: model-scoped, a fixed short
//     cooldown, no backoff escalation.
//   - A 503 "No capacity available for model ..." is Google's model
//     capacity wall (see IsModelCapacityError): the model is saturated
//     for every caller, so the verdict is model-scoped with a fixed
//     cooldown, never an account sentence.
func classifyAntigravity(statusCode int, lowerError string, backoffLevel int, provider string) (ErrorClassification, bool) {
	if provider != "antigravity" {
		return ErrorClassification{}, false
	}
	if statusCode == http.StatusServiceUnavailable && strings.Contains(lowerError, "no capacity available") {
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      60000,
			NewBackoffLevel: backoffLevel,
			ModelScoped:     true,
		}, true
	}
	if statusCode != http.StatusTooManyRequests {
		return ErrorClassification{}, false
	}
	for _, marker := range []string{"quota_exhausted", "individual quota", "rate_limit_exceeded"} {
		if strings.Contains(lowerError, marker) {
			return ErrorClassification{}, false
		}
	}
	if strings.Contains(lowerError, "resource_exhausted") || strings.Contains(lowerError, "resource has been exhausted") {
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      30000,
			NewBackoffLevel: backoffLevel,
			ModelScoped:     true,
		}, true
	}
	return ErrorClassification{}, false
}
