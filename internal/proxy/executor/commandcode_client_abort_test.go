package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"axonrouter/proxy/internal/providers"
)

// Upstream #5639: a caller abort (client disconnect) is not a provider
// failure. The CommandCode executor retried a transient 502/503/504 purely on
// the status, so a client that had already left still had its request
// replayed against the upstream — burning the seat's quota for a turn nobody
// reads. The retry has to consult the request context, not the status alone.

// TestForwardCommandcodeDoesNotRetryAfterClientAbort pins the guard: the
// upstream answered a transient 502, the client hung up, and the retry must
// not be sent.
func TestForwardCommandcodeDoesNotRetryAfterClientAbort(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// The upstream answers, the client hangs up, then the backoff would run.
	cancel()

	start := time.Now()
	rec := httptest.NewRecorder()
	err := ForwardCommandcode(rec, &Request{
		Ctx:    ctx,
		Config: &providers.ProviderConfig{BaseURL: srv.URL}, Client: srv.Client(),
		Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), SessionID: "s",
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled — a client abort must not surface as an upstream status", err, err)
	}
	// The retry backoff is a second; a canceled context must not pay it.
	if elapsed > time.Second {
		t.Errorf("cancelled client waited %v for a retry backoff it cannot use", elapsed)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected no upstream call for an already-cancelled context, got %d", got)
	}
}

// TestForwardCommandcodeAbortAfterTransientAnswerIsNotRetried covers the
// timing that burned seats: the upstream answered a transient 502 while the
// client was still connected, and the client hung up before the retry.
func TestForwardCommandcodeAbortAfterTransientAnswerIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	rec := httptest.NewRecorder()
	err := ForwardCommandcode(rec, &Request{
		Ctx:    ctx,
		Config: &providers.ProviderConfig{BaseURL: srv.URL}, Client: srv.Client(),
		Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), SessionID: "s",
	})
	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 upstream attempt (no retry after the abort), got %d", got)
	}
}

// TestForwardCommandcodeLiveClientStillRetries is the guard against the fix
// going too far: a live client keeps the transient retry behaviour.
func TestForwardCommandcodeLiveClientStillRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"type":"text-delta","text":"ok"}` + "\n" + `{"type":"finish","finishReason":"stop"}` + "\n"))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	err := ForwardCommandcode(rec, &Request{
		Ctx:    context.Background(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL}, Client: srv.Client(),
		Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), SessionID: "s",
	})
	if err != nil {
		t.Fatalf("ForwardCommandcode: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 attempts (1 failing + 1 served), got %d", got)
	}
}
