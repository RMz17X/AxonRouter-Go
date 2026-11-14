package providers

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// tokenharborNextRequestRegex parses: "Rate limit reached. You can send your next request in 59 min 1 s."
	// or "You can send your next request in 30 s."
	tokenharborNextRequestRegex = regexp.MustCompile(`(?i)you\s+can\s+send\s+your\s+next\s+request\s+in\s+(?:(\d+)\s*(?:min|m))?\s*(?:(\d+)\s*(?:sec|s))?`)
	tokenharborCapacityRegex    = regexp.MustCompile(`(?i)(?:model\s+is\s+at\s+capacity|at\s+capacity\s+for\s+your\s+account|retry\s+in\s+about)`)
	tokenharborRetryAboutRe     = regexp.MustCompile(`(?i)retry\s+in\s+about\s+(\d+)\s*sec`)
	tokenharborEgressIpRegex    = regexp.MustCompile(`(?i)(?:sharing\s+this\s+network\s+address|many\s+free\s+accounts|too\s+many\s+requests\s+from\s+(?:this|your)\s+(?:ip|network))`)
	tokenharborServerBusyRegex  = regexp.MustCompile(`(?i)(?:under\s+heavy\s+load|server_busy|service_busy|slowed\s+down\s+for\s+a\s+few\s+minutes)`)
	tokenharborEmailVerifyRe   = regexp.MustCompile(`(?i)(?:email_verification_required|verify\s+your\s+email\s+address)`)
)

// IsTokenHarborEmailVerificationRequired reports whether the error is the
// "confirm your email address" refusal: the account exists and its key is
// valid, but the vendor never got a confirmation click, so the API refuses
// every call with 403. Nothing the router does can clear it — only the
// operator opening the confirmation link can. The account is dead weight in
// the pool until then: it must leave rotation instead of burning a 403
// attempt on every sweep.
func IsTokenHarborEmailVerificationRequired(text string) bool {
	if text == "" {
		return false
	}
	return tokenharborEmailVerifyRe.MatchString(text)
}

// ParseTokenHarborRetryDuration extracts cooldown duration from TokenHarbor error messages.
// Returns 0, false if not matched.
func ParseTokenHarborRetryDuration(text string) (time.Duration, bool) {
	if text == "" {
		return 0, false
	}
	matches := tokenharborNextRequestRegex.FindStringSubmatch(text)
	if len(matches) > 0 && (matches[1] != "" || matches[2] != "") {
		var totalSecs int64
		if matches[1] != "" {
			if mins, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
				totalSecs += mins * 60
			}
		}
		if matches[2] != "" {
			if secs, err := strconv.ParseInt(matches[2], 10, 64); err == nil {
				totalSecs += secs
			}
		}
		if totalSecs > 0 {
			return time.Duration(totalSecs) * time.Second, true
		}
	}
	return 0, false
}

// TokenHarborAdjustedCooldown applies policy-based padding to TokenHarbor rate limit delays:
// 1. Hourly/quota resets (>= 5 min) get a minimum floor of 70 minutes (4200s).
// 2. Short burst/per-minute resets (< 5 min) get a 5-second buffer (e.g. 32s -> 37s, 1s -> 6s)
//    to prevent boundary collision retries.
func TokenHarborAdjustedCooldown(dur time.Duration) time.Duration {
	const (
		hourlyThreshold = 5 * time.Minute
		hourlyFloor     = 70 * time.Minute
		burstBuffer     = 5 * time.Second
	)
	if dur >= hourlyThreshold {
		if dur < hourlyFloor {
			return hourlyFloor
		}
		return dur + burstBuffer
	}
	if dur > 0 {
		return dur + burstBuffer
	}
	return hourlyFloor
}

// IsTokenHarborEgressIpLimit reports whether the error is bound to the source/proxy address
// rather than an account quota.
func IsTokenHarborEgressIpLimit(text string) bool {
	if text == "" {
		return false
	}
	return tokenharborEgressIpRegex.MatchString(text)
}

// classifyTokenHarbor handles TokenHarbor specific 429 and error classifications.
func classifyTokenHarbor(statusCode int, rawError string, backoffLevel int, provider string) (ErrorClassification, bool) {
	if provider != "tokenharbor" {
		return ErrorClassification{}, false
	}
	lowerError := strings.ToLower(rawError)

	// 1. Egress IP Limit ("sharing this network address")
	// The exit proxy IP is exhausted. It is not an account fault.
	// We return a short cooldown and do NOT lock account permanently.
	if IsTokenHarborEgressIpLimit(lowerError) {
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      5 * 60 * 1000, // 5 minutes proxy/pool cooldown
			NewBackoffLevel: backoffLevel,
			ModelScoped:     false,
		}, true
	}

	// 2. Model Capacity limit ("model is at capacity for your account, retry in about N seconds")
	// This is MODEL-SCOPED: only this model is locked, other models on the account stay active.
	if statusCode == http.StatusTooManyRequests && tokenharborCapacityRegex.MatchString(lowerError) {
		cooldownMs := int64(60 * 1000) // default 60s
		if matches := tokenharborRetryAboutRe.FindStringSubmatch(lowerError); len(matches) > 1 && matches[1] != "" {
			if secs, err := strconv.ParseInt(matches[1], 10, 64); err == nil && secs > 0 {
				cooldownMs = secs * 1000
			}
		}
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      int(cooldownMs),
			NewBackoffLevel: backoffLevel,
			ModelScoped:     true, // LOCK MODEL ONLY!
		}, true
	}

	// 2b. Upstream Server Busy / Heavy Load ("Token Harbor is under heavy load ... code server_busy")
	// The upstream cluster is saturated. Lock account for 5 minutes (so it rests without killing the pool).
	if (statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable) && tokenharborServerBusyRegex.MatchString(lowerError) {
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      5 * 60 * 1000, // 5 minutes cooldown for the account
			NewBackoffLevel: backoffLevel,
			ModelScoped:     false,
		}, true
	}

	// 3. Hourly Account Rate Limit ("Rate limit reached. You can send your next request in X min Y s.")
	// Upstream locks this specific key across all free models for ~1 hour.
	// Can arrive as HTTP 429 or HTTP 200 wrapped into BadGateway by the gateway empty-turn check.
	// This is ACCOUNT-SCOPED (ModelScoped = false) so router doesn't keep hammering dead key with other models.
	if strings.Contains(lowerError, "rate limit reached") ||
		strings.Contains(lowerError, "you can send your next request in") ||
		strings.Contains(lowerError, "per-minute rate limit") {

		cooldown := TokenHarborAdjustedCooldown(0) // default 70 min floor
		if dur, ok := ParseTokenHarborRetryDuration(lowerError); ok && dur > 0 {
			cooldown = TokenHarborAdjustedCooldown(dur)
		}

		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      int(cooldown.Milliseconds()),
			NewBackoffLevel: backoffLevel,
			ModelScoped:     false, // LOCK THIS ACCOUNT/KEY
			Exhausted:       false, // Temporary throttle, not permanently exhausted
		}, true
	}

	// 5. Email Verification Required (code email_verification_required)
	// The key works; the mailbox was never confirmed. The refusal is
	// permanent until a human clicks the vendor's confirmation link, so the
	// account is disabled outright (DeactivateKey -> DisableConnectionWithReason,
	// which clears isActive and writes the stored verdict) instead of being
	// parked behind a cooldown it can never outgrow.
	//
	// Matched on the signature alone, not on a status code: TokenHarbor
	// answers 403 with an empty "content" field, and the gateway's
	// empty-turn check rewrites that into 502 before it reaches the
	// classifier. Keying off 403 alone would let the refusal fall through
	// to the generic 5xx rule and stay alive as a transient cooldown.
	if IsTokenHarborEmailVerificationRequired(lowerError) {
		return ErrorClassification{
			ShouldFallback:  true,
			DeactivateKey:   true, // terminal: the account leaves routing
			CooldownMs:      0,
			NewBackoffLevel: backoffLevel,
			ModelScoped:     false,
			Exhausted:       false,
		}, true
	}

	// 4. Upstream HTML / Cloudflare 500 Error ("This page couldn’t load", "500: this page")
	// TokenHarbor frontend/edge sometimes glitches and serves HTML 500.
	// This is transient, fallback to next connection with short 15s cooldown.
	if statusCode >= 500 || strings.Contains(lowerError, "this page couldn’t load") || strings.Contains(lowerError, "this page couldnt load") {
		return ErrorClassification{
			ShouldFallback:  true,
			CooldownMs:      15 * 1000, // 15 seconds transient cooldown
			NewBackoffLevel: backoffLevel,
			ModelScoped:     false,
			Exhausted:       false,
		}, true
	}

	return ErrorClassification{}, false
}
