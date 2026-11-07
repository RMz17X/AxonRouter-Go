package providers

import (
	"net/http"
	"regexp"
)

// CodeBuddy-family gated error verdicts (fragment 977) — the verdicts a
// substring rule cannot express, ported from upstream's two classifier
// layers:
//
//   - open-sse/services/accountFallback.js checkFallbackError: the 11140
//     guard runs BEFORE ERROR_RULES. It is gated on the code literal,
//     not the provider: 11140 is emitted only by codebuddy-cn /
//     codebuddy-intl / workbuddy, so the code itself is the provider
//     check, and a body whose wording is a safety refusal is vetoed out
//     of the guard back to the request-level safety rules.
//   - src/sse/services/auth.js markAccountUnavailable: inside its 429
//     block, three family-gated sentences override the generic verdict —
//     model-busy (model-scoped 5s), throttle wording (model-scoped
//     2min), credit exhaustion (account-wide, exhausted, 7 days).
//
// The extracted error text the chat layer feeds the classifier always
// carries the upstream code as a "(code NNNNN)" suffix
// (extractErrorText), so the code regexes below match both the raw JSON
// body shape upstream matches and the extracted shape Go actually sees.
var (
	// codebuddy11140CodeRe is WB_11140_CODE_RE: a "code" key (quoted or
	// bare) carrying 11140 as a whole number.
	codebuddy11140CodeRe = regexp.MustCompile(`(?i)(?:"code"|code)\s*["']?\s*[:=]?\s*["']?11140\b`)
	// codebuddyRestrictionRe is WB_ACCOUNT_RESTRICTION_RE.
	codebuddyRestrictionRe = regexp.MustCompile(`(?i)restricted due to unusual activity|unusual activity|submit feedback to appeal|has been restricted|account restriction|\brestricted\b`)
	// codebuddySafetyRe is the guard's safety veto.
	codebuddySafetyRe = regexp.MustCompile(`(?i)safety review|content filter`)
	// codebuddyModelBusyRe / codebuddyBare14003Re: "model busy" wording
	// or the bare 14003 token (delimited by non-digits, so 140030 or
	// 114003 never match).
	codebuddyModelBusyRe  = regexp.MustCompile(`(?i)model busy`)
	codebuddyBare14003Re  = regexp.MustCompile(`(^|[^0-9])14003([^0-9]|$)`)
	codebuddyBare14018Re  = regexp.MustCompile(`(^|[^0-9])14018([^0-9]|$)`)
	codebuddyThrottleRe   = regexp.MustCompile(`(?i)too many requests|rate.?limit exceeded|rate limited|usage exceeds frequency limit`)
	codebuddyQuotaWordsRe = regexp.MustCompile(`(?i)quota|credit|exhaust|deplet|balance|payment|billing`)
	codebuddyCreditTextRe = regexp.MustCompile(`(?i)credits exhausted|insufficient credits`)
)

// CodeBuddy-family cooldowns (ms) for the gated verdicts.
const (
	// codebuddyRestrictedCooldownMs is ACCOUNT_RESTRICTED_COOLDOWN_MS.
	codebuddyRestrictedCooldownMs = 2 * 60 * 60 * 1000 // 2 hours
	// codebuddyBare11140CooldownMs is the bare-11140 account cooldown.
	codebuddyBare11140CooldownMs = 5 * 60 * 1000 // 5 minutes
	// codebuddyModelBusyCooldownMs is the flat model-busy sentence: 5s
	// every time, never escalated by backoff.
	codebuddyModelBusyCooldownMs = 5 * 1000
	// codebuddyThrottleCooldownMs is the model-scoped throttle sentence.
	codebuddyThrottleCooldownMs = 2 * 60 * 1000 // 2 minutes
)

// IsCodebuddyFamilyProvider reports whether a canonical provider id is
// one of the three CodeBuddy-family providers the gated 429 verdicts
// (and the combo attempt cap) are scoped to.
func IsCodebuddyFamilyProvider(provider string) bool {
	switch provider {
	case "codebuddy-cn", "codebuddy-intl", "workbuddy":
		return true
	default:
		return false
	}
}

// classifyCodebuddyFamily evaluates the gated CodeBuddy-family verdicts
// ahead of ErrorRules. The second result reports whether a verdict
// fired; when it does not, classification continues with the rule list.
func classifyCodebuddyFamily(statusCode int, lowerError string, backoffLevel int, provider string) (ErrorClassification, bool) {
	if lowerError == "" {
		return ErrorClassification{}, false
	}

	// 11140 (checkFallbackError guard, code-gated): a 403 carrying the
	// code rotates the account. With restriction wording it is a
	// risk-control verdict — 2h, account-wide, marked exhausted so it
	// leaves the rotation until the vendor clears the flag. A bare
	// 11140 (generic "request illegal", quota/throttle blips) is a
	// 5-minute account cooldown and NOT exhausted, so the pool
	// self-heals without a manual reset. A safety wording vetoes the
	// guard entirely: the request-level safety rules decide instead.
	if statusCode == http.StatusForbidden &&
		codebuddy11140CodeRe.MatchString(lowerError) &&
		!codebuddySafetyRe.MatchString(lowerError) {
		if codebuddyRestrictionRe.MatchString(lowerError) {
			return ErrorClassification{
				ShouldFallback:  true,
				CooldownMs:      codebuddyRestrictedCooldownMs,
				NewBackoffLevel: backoffLevel,
				Exhausted:       true,
			}, true
		}
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      codebuddyBare11140CooldownMs,
			NewBackoffLevel: backoffLevel,
		}, true
	}

	if !IsCodebuddyFamilyProvider(provider) || statusCode != http.StatusTooManyRequests {
		return ErrorClassification{}, false
	}

	// The 429 sentences (markAccountUnavailable), in upstream's order:
	// each later check may replace the earlier verdict, exactly like the
	// sequential assignments there. The credit check is deliberately
	// not gated on the busy/throttle outcomes — a body carrying credit
	// wording is a spent pool even when it also says "busy".
	var verdict *ErrorClassification
	busy := codebuddyModelBusyRe.MatchString(lowerError) || codebuddyBare14003Re.MatchString(lowerError)
	if busy {
		// Flat 5s with the backoff level reset: the generic 429 backoff
		// would otherwise escalate 2s → 4s → 8s on repeat hits for the
		// same busy model.
		verdict = &ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      codebuddyModelBusyCooldownMs,
			NewBackoffLevel: 0,
			ModelScoped:     true,
		}
	}
	if !busy && codebuddyThrottleRe.MatchString(lowerError) && !codebuddyQuotaWordsRe.MatchString(lowerError) {
		verdict = &ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      codebuddyThrottleCooldownMs,
			NewBackoffLevel: backoffLevel,
			ModelScoped:     true,
		}
	}
	// 14018 "Credits exhausted": the spending pool is empty for ALL
	// models on the account — account-wide, exhausted, 7 days. Upstream
	// keys on the wording; the bare code token is its constant companion
	// on the wire, so either claims the verdict.
	if codebuddyCreditTextRe.MatchString(lowerError) || codebuddyBare14018Re.MatchString(lowerError) {
		verdict = &ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      cooldownSevenDays,
			NewBackoffLevel: backoffLevel,
			Exhausted:       true,
		}
	}
	if verdict == nil {
		return ErrorClassification{}, false
	}
	return *verdict, true
}
