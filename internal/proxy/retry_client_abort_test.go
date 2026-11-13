package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A client that hung up is not a provider failure. Upstream #5639 makes the
// abort classification independent of the error's name: the executor must not
// retry a request whose caller has already left, and the abort must not be
// re-shaped into a transient 502/503/504 on the way out.
//
// The port keeps the same contract on the Go side, where the shape is
// different: a canceled request context is the signal (an *url.Error wrapping
// context.Canceled says nothing about the caller), so every place that used
// to re-read the upstream status has to consult the context instead.

// TestRetryTransientUpstream_ClientAbortDuringBackoffIsNotRetried pins the
// core of #5639: the upstream answered a transient 503, the client hung up
// during the backoff, and the retry must not be sent.
//
// Before the fix the backoff was abandoned but the PREVIOUS error was
// returned verbatim — an *UpstreamError carrying 503 — so the caller saw a
// provider failure for a disconnect it caused, and the failover path burned
// the next account over it.
func TestRetryTransientUpstream_ClientAbortDuringBackoffIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"service_overloaded"}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// The upstream answers, the client hangs up, then the backoff starts.
	cancel()

	start := time.Now()
	_, err := DoRequest(ctx, srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	// The abort has to arrive as the abort, not as the upstream's status.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled — a client abort must not surface as an upstream status", err, err)
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		t.Fatalf("a client abort was reported as upstream status %d — it is not an upstream failure", ue.StatusCode)
	}
	// The 503 policy waits 2s per retry; a canceled context must not pay it.
	if elapsed > time.Second {
		t.Errorf("cancelled client waited %v for a retry backoff it cannot use", elapsed)
	}
	// No attempt at all: the client was already gone before the request went out.
	if got := calls.Load(); got != 0 {
		t.Errorf("expected no upstream call for an already-cancelled context, got %d", got)
	}
}

// TestRetryTransientUpstream_ClientAbortAfterTransientAnswerIsNotRetried
// covers the timing that actually burned accounts: the upstream answered a
// transient 503 while the client was still connected, and the client hung up
// before the backoff finished. The retry must not go out, and the answer must
// not be the 503.
func TestRetryTransientUpstream_ClientAbortAfterTransientAnswerIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// The client leaves while the first attempt is in flight.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := DoRequest(ctx, srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled — the abort must not be re-shaped into the upstream's 502", err, err)
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		t.Fatalf("a client abort was reported as upstream status %d", ue.StatusCode)
	}
	// The 502 policy waits 3s per retry; the abort must cut it short.
	if elapsed > 2*time.Second {
		t.Errorf("cancelled client waited %v for a retry backoff it cannot use", elapsed)
	}
	// Exactly one attempt: the retry that would have followed the 502 was
	// never sent.
	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly 1 upstream attempt (no retry after the abort), got %d", got)
	}
}

// TestRetryTransientUpstream_LiveClientStillRetries is the guard against the
// fix going too far: a live client keeps the transient retry behaviour the
// policy table describes.
func TestRetryTransientUpstream_LiveClientStillRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"service_overloaded"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"ok\":true}\n\n"))
	}))
	defer srv.Close()

	resp, err := DoRequest(context.Background(), srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("a recovered upstream must serve the request, got %v", err)
	}
	defer resp.Body.Close()
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 attempts (2 failing + 1 served), got %d", got)
	}
}

// TestRetryTransientUpstream_AbortAfterRetryAttemptIsReportedAsAbort covers
// the window after a RETRY attempt: the second attempt answered a transient
// status and the client hung up while it was in flight. The caller must get
// the abort, not the status.
func TestRetryTransientUpstream_AbortAfterRetryAttemptIsReportedAsAbort(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"service_overloaded"}}`))
			return
		}
		// The second attempt hangs until the test releases it, so the
		// client is the one that ends the exchange.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	// Deferred calls run last-in-first-out: the release must fire before the
	// server is closed, or Close waits on the still-active handler.
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(2500 * time.Millisecond)
		cancel()
	}()

	_, err := DoRequest(ctx, srv.Client(), "POST", srv.URL, nil, []byte(`{"model":"x"}`))
	if err == nil {
		t.Fatal("expected an error for a cancelled client context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v (%T), want context.Canceled", err, err)
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		t.Fatalf("a client abort was reported as upstream status %d", ue.StatusCode)
	}
}
