package handlerutil

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestClampClientRetryAfterBoundsTheWait pins the ceiling the gateway
// publishes.
//
// The value is not a router policy — it is the clients' contract. Every
// client of this API reads Retry-After and obeys it, and each has its own
// ceiling above which it stops retrying instead of retrying later:
//
//   - the pi-ai provider retry helper throws past 60 s;
//   - the OpenAI Python SDK treats a longer value as a reason not to retry
//     at all;
//   - oh-my-pi fails fast past its configured retry.maxDelayMs.
//
// A relay that forwards the raw value therefore decides whether the client
// retries. The 2026-11-18 incident on axon.hijitoko.com is that failure:
// a 120 s wait turned a retryable status into a terminal one.
func TestClampClientRetryAfterBoundsTheWait(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{name: "a value past the ceiling is capped", raw: "120", want: 60},
		{name: "a value at the ceiling is kept", raw: "60", want: 60},
		{name: "a value under the ceiling is kept", raw: "7", want: 7},
		{name: "a zero wait is kept", raw: "0", want: 0},
		{name: "a float is rounded up", raw: "1.5", want: 2},
		{name: "an absurd value is capped, not rejected", raw: "99999999", want: 60},
		{name: "a negative value names no wait", raw: "-5", want: -1},
		{name: "a malformed value names no wait", raw: "soon", want: -1},
		{name: "an empty value names no wait", raw: "", want: -1},
		{name: "a date in the past names no wait", raw: "Wed, 21 Oct 2015 07:28:00 GMT", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClampClientRetryAfter(tt.raw)
			if tt.want < 0 {
				if ok {
					t.Errorf("ClampClientRetryAfter(%q) = %d, want no usable wait", tt.raw, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ClampClientRetryAfter(%q) reported no usable wait, want %d", tt.raw, tt.want)
			}
			if got != tt.want {
				t.Errorf("ClampClientRetryAfter(%q) = %d, want %d", tt.raw, got, tt.want)
			}
			if got > ClientRetryAfterCapSeconds {
				t.Errorf("ClampClientRetryAfter(%q) = %d, want at most %d", tt.raw, got, ClientRetryAfterCapSeconds)
			}
		})
	}
}

// TestClampClientRetryAfterReadsAnHTTPDate pins that the date form of the
// header is honoured, because an upstream that publishes a reset instant
// rather than a duration is common.
func TestClampClientRetryAfterReadsAnHTTPDate(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	got, ok := ClampClientRetryAfter(future)
	if !ok {
		t.Fatalf("ClampClientRetryAfter(%q) reported no usable wait", future)
	}
	if got < 25 || got > 31 {
		t.Errorf("ClampClientRetryAfter(%q) = %d, want about 30 seconds", future, got)
	}
}

// TestClampedRetryAfterPublishesNothingForABrokenValue pins that a header
// the gateway cannot read is not published at all.
//
// A client that reads a broken value retries on the wrong schedule, which
// is worse than the client falling back to its own backoff.
func TestClampedRetryAfterPublishesNothingForABrokenValue(t *testing.T) {
	if got := ClampedRetryAfter("not-a-wait"); got != "" {
		t.Errorf("ClampedRetryAfter(%q) = %q, want it empty", "not-a-wait", got)
	}
	if got := ClampedRetryAfter(""); got != "" {
		t.Errorf("ClampedRetryAfter(\"\") = %q, want it empty", got)
	}
	if got := ClampedRetryAfter("12"); got != "12" {
		t.Errorf("ClampedRetryAfter(%q) = %q, want %q", "12", got, "12")
	}
}

// TestClampRetryAfterSecondsBoundsTheGatewaysOwnWait pins the form for a wait
// the gateway computed itself.
//
// The gateway's sliding window is 60 s and the concurrency queue timeout is
// 60 s, and each publishes its bound plus a second — one second past what the
// pi-ai retry helper accepts, which turns a retryable refusal into a
// terminal error on the client.
func TestClampRetryAfterSecondsBoundsTheGatewaysOwnWait(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "a wait past the ceiling is capped", in: 61, want: 60},
		{name: "a long wait is capped", in: 1800, want: 60},
		{name: "a wait at the ceiling is kept", in: 60, want: 60},
		{name: "a short wait is kept", in: 5, want: 5},
		{name: "zero is kept", in: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampRetryAfterSeconds(tt.in); got != tt.want {
				t.Errorf("ClampRetryAfterSeconds(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestClientRetryAfterCapIsTheLowestClientCeiling guards the constant itself.
//
// The cap is only safe while it stays at or below every client's own
// ceiling. The smallest of them is the pi-ai retry helper's 60 s default;
// raising the cap above that would hand clients a wait they refuse.
func TestClientRetryAfterCapIsTheLowestClientCeiling(t *testing.T) {
	const piAIMaxRetryDelayMs = 60_000
	const openAIMaxRetryAfterSeconds = 120

	if ClientRetryAfterCapSeconds > piAIMaxRetryDelayMs/1000 {
		t.Errorf("ClientRetryAfterCapSeconds = %d, want at most %d", ClientRetryAfterCapSeconds, piAIMaxRetryDelayMs/1000)
	}
	if ClientRetryAfterCapSeconds > openAIMaxRetryAfterSeconds {
		t.Errorf("ClientRetryAfterCapSeconds = %d, want at most %d", ClientRetryAfterCapSeconds, openAIMaxRetryAfterSeconds)
	}
}

// TestClampedRetryAfterIsAlwaysParseable pins that whatever the gateway
// publishes is a plain integer number of seconds.
//
// A client parses the header as a number first and a date second, so a
// value that is neither keeps the client on its date-parsing path, where an
// unparseable value becomes a negative delay.
func TestClampedRetryAfterIsAlwaysParseable(t *testing.T) {
	for _, raw := range []string{"", "0", "1", "60", "120", "3600", "-1", "soon", "Wed, 21 Oct 2015 07:28:00 GMT"} {
		got := ClampedRetryAfter(raw)
		if got == "" {
			continue
		}
		secs, err := strconv.Atoi(got)
		if err != nil {
			t.Errorf("ClampedRetryAfter(%q) = %q, want an integer number of seconds: %v", raw, got, err)
			continue
		}
		if secs < 0 {
			t.Errorf("ClampedRetryAfter(%q) = %d, want it non-negative", raw, secs)
		}
		if secs > ClientRetryAfterCapSeconds {
			t.Errorf("ClampedRetryAfter(%q) = %d, want at most %d", raw, secs, ClientRetryAfterCapSeconds)
		}
	}
}
