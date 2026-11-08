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
)

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

	// 3. Hourly Account Rate Limit ("Rate limit reached. You can send your next request in X min Y s.")
	// Upstream locks this specific key across all free models for ~1 hour.
	// This is ACCOUNT-SCOPED (ModelScoped = false) so router doesn't keep hammering dead key with other models.
	if statusCode == http.StatusTooManyRequests &&
		(strings.Contains(lowerError, "rate limit reached") ||
			strings.Contains(lowerError, "you can send your next request in") ||
			strings.Contains(lowerError, "per-minute rate limit")) {

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
