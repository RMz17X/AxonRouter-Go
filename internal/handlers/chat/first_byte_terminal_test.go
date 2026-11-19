package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	internalproxy "axonrouter/proxy/internal/proxy"
)

// TestFirstByteTimeoutProducesGatewayTimeout pins the client-facing status
// for a chain that ran out of time before any upstream answered.
//
// This is the status the 2026-11-18 incident did not have. The edge closed
// the connection at its own timeout and the client received 524, a status
// that names the proxy in front of the gateway rather than anything the
// client can act on. The gateway has to answer first, and the answer has to
// be its own.
//
// 524 is not a status any client of this API can retry meaningfully: it
// names a timeout on a hop the client cannot see. 504 is the gateway's own
// timeout, and it carries the OpenAI error envelope the clients parse.
func TestFirstByteTimeoutProducesGatewayTimeout(t *testing.T) {
	err := firstByteTimeoutUpstreamError(42 * time.Second)

	rec := httptest.NewRecorder()
	writeTerminalFailure(rec, err)

	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}

	if got := rec.Header().Get("X-Axon-First-Byte-Wait-Ms"); got != "42000" {
		t.Errorf("X-Axon-First-Byte-Wait-Ms = %q, want %q", got, "42000")
	}

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", ct)
	}

	// The clients of this API parse the error envelope, so the body has to
	// be the envelope and not a bare string.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body is not the error envelope: %v (body %q)", err, rec.Body.String())
	}
	if envelope.Error.Type != "timeout_error" {
		t.Errorf("error type = %q, want %q", envelope.Error.Type, "timeout_error")
	}
	if envelope.Error.Code != http.StatusGatewayTimeout {
		t.Errorf("error code = %d, want %d", envelope.Error.Code, http.StatusGatewayTimeout)
	}
}

// TestNonTimeoutTerminalFailureIsServiceUnavailable pins that a terminal
// failure the gateway could not explain is answered as a transient
// unavailability.
//
// The status used to be 502, which says "the upstream answered and the
// answer was bad". Nothing answered on this path: the gateway had no
// connection to use, the provider was unhealthy, or the combo was
// exhausted. A client that reads 503 retries, which is the outcome the
// gateway wants — the next attempt can land on a different account — while
// 502 leaves the client with a failure it cannot classify.
func TestNonTimeoutTerminalFailureIsServiceUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTerminalFailure(rec, errors.New("upstream error: something else"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("X-Axon-First-Byte-Wait-Ms"); got != "" {
		t.Errorf("X-Axon-First-Byte-Wait-Ms = %q, want it absent", got)
	}
	// The clients of this API parse the error envelope, so the body has to
	// be the envelope and not a bare string.
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body is not the error envelope: %v (body %q)", err, rec.Body.String())
	}
	if envelope.Error.Type != "server_error" {
		t.Errorf("error type = %q, want %q", envelope.Error.Type, "server_error")
	}
	if envelope.Error.Code != "service_unavailable" {
		t.Errorf("error code = %q, want %q", envelope.Error.Code, "service_unavailable")
	}
	// No Retry-After: the gateway does not know when the upstream recovers,
	// and a delay it invented would only defer the retry the client should
	// make immediately.
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q, want it absent", got)
	}
}

// TestFirstByteTimeoutEndsTheSilentGapBeforeTheEdge pins the property the
// deadline exists for: the gateway stops waiting before the proxy in front
// of it gives up.
//
// The upstream here never answers — the exact shape of the 2026-11-18
// incident, where an upstream held the request for 121 seconds and the edge
// cut the connection at that point. The gateway must be the one to answer.
func TestFirstByteTimeoutEndsTheSilentGapBeforeTheEdge(t *testing.T) {
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer silent.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithFirstByteDeadline(ctx, 300*time.Millisecond)

	attemptCtx, cancelAttempt := firstByteAttemptContext(ctx)
	defer cancelAttempt()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, silent.URL, nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)

	if err == nil {
		resp.Body.Close()
		t.Fatal("the request to a silent upstream succeeded")
	}
	if elapsed > 3*time.Second {
		t.Errorf("the wait took %v, want it bounded by the deadline", elapsed)
	}

	// And the failure has to be recognisable as a timeout, not as a client
	// that hung up.
	timedOut, waited := firstByteTimeoutError(attemptCtx, err)
	if !timedOut {
		t.Errorf("the failure was not recognised as a first-byte timeout: %v", err)
	}
	if waited <= 0 {
		t.Errorf("waited = %v, want a positive duration", waited)
	}
}

// TestFirstByteTimeoutDoesNotCarryAnUpstreamStatus pins that a timeout stays
// out of the lock path.
//
// Every status in providers.RetryableStatusCodes makes the fallback layer
// cool the account down and count the failure against the provider breaker.
// A timeout is evidence of neither, so it must not carry a status.
func TestFirstByteTimeoutDoesNotCarryAnUpstreamStatus(t *testing.T) {
	err := firstByteTimeoutUpstreamError(time.Second)

	var ue *upstreamError
	if errors.As(err, &ue) {
		t.Fatalf("the timeout error carries upstream status %d, which would lock the account", ue.StatusCode)
	}
}

// TestFirstByteTimeoutIsNotMistakenForAClientAbort pins the distinction the
// handle exists for.
//
// A client that hangs up and an upstream that never answers both end the
// attempt with a cancelled context. Only the second one is a timeout, and
// only the second one may be reported as one.
func TestFirstByteTimeoutIsNotMistakenForAClientAbort(t *testing.T) {
	ctx := WithFirstByteDeadline(context.Background(), 5*time.Second)

	attemptCtx, cancelAttempt := firstByteAttemptContext(ctx)
	handle := internalproxy.DeadlineHandleFrom(attemptCtx)
	if handle == nil {
		t.Fatal("the attempt context carries no deadline handle")
	}

	// The client hung up.
	cancelAttempt()

	if handle.Fired() {
		t.Error("the handle reported the bound as expired when the client aborted")
	}
	timedOut, _ := firstByteTimeoutError(attemptCtx, context.Canceled)
	if timedOut {
		t.Error("a client abort was reported as a first-byte timeout")
	}
}

func TestClassifyTerminalFailure_FirstByteTimeoutWrapped(t *testing.T) {
	ue := &upstreamError{
		StatusCode: http.StatusGatewayTimeout,
		Body:       []byte(`{"error":{"message":"upstream did not produce a first byte before the deadline after 1m30s","type":"timeout_error","code":504}}`),
	}

	class := classifyTerminalFailure(ue)
	if class != failureClassFirstByteTimeout {
		t.Errorf("classifyTerminalFailure(ue) = %q, want %q", class, failureClassFirstByteTimeout)
	}

	rec := httptest.NewRecorder()
	writeTerminalFailure(rec, ue)
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("writeTerminalFailure status = %d, want %d", rec.Code, http.StatusGatewayTimeout)
	}
}
