package handlerutil

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ClientRetryAfterCapSeconds bounds the wait the gateway publishes in a
// Retry-After header.
//
// The cap is a client-contract limit, not a router policy. Every client of
// this API reads the header and obeys it, and each one has its own ceiling
// above which it stops retrying instead of retrying later:
//
//   - the pi-ai provider retry helper (DEFAULT_MAX_RETRY_DELAY_MS) throws
//     when the server asks for more than 60 s;
//   - the OpenAI Python SDK (MAX_RETRY_AFTER_DELAY) treats a longer value as
//     a reason not to retry at all;
//   - oh-my-pi fails fast past its configured retry.maxDelayMs.
//
// A relay that forwards an upstream's raw value therefore decides whether
// the client retries at all. The 2026-11-18 incident on axon.hijitoko.com
// is the shape of that failure: a 120 s wait turned a retryable status into
// a terminal one, and the client reported "Provider requested 120000ms
// wait, exceeds retry.maxDelayMs (90000ms)".
//
// 60 s is the smallest of those ceilings, so a value inside the cap is
// honoured by every client of this API. Past the cap the gateway publishes
// the cap itself: a shorter wait than the upstream asked for still lets the
// client retry, and the client's own backoff absorbs the difference.
//
// The upstream JavaScript router caps at 90 s, which matched the client
// ceiling of its day. The lower value here is deliberate: the ceiling the
// clients enforce moved down, and a cap above it is a cap the clients
// ignore.
const ClientRetryAfterCapSeconds = 60

// ClampClientRetryAfter parses a Retry-After value and returns the number of
// seconds the gateway may publish for it.
//
// The value is read the way RFC 9110 allows: delta-seconds, or an HTTP-date.
// A value that names no usable wait — absent, negative, malformed, or a date
// already in the past — reports no usable wait, because a client that reads
// a broken header retries on the wrong schedule.
//
// The result never exceeds ClientRetryAfterCapSeconds.
func ClampClientRetryAfter(raw string) (int, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return 0, false
	}
	if secs, ok := parseRetryAfterSeconds(v); ok {
		return secs, true
	}
	if t, err := http.ParseTime(v); err == nil {
		wait := time.Until(t)
		if wait <= 0 {
			return 0, false
		}
		if wait >= ClientRetryAfterCapSeconds*time.Second {
			return ClientRetryAfterCapSeconds, true
		}
		return ceilSeconds(wait), true
	}
	return 0, false
}

// parseRetryAfterSeconds reads a delta-seconds value.
//
// A float is accepted because two clients parse the header that way (the
// OpenAI SDK and the pi-ai retry helper both call parseFloat on it), and the
// result is rounded up so the published wait is never shorter than the one
// the upstream asked for.
//
// An out-of-range magnitude is capped rather than rejected: a header that
// names a wait of ten years is still a header that names a wait, and the
// client has to be given something it can act on.
func parseRetryAfterSeconds(v string) (int, bool) {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if f < 0 {
		return 0, false
	}
	if f >= float64(ClientRetryAfterCapSeconds) {
		return ClientRetryAfterCapSeconds, true
	}
	return int(math.Ceil(f)), true
}

// ClampedRetryAfter returns the Retry-After value to publish to the client,
// capped at the client ceiling, or "" when the upstream named no usable wait.
//
// It is the single place where a relayed wait becomes a published one, so
// every client-facing path clamps the same way.
func ClampedRetryAfter(raw string) string {
	secs, ok := ClampClientRetryAfter(raw)
	if !ok {
		return ""
	}
	return strconv.Itoa(secs)
}

// ClampRetryAfterSeconds bounds an already-computed number of seconds by the
// client ceiling.
//
// It is the form for a wait the gateway computed itself — a rate-limit
// window, a concurrency queue timeout — rather than one read off an upstream
// header. Those waits can exceed the ceiling on their own: the gateway's
// sliding window is 60 s and the queue timeout is 60 s, and each publishes
// its bound plus a second, which is one second past what the clients accept.
func ClampRetryAfterSeconds(secs int) int {
	if secs <= 0 {
		return secs
	}
	if secs > ClientRetryAfterCapSeconds {
		return ClientRetryAfterCapSeconds
	}
	return secs
}

// ceilSeconds rounds a duration up to whole seconds.
func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
