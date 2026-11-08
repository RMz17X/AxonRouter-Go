package chat

import (
	json "encoding/json/v2"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"axonrouter/proxy/internal/constants"
	"axonrouter/proxy/internal/providers"
	"axonrouter/proxy/internal/translator"
)

const (
	minRetryJitterMs = 100
	maxRetryJitterMs = 400
)

// addRetryJitter adds a random jitter between minRetryJitterMs and maxRetryJitterMs
// to a positive duration to prevent thundering-herd collisions on retry.
func addRetryJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	delta := maxRetryJitterMs - minRetryJitterMs
	var jitter int64
	if delta > 0 {
		jitter = minRetryJitterMs + rand.Int64N(int64(delta))
	} else {
		jitter = minRetryJitterMs
	}
	return d + time.Duration(jitter)*time.Millisecond
}

// brief429RetryTolerance is the longest wait a rate-limited upstream may ask
// for and still be worth repeating on the same client turn. It is the router's
// own base backoff (providers.BackoffConfig.BaseMs): at or under it the account
// is very likely free again by the time the next attempt runs, so an ordinary
// burst is absorbed instead of surfacing a 429. Above it the quota window is
// spent, and re-sending to the same account only deepens Google's backoff
// instead of letting the next account serve the turn.
const brief429RetryTolerance = 2 * time.Second

// comboRetryWaitCap bounds how long a fully-failed combo pass will hold the
// request before retrying. Longer upstream Retry-After values are surfaced via
// the Retry-After header instead so the client decides. A 429 is held to the
// stricter brief429RetryTolerance: unlike a transient 5xx, a long Retry-After
// on a 429 names a quota window that waiting out here cannot shorten.
const comboRetryWaitCap = 8 * time.Second

// deepSeekWebMaxMuteCooldown is the ceiling for a DeepSeek web mute the site
// announced. Its mute envelope names a real release time, and the generic 2h
// cap would re-pick the muted account long before the mute ends — which is how
// a brief mute becomes a longer one.
const deepSeekWebMaxMuteCooldown = 7 * 24 * time.Hour

// maxCooldownFor returns the ceiling for an announced wait on one provider.
// Every provider shares maxResetCooldown; deepseek-web is the exception because
// its enforcement payload carries the actual release time and clamping it
// defeats the purpose of reporting it.
func maxCooldownFor(provider string) time.Duration {
	if provider == "deepseek-web" {
		return deepSeekWebMaxMuteCooldown
	}
	if provider == "antigravity" || provider == "ag" {
		return maxResetCooldown
	}
	return maxResetCooldown
}

// comboPassRetryWait returns how long to hold a fully-failed combo pass before
// repeating it, or 0 when the pass must not be repeated.
//
// earliest is the smallest Retry-After any candidate asked for; rateLimited
// reports whether a 429 was among the failures. When it was, only a wait
// inside brief429RetryTolerance earns another pass — a longer one means every
// candidate in the pool is out of quota, and retrying is what kept the same
// limited accounts absorbing the client's retries.
func comboPassRetryWait(earliest time.Duration, rateLimited bool) time.Duration {
	limit := comboRetryWaitCap
	if rateLimited {
		limit = brief429RetryTolerance
	}
	if earliest <= 0 || earliest > limit {
		return 0
	}
	return earliest
}

// M1 — the 404/ToS cooldown semantics live here, in one place.
//
// Origin commit fb584e06 gave the classifier a banned-account class
// (providers.ErrorClassification.BannedAccount, 24h) with an account-level
// intent, but two clamps in this package silently defeated it: every
// cooldown was capped at the generic 2h reset ceiling, and every 404 was
// treated as model-scoped, so the account lock never fired. The reconciled
// semantics (owner decision, review cycle 1):
//
//   - Duration: a banned-account classification is a sentence, not a
//     backoff. cooldownSecForClassification returns its 24h UNCLAMPED —
//     neither the maxCooldownFor ceiling nor a quota-reset reading in the
//     body shortens it. Every other class keeps the clamped
//     retryableCooldownSec behaviour below, unchanged.
//   - The exhausted class (fragment 977) is a sentence of the same
//     kind: a spent budget does not refill inside the 72h reset ceiling
//     (the CodeBuddy 7-day and generic 30-day verdicts must survive
//     whole), and a Retry-After header on the same 429 names the
//     throttle window, not the budget's return. It rides the banned
//     class's unclamped path.
//   - A model-scoped verdict (fragment 977) carries its exact duration
//     for the same reason in miniature: the classifier's flat 5s
//     model-busy sentence must not be replaced by a Retry-After/reset
//     reading meant for account throttles.
//   - Scope: a plain model_not_found 404 stays model-scoped (the model is
//     gone, the account is not). "requested entity was not found" is
//     carved out in isModelScopedError: on Gemini/Antigravity it names
//     the account's project/entity, so it takes the account-level
//     LockConnectionRateLimit path like any other account death.
//
// cooldownSecForClassification is the single entry point both failover
// loops (fallback.go, combo.go) use to turn a classification into parked
// seconds, so the two cannot drift back into disagreeing halves.
func cooldownSecForClassification(statusCode int, cls providers.ErrorClassification, ue *upstreamError, provider string) int {
	if cls.BannedAccount || cls.Exhausted || statusCode == http.StatusNotFound {
		return ceilSeconds(time.Duration(cls.CooldownMs) * time.Millisecond)
	}
	if cls.ModelScoped {
		return ceilSeconds(time.Duration(cls.CooldownMs) * time.Millisecond)
	}
	if provider == "tokenharbor" && cls.CooldownMs > 0 {
		return ceilSeconds(time.Duration(cls.CooldownMs) * time.Millisecond)
	}
	return retryableCooldownSec(statusCode, time.Duration(cls.CooldownMs)*time.Millisecond, ue, provider)
}

// retryableCooldownSec returns how long a failed account must stay out of
// rotation, in seconds, given the classifier's own backoff as the baseline.
//
// A 429 names the window the account is out for, in either direction: honour a
// long one and the next request no longer re-picks an exhausted account (the
// old code locked it for the classifier's 2s base, so the same limited account
// came back two seconds later and absorbed another failing request), and
// honour a short one and the bounded second pass below actually finds the
// account again instead of waiting out a lock it set itself. Every other
// status keeps the quota-reset reading it has always had. maxResetCooldown
// stops a hostile or nonsensical duration from parking an account forever.
//
// Exception: 503. A 503 is a capacity/node verdict ("no capacity available
// for model", UNAVAILABLE) whose own timescale is seconds to minutes — the
// classifier's Backoff class (2s→5min). Any reset duration found in its
// body describes a different window (a quota reset), and borrowing it
// parked accounts for hours on a capacity signal: the JS router benched a
// model fleet-wide for ~2h that way on 2026-11-07 (fixed there in
// v0.1.34 by clamping the capacity writer to its short window). The Go
// equivalent is to keep 503 on the classifier backoff and never consult
// the body for a duration.
func retryableCooldownSec(statusCode int, base time.Duration, ue *upstreamError, provider string) int {
	cooldown := base
	if statusCode == http.StatusTooManyRequests {
		if wait, ok := retryAfterWait(ue); ok {
			cooldown = wait
		}
	} else if statusCode != http.StatusServiceUnavailable {
		if dur, ok := extractResetDuration(ue.Body); ok {
			cooldown = dur
		}
	}
	return ceilSeconds(min(cooldown, maxCooldownFor(provider)))
}

// passRetry is the rate-limit bookkeeping for one combo pass. The OpenAI and
// Claude combo loops need the same three answers — how long to hold before
// repeating the pass, what to tell the client, and whether a 429 was among the
// failures — and keeping them together stops the two endpoints from drifting
// apart on a contract only one of them would enforce.
type passRetry struct {
	earliest    time.Duration
	rateLimited bool
}

// note folds an upstream failure into the pass-wide wait.
func (p *passRetry) note(ue *upstreamError) {
	if ue.StatusCode == http.StatusTooManyRequests {
		p.rateLimited = true
	}
	if wait, ok := retryAfterWait(ue); ok && (p.earliest == 0 || wait < p.earliest) {
		p.earliest = wait
	}
}

// reset starts a fresh pass: the previous one's exclusions and waits do not
// carry over.
func (p *passRetry) reset() { *p = passRetry{} }

// wait returns how long to hold before repeating the pass (with bounded random jitter),
// or 0 when the pass must not be repeated.
func (p *passRetry) wait() time.Duration {
	w := comboPassRetryWait(p.earliest, p.rateLimited)
	if w <= 0 {
		return 0
	}
	return addRetryJitter(w)
}

// writeError answers the client with lastErr, publishing the earliest
// Retry-After the pass collected three ways: the Retry-After header, the
// human-readable suffix on the error message, and the reset_at / retry_after
// pair in the JSON error object. The body fields exist because a client that
// never surfaces headers — a browser SDK, a log-only integration — has no
// other way to learn when the combo becomes usable again.
//
// Without a usable reset the upstream body is forwarded untouched: an invented
// reset instant would send clients into a retry loop against a quota with no
// known end.
func (p *passRetry) writeError(cw *committedResponseWriter, lastErr *upstreamError) {
	// A client that asked for a stream must be answered with one. The body is
	// the same JSON either way; what changes is the envelope. Writing the bare
	// document under a text/event-stream Content-Type leaves the client with no
	// frame separator it can split on, and a strict SSE reader reports the
	// first backslash it meets as `Unrecognized token '\'` — the status, type
	// and code never reach it.
	if cw.stream {
		cw.Header().Set(constants.HeaderContentType, constants.ContentTypeEventStream)
		if cw.responsesClient {
			var errBody struct {
				Error map[string]any `json:"error"`
			}
			if err := json.Unmarshal(lastErr.Body, &errBody); err == nil && errBody.Error != nil {
				if code, _ := errBody.Error["code"].(string); code == "context_length_exceeded" {
					cw.WriteHeader(http.StatusOK)
					_, _ = cw.Write([]byte(translator.FormatResponsesFailedSSE(errBody.Error)))
					if f, ok := cw.ResponseWriter.(http.Flusher); ok {
						f.Flush()
					}
					return
				}
			}
		}
		cw.WriteHeader(lastErr.StatusCode)
		frame := append([]byte("data: "), lastErr.Body...)
		frame = append(frame, '\n', '\n')
		_, _ = cw.Write(frame)
		_, _ = cw.Write([]byte("data: [DONE]\n\n"))
		if f, ok := cw.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	// Set before any branch so the pass-through path below is a JSON body
	// too. The upstream content type is not trusted here: a 429 served as
	// text/html is what makes an SDK report a parse crash instead of a quota.
	cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	if p.earliest > 0 {
		sec := max(ceilSeconds(p.earliest), 1)
		cw.Header().Set("Retry-After", strconv.Itoa(sec))
		retryAt := time.Now().UTC().Add(p.earliest)
		retryHuman := formatRetryAfter(retryAt.Format(time.RFC3339))
		var errBody map[string]any
		if err := json.Unmarshal(lastErr.Body, &errBody); err == nil {
			if errObj, ok := errBody["error"].(map[string]any); ok {
				if msg, _ := errObj["message"].(string); msg != "" {
					errObj["message"] = msg + " (" + retryHuman + ")"
					// The instant has to travel as data, not only as prose:
					// only a human reads "reset after 2m 30s", and a client
					// that never surfaces the header has nothing else left.
					errObj["reset_at"] = retryAt.Format(time.RFC3339)
					errObj["retry_after"] = sec
					updated, _ := json.Marshal(errBody)
					cw.WriteHeader(lastErr.StatusCode)
					cw.Write(updated)
					return
				}
			}
		}
	}
	cw.WriteHeader(lastErr.StatusCode)
	cw.Write(lastErr.Body)
}

// retryAfterWait reports how long the upstream asked the client to wait before
// re-sending. It reads both carriers a rate limit uses — the Retry-After
// response header and the google.rpc.RetryInfo payload in the JSON body — plus
// the quota-reset forms extractRetryAfter already knows. The longest value wins,
// because a wait is only over when every source's window has passed.
func retryAfterWait(ue *upstreamError) (time.Duration, bool) {
	if ue == nil {
		return 0, false
	}
	var longest time.Duration
	found := false
	note := func(d time.Duration, ok bool) {
		if ok && d > longest {
			longest, found = d, true
		}
	}
	note(parseRetryAfterHeader(ue.Header.Get("Retry-After")))
	note(retryInfoDelay(ue.Body))
	// extractRetryAfter already resolves quotaResetDelay, "Resets in X" and the
	// retryAfter/retry_after/resetsAt/resets_at fields; re-reading its string
	// keeps one owner of the field names.
	note(parseRetryAfterValue(extractRetryAfter(ue.Body)))
	return longest, found
}

// retryInfoDelay reads the google.rpc.RetryInfo detail a rate-limited Gemini
// request carries. retryDelay sits on the detail itself, not inside ErrorInfo
// metadata, so extractResetDuration (which only looks for quotaResetDelay there)
// never sees it — and Google's most common carrier of the wait was therefore
// invisible to the whole cooldown/failover path.
func retryInfoDelay(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}
	var rpcErr struct {
		Error struct {
			Details []struct {
				Type        string `json:"@type"`
				RetryDelay  string `json:"retryDelay"`
				MetadataMap struct {
					RetryDelay string `json:"retryDelay"`
				} `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcErr); err != nil {
		return 0, false
	}
	for _, d := range rpcErr.Error.Details {
		if !strings.Contains(d.Type, "RetryInfo") && d.RetryDelay == "" && d.MetadataMap.RetryDelay == "" {
			continue
		}
		if dur, ok := parseDurationString(d.RetryDelay); ok {
			return dur, true
		}
		if dur, ok := parseDurationString(d.MetadataMap.RetryDelay); ok {
			return dur, true
		}
	}
	return 0, false
}

// parseRetryAfterHeader reads an RFC 9110 Retry-After value: delta-seconds or
// an HTTP-date. Anything else — an empty header, a bare word, a date already in
// the past — carries no usable wait.
func parseRetryAfterHeader(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
	}
	return 0, false
}

// parseRetryAfterValue reads a Retry-After carried in a JSON body, which
// upstreams spell as an RFC3339 timestamp, a Go duration ("120s") or bare
// seconds ("120").
func parseRetryAfterValue(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, false
	}
	return parseDurationString(v)
}

func parseDurationString(v string) (time.Duration, bool) {
	v = strings.TrimRight(strings.TrimSpace(v), ".")
	if v == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d, d > 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second, secs > 0
	}
	return 0, false
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
