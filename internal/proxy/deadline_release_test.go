package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRetryableStatusKeepsTheDeadlineArmed pins that a failure status does
// not release the first-byte bound.
//
// The caller retries a retryable status, and that retry is another wait for
// a first byte that has not happened yet. Releasing the bound on the failure
// would leave the retry unbounded, which is the silent gap the bound exists
// to close.
func TestRetryableStatusKeepsTheDeadlineArmed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, handle := NewDeadlineHandle(ctx, time.Now().Add(2*time.Second))

	resp, err := DoRequest(ctx, &http.Client{}, http.MethodPost, upstream.URL, nil, []byte(`{}`))
	if err == nil {
		resp.Body.Close()
		t.Fatal("the 429 was not reported as an error")
	}
	if handle.Fired() {
		t.Error("the bound fired on a response that arrived inside it")
	}

	// The bound is still armed: the handle reports no expiry, and the
	// context is still alive.
	if ctx.Err() != nil {
		t.Errorf("the attempt context ended with %v after a retryable status", ctx.Err())
	}
}

// TestSuccessReleasesTheDeadline pins that a successful response releases
// the bound, so a long generation is not cut in half.
func TestSuccessReleasesTheDeadline(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, handle := NewDeadlineHandle(ctx, time.Now().Add(2*time.Second))

	resp, err := DoRequest(ctx, &http.Client{}, http.MethodPost, upstream.URL, nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("the request failed: %v", err)
	}
	defer resp.Body.Close()

	handle.Release()
	time.Sleep(100 * time.Millisecond)
	if handle.Fired() {
		t.Error("the bound fired after a successful response")
	}
	if ctx.Err() != nil {
		t.Errorf("the attempt context ended with %v after a successful response", ctx.Err())
	}
}

// TestDeadlineIsNotReleasedBeforeTheResponseArrives pins the timing: the
// bound is still armed while the upstream is thinking.
func TestDeadlineIsNotReleasedBeforeTheResponseArrives(t *testing.T) {
	// The handler drains the request body first: a server that has not read
	// the body cannot notice that the peer closed the connection, so it
	// would hold the test open for its own timeout instead of the deadline.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, handle := NewDeadlineHandle(ctx, time.Now().Add(150*time.Millisecond))

	start := time.Now()
	resp, err := DoRequest(ctx, &http.Client{}, http.MethodPost, upstream.URL, nil, []byte(`{}`))
	elapsed := time.Since(start)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("the request to a silent upstream succeeded")
	}
	if elapsed > 3*time.Second {
		t.Errorf("the request took %v, want it bounded by the deadline", elapsed)
	}
	if !handle.Fired() {
		t.Error("the handle did not record that the bound expired")
	}
}
