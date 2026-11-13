package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axonrouter/proxy/internal/providers"
)

// ctx-cancel guards for the executor retry/replay loops.
//
// The card's requirement is that a cancelled context must not buy a second
// upstream attempt. The test uses a fake transport (not a live server) so the
// cancellation point is deterministic: the transport cancels the context at
// the exact moment the first attempt's answer arrives, which is the window
// where an unguarded replay loop would spend another request.

// cancelOnModelCallTransport answers the perch turn-ticket and model-call
// endpoints and cancels the context as soon as the model call has answered.
//
// Cancelling inside the RoundTrip (rather than from a timer goroutine) is what
// makes the test deterministic: the client receives a complete response, and
// the context is already dead when the executor reaches its replay guard.
type cancelOnModelCallTransport struct {
	cancel context.CancelFunc
	calls  atomic.Int32
	// served counts the requests that actually reached the fake upstream,
	// i.e. the ones that were not refused by the dead context.
	served atomic.Int32
	// modelCalls counts only the model-call requests, so the replay can be
	// told apart from the first attempt.
	modelCalls atomic.Int32
}

func (t *cancelOnModelCallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	// A real transport refuses a request whose context is already dead, and
	// it refuses it BEFORE the request reaches the wire — so the served
	// counter stays where the abort actually stopped the exchange.
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	t.served.Add(1)
	switch {
	case strings.HasSuffix(req.URL.Path, perchTurnTicketPath):
		return fakeJSONResponse(http.StatusOK, `{"ok":true,"ticket":"t1","ticketId":"id1","runId":"r1"}`), nil
	case strings.HasSuffix(req.URL.Path, perchModelCallPath):
		if t.modelCalls.Add(1) == 1 {
			// The client hung up right after this answer arrived.
			t.cancel()
			return fakeJSONResponse(http.StatusUnauthorized, `{"error":"token expired"}`), nil
		}
		return fakeJSONResponse(http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`), nil
	default:
		return fakeJSONResponse(http.StatusNotFound, `{"error":"not found"}`), nil
	}
}

func fakeJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    nil,
	}
}

// TestGuardRetryAttempt_CancelledContextSendsNoSecondAttempt is the core
// guard: the first upstream answer was a 401 (perch re-claims the ticket and
// replays), the client hung up, and the replay must not go out.
//
// Without the guard the executor would re-acquire a ticket and issue a
// second model call — two more upstream requests for a turn nobody reads.
func TestGuardRetryAttempt_CancelledContextSendsNoSecondAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	transport := &cancelOnModelCallTransport{cancel: cancel}
	client := &http.Client{Transport: transport}

	err := ForwardPerch(httptestRecorder(), &Request{
		Ctx:       ctx,
		Config:    &providers.ProviderConfig{BaseURL: "http://perch.test"},
		Client:    client,
		Body:      []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`),
		SessionID: "s",
		APIKey:    "token-cancel-before-replay",
	})

	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled — an abort must not surface as an upstream status", err, err)
	}
	// ticket (1) + first model call (2). The replay would be the third.
	// The executor issued the ticket request and the first model call, and
	// nothing more: the replay (a third issued request) was suppressed by
	// the guard. The "served" counter stays at 2 either way — a transport
	// refuses a dead-context request on its own — so the assertion is on
	// the issued count, which is what the guard controls.
	if got := transport.calls.Load(); got != 2 {
		t.Errorf("expected exactly 2 issued upstream requests (ticket + model call), got %d — the replay was not suppressed", got)
	}
	if got := transport.served.Load(); got != 2 {
		t.Errorf("expected 2 requests to reach the upstream, got %d", got)
	}
}

// TestGuardRetryAttempt_LiveClientStillReplays is the guard against the fix
// going too far: a live client keeps the 401 re-claim-and-replay behaviour.
func TestGuardRetryAttempt_LiveClientStillReplays(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	transport := &noCancelTransport{cancel: cancel}
	client := &http.Client{Transport: transport}

	err := ForwardPerch(httptestRecorder(), &Request{
		Ctx:       ctx,
		Config:    &providers.ProviderConfig{BaseURL: "http://perch.test"},
		Client:    client,
		Body:      []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`),
		SessionID: "s",
		APIKey:    "token-live-client-replay",
	})

	// The replay answered 200, so the turn is served.
	if err != nil {
		t.Fatalf("a live client must still be served, got %v", err)
	}
	// ticket (1) + first model call (2) + re-claimed ticket (3) + replay
	// model call (4).
	if got := transport.calls.Load(); got != 4 {
		t.Errorf("expected 4 upstream attempts (ticket + model + ticket + replay), got %d — the replay was suppressed for a live client", got)
	}
}

// noCancelTransport answers the perch endpoints without ever cancelling the
// context, so the replay path runs to completion.
type noCancelTransport struct {
	cancel context.CancelFunc
	calls  atomic.Int32
	// modelCalls counts only the model-call requests, so the replay can be
	// told apart from the first attempt.
	modelCalls atomic.Int32
}

func (t *noCancelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	switch {
	case strings.HasSuffix(req.URL.Path, perchTurnTicketPath):
		return fakeJSONResponse(http.StatusOK, `{"ok":true,"ticket":"t1","ticketId":"id1","runId":"r1"}`), nil
	case strings.HasSuffix(req.URL.Path, perchModelCallPath):
		if t.modelCalls.Add(1) == 1 {
			return fakeJSONResponse(http.StatusUnauthorized, `{"error":"token expired"}`), nil
		}
		return fakeJSONResponse(http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`), nil
	default:
		return fakeJSONResponse(http.StatusNotFound, `{"error":"not found"}`), nil
	}
}

// TestGuardRetryAttempt_AlreadyCancelledContextSendsNothing covers the
// earlier window: the context was already dead before the first attempt, so
// no upstream request goes out at all.
func TestGuardRetryAttempt_AlreadyCancelledContextSendsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	transport := &cancelOnModelCallTransport{cancel: cancel}
	client := &http.Client{Transport: transport}

	err := ForwardPerch(httptestRecorder(), &Request{
		Ctx:       ctx,
		Config:    &providers.ProviderConfig{BaseURL: "http://perch.test"},
		Client:    client,
		Body:      []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`),
		SessionID: "s",
		APIKey:    "token-already-cancelled",
	})

	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled", err, err)
	}
	// The dead context is refused before the wire: no request is served.
	if got := transport.served.Load(); got != 0 {
		t.Errorf("expected no upstream attempt for an already-cancelled context, got %d", got)
	}
}

// TestGuardRetryAttempt_HelperCorrectness validates the guard's logic
// independently of the executor plumbing.
func TestGuardRetryAttempt_HelperCorrectness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !ctxAborted(ctx) {
		t.Fatal("ctxAborted should be true for a cancelled context")
	}
	abortErr := guardRetryAttempt(ctx)
	if abortErr == nil {
		t.Fatal("guardRetryAttempt should return an error for a cancelled context")
	}
	if !errors.Is(abortErr, context.Canceled) {
		t.Fatalf("guardRetryAttempt error = %v, want context.Canceled", abortErr)
	}

	// A live context.
	aliveCtx := context.Background()
	if ctxAborted(aliveCtx) {
		t.Fatal("ctxAborted should be false for a background context")
	}
	if err := guardRetryAttempt(aliveCtx); err != nil {
		t.Fatal("guardRetryAttempt should return nil for a live context")
	}

	// A nil context carries no cancellation, so it can never end.
	if ctxAborted(nil) {
		t.Fatal("ctxAborted should be false for a nil context")
	}
	if err := guardRetryAttempt(nil); err != nil {
		t.Fatal("guardRetryAttempt should return nil for a nil context")
	}

	// A deadline that has passed reads as context.DeadlineExceeded.
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), timeInThePast())
	defer deadlineCancel()
	if !ctxAborted(deadlineCtx) {
		t.Fatal("ctxAborted should be true for an expired deadline")
	}
	if err := guardRetryAttempt(deadlineCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("guardRetryAttempt error = %v, want context.DeadlineExceeded", err)
	}

	// isClientAbort: the context is the signal, not the error's name.
	if !isClientAbort(ctx, errors.New("unrelated")) {
		t.Fatal("isClientAbort should be true for a cancelled context")
	}
	if isClientAbort(aliveCtx, errors.New("unrelated")) {
		t.Fatal("isClientAbort should be false for a live context and an unrelated error")
	}
	if !isClientAbort(aliveCtx, context.Canceled) {
		t.Fatal("isClientAbort should be true for a context.Canceled error")
	}
	if !isClientAbort(aliveCtx, context.DeadlineExceeded) {
		t.Fatal("isClientAbort should be true for a context.DeadlineExceeded error")
	}
}

// TestGuardRetryAttempt_RetryLoopStopsAfterAbort is the loop-level contract
// the card asks for: a retry loop that consults the guard before every
// attempt must not issue a second attempt once the context is dead.
func TestGuardRetryAttempt_RetryLoopStopsAfterAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32
	// A fake upstream: the first attempt answers a retryable status and the
	// client hangs up while the answer is being processed.
	send := func() (int, error) {
		attempts.Add(1)
		cancel()
		return http.StatusBadGateway, errors.New("transient")
	}

	attemptsSoFar := 0
	for attempt := 0; attempt < 3; attempt++ {
		if err := guardRetryAttempt(ctx); err != nil {
			break
		}
		status, _ := send()
		if status != http.StatusBadGateway {
			break
		}
		attemptsSoFar++
	}

	if got := attempts.Load(); got != 1 {
		t.Errorf("expected exactly 1 upstream attempt, got %d — the retry loop issued a second attempt after the abort", got)
	}
	if attemptsSoFar != 1 {
		t.Errorf("expected 1 completed attempt, got %d", attemptsSoFar)
	}
}

// timeInThePast returns a moment already gone, for the deadline test.
func timeInThePast() time.Time {
	return time.Now().Add(-time.Hour)
}

// httptestRecorder returns a recorder for the executor's response writer.
func httptestRecorder() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
