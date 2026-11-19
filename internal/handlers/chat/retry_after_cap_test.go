package chat

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"axonrouter/proxy/internal/handlerutil"
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
			got, ok := handlerutil.ClampClientRetryAfter(tt.raw)
			if tt.want < 0 {
				if ok {
					t.Errorf("handlerutil.ClampClientRetryAfter(%q) = %d, want no usable wait", tt.raw, got)
				}
				return
			}
			if !ok {
				t.Fatalf("handlerutil.ClampClientRetryAfter(%q) reported no usable wait, want %d", tt.raw, tt.want)
			}
			if got != tt.want {
				t.Errorf("handlerutil.ClampClientRetryAfter(%q) = %d, want %d", tt.raw, got, tt.want)
			}
			if got > handlerutil.ClientRetryAfterCapSeconds {
				t.Errorf("handlerutil.ClampClientRetryAfter(%q) = %d, want at most %d", tt.raw, got, handlerutil.ClientRetryAfterCapSeconds)
			}
		})
	}
}

// TestClampClientRetryAfterReadsAnHTTPDate pins that the date form of the
// header is honoured, because an upstream that publishes a reset instant
// rather than a duration is common.
func TestClampClientRetryAfterReadsAnHTTPDate(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	got, ok := handlerutil.ClampClientRetryAfter(future)
	if !ok {
		t.Fatalf("handlerutil.ClampClientRetryAfter(%q) reported no usable wait", future)
	}
	if got < 25 || got > 31 {
		t.Errorf("handlerutil.ClampClientRetryAfter(%q) = %d, want about 30 seconds", future, got)
	}
}

// TestClampedRetryAfterPublishesNothingForABrokenValue pins that a header
// the gateway cannot read is not published at all.
//
// A client that reads a broken value retries on the wrong schedule, which
// is worse than the client falling back to its own backoff.
func TestClampedRetryAfterPublishesNothingForABrokenValue(t *testing.T) {
	if got := handlerutil.ClampedRetryAfter("not-a-wait"); got != "" {
		t.Errorf("handlerutil.ClampedRetryAfter(%q) = %q, want it empty", "not-a-wait", got)
	}
	if got := handlerutil.ClampedRetryAfter(""); got != "" {
		t.Errorf("handlerutil.ClampedRetryAfter(\"\") = %q, want it empty", got)
	}
	if got := handlerutil.ClampedRetryAfter("12"); got != "12" {
		t.Errorf("handlerutil.ClampedRetryAfter(%q) = %q, want %q", "12", got, "12")
	}
}

// TestMaskedErrorPublishesACappedWait pins that the wait a client receives
// from the masked upstream error is inside the ceiling.
//
// This is the path the 2026-11-18 incident travelled: the upstream named a
// 120 s wait, and the client refused to retry because of it.
func TestMaskedErrorPublishesACappedWait(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream failed"}}`)

	masked := MaskClientFacingError(http.StatusBadGateway, body, "120", false)
	secs, err := strconv.Atoi(masked.RetryAfter)
	if err != nil {
		t.Fatalf("Retry-After = %q, want an integer number of seconds: %v", masked.RetryAfter, err)
	}
	if secs > handlerutil.ClientRetryAfterCapSeconds {
		t.Errorf("Retry-After = %d, want at most %d", secs, handlerutil.ClientRetryAfterCapSeconds)
	}
}

// TestMaskedRateLimitKeepsItsShortWait pins that the 429 contract did not
// change: the upstream_unavailable answer still asks the client to come
// back in at most two seconds.
//
// The 429 lane has always published a short wait on purpose — a quota window
// is minutes long, and holding the request on it only delays the failover
// to another account.
func TestMaskedRateLimitKeepsItsShortWait(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limited"}}`)
	masked := MaskClientFacingError(http.StatusTooManyRequests, body, "120", false)

	if masked.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", masked.StatusCode, http.StatusServiceUnavailable)
	}
	if masked.RetryAfter != "2" {
		t.Errorf("Retry-After = %q, want %q", masked.RetryAfter, "2")
	}
}

// TestPassRetryPublishesACappedWait pins the combo path: the wait the pass
// collected is capped before it reaches the client.
//
// The pass collects the earliest wait any candidate asked for, and that wait
// is read from an upstream header — the same unbounded source.
func TestPassRetryPublishesACappedWait(t *testing.T) {
	p := passRetry{}
	p.note(&upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"quota"}}`),
		Header:     http.Header{"Retry-After": []string{"900"}},
	})

	rec := httptest.NewRecorder()
	cw := newCommittedResponseWriter(rec)
	p.writeError(cw, &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"quota"}}`),
		Header:     http.Header{"Retry-After": []string{"900"}},
	})

	raw := rec.Header().Get("Retry-After")
	if raw == "" {
		t.Fatal("Retry-After header missing")
	}
	secs, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("Retry-After = %q, want an integer number of seconds: %v", raw, err)
	}
	if secs > handlerutil.ClientRetryAfterCapSeconds {
		t.Errorf("Retry-After = %d, want at most %d", secs, handlerutil.ClientRetryAfterCapSeconds)
	}
}

// TestPublishedWaitNeverExceedsTheClientCeiling is the cross-path check: no
// client-facing path may publish a wait past the ceiling.
func TestPublishedWaitNeverExceedsTheClientCeiling(t *testing.T) {
	paths := map[string]func() string{
		"masked upstream error": func() string {
			return MaskClientFacingError(http.StatusBadGateway, []byte(`{}`), "600", false).RetryAfter
		},
		"masked html body": func() string {
			return MaskClientFacingError(http.StatusBadGateway, []byte(`<html></html>`), "600", false).RetryAfter
		},
		"combo pass": func() string {
			p := passRetry{}
			rec := httptest.NewRecorder()
			cw := newCommittedResponseWriter(rec)
			p.writeError(cw, &upstreamError{
				StatusCode: http.StatusTooManyRequests,
				Body:       []byte(`{"error":{"message":"quota"}}`),
				Header:     http.Header{"Retry-After": []string{"600"}},
			})
			return rec.Header().Get("Retry-After")
		},
	}
	for name, publish := range paths {
		t.Run(name, func(t *testing.T) {
			raw := publish()
			if raw == "" {
				t.Fatalf("%s published no Retry-After", name)
			}
			secs, err := strconv.Atoi(raw)
			if err != nil {
				t.Fatalf("%s published %q, want an integer number of seconds: %v", name, raw, err)
			}
			if secs > handlerutil.ClientRetryAfterCapSeconds {
				t.Errorf("%s published %ds, want at most %d", name, secs, handlerutil.ClientRetryAfterCapSeconds)
			}
		})
	}
}

// TestGatewayDoesNotInventARetryAfterMsHeader pins that the gateway does not
// re-spell the wait as retry-after-ms.
//
// The millisecond header is read before the standard one by two clients of
// this API, so publishing both would let a value slip past the ceiling the
// standard header was clamped to.
func TestGatewayDoesNotInventARetryAfterMsHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := newCommittedResponseWriter(rec)
	p := passRetry{}
	p.writeError(cw, &upstreamError{
		StatusCode: http.StatusTooManyRequests,
		Body:       []byte(`{"error":{"message":"quota"}}`),
		Header:     http.Header{"Retry-After": []string{"120"}},
	})

	if got := rec.Header().Get("Retry-After-Ms"); got != "" {
		t.Errorf("Retry-After-Ms = %q, want it absent", got)
	}
	if got := rec.Header().Values("Retry-After"); len(got) != 1 {
		t.Errorf("Retry-After published %d times, want exactly one: %v", len(got), got)
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

	if handlerutil.ClientRetryAfterCapSeconds > piAIMaxRetryDelayMs/1000 {
		t.Errorf("handlerutil.ClientRetryAfterCapSeconds = %d, want at most %d", handlerutil.ClientRetryAfterCapSeconds, piAIMaxRetryDelayMs/1000)
	}
	if handlerutil.ClientRetryAfterCapSeconds > openAIMaxRetryAfterSeconds {
		t.Errorf("handlerutil.ClientRetryAfterCapSeconds = %d, want at most %d", handlerutil.ClientRetryAfterCapSeconds, openAIMaxRetryAfterSeconds)
	}
}

// TestTerminalFailurePublishesNoWait pins that the gateway's own terminal
// answer carries no delay.
//
// The gateway does not know when the upstream recovers. A delay it invented
// would only defer the retry the client should make immediately, and the
// default exponential backoff a client applies is the honest schedule.
func TestTerminalFailurePublishesNoWait(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTerminalFailure(rec, errors.New("no active connections for provider: deepseek"))

	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want it absent", got)
	}
	if !strings.Contains(rec.Body.String(), "service_unavailable") {
		t.Errorf("body = %q, want the service_unavailable envelope", rec.Body.String())
	}
}
