package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDeadlineReaderBoundsSilentUpstream proves the reader interrupts a
// pending read when the deadline passes.
//
// The scenario is the one measured on 2026-11-18: an upstream that accepts
// the connection and then says nothing. Go's transport aborts the read when
// the request context is cancelled, but the error it reports is the context's
// cause — which for a client that hung up is context.Canceled. The reader
// has to report the deadline itself, or the gateway cannot tell the two
// apart.
func TestDeadlineReaderBoundsSilentUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithFirstByteDeadlineAt(ctx, time.Now().Add(200*time.Millisecond))

	resp, err := http.DefaultClient.Do(mustRequest(ctx, upstream.URL))
	if err != nil {
		t.Fatalf("upstream request failed: %v", err)
	}
	defer resp.Body.Close()

	body := NewDeadlineReader(ctx, resp.Body)

	start := time.Now()
	_, err = io.ReadAll(body)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("read succeeded against a silent upstream")
	}
	if elapsed > 3*time.Second {
		t.Errorf("read took %v, want it bounded by the deadline", elapsed)
	}
}

// TestDeadlineReaderStaysArmedForTheWholeBody proves the non-streaming
// reader keeps its bound after the first byte.
//
// The non-streaming lane reads the whole body before it writes a single
// header, so the client sees nothing for the entire generation. A bound
// that released on the first byte would let that generation run unbounded.
func TestDeadlineReaderStaysArmedForTheWholeBody(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithFirstByteDeadlineAt(ctx, time.Now().Add(300*time.Millisecond))

	resp, err := http.DefaultClient.Do(mustRequest(ctx, upstream.URL))
	if err != nil {
		t.Fatalf("upstream request failed: %v", err)
	}
	defer resp.Body.Close()

	body := NewDeadlineReader(ctx, resp.Body)

	buf := make([]byte, 8)
	n, err := body.Read(buf)
	if err != nil || n != 7 {
		t.Fatalf("first read = %d bytes, err %v; want 7 bytes", n, err)
	}

	// The second read has to hit the bound: the upstream has written
	// everything it is going to write and is now holding the connection
	// open, so the read can only end when the deadline closes the body.
	_, err = body.Read(buf)
	if err == nil {
		close(release)
		t.Fatal("the second read succeeded against a stalled upstream")
	}
	close(release)
}

// TestFirstByteDeadlineReaderReleasesOnFirstByte proves the streaming
// reader lets a long generation continue.
//
// 281 requests on 2026-11-18 ran longer than 100 seconds and still answered
// 200. A bound that kept running would have cut them.
func TestFirstByteDeadlineReaderReleasesOnFirstByte(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		_, _ = w.Write([]byte("second"))
	}))
	defer upstream.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = WithFirstByteDeadlineAt(ctx, time.Now().Add(200*time.Millisecond))

	resp, err := http.DefaultClient.Do(mustRequest(ctx, upstream.URL))
	if err != nil {
		t.Fatalf("upstream request failed: %v", err)
	}
	defer resp.Body.Close()

	body := NewFirstByteDeadlineReader(ctx, resp.Body)

	buf := make([]byte, 8)
	n, err := body.Read(buf)
	if err != nil || n != 5 {
		t.Fatalf("first read = %d bytes, err %v; want 5 bytes", n, err)
	}

	// Past the deadline, the reader must still serve the body. The upstream
	// only writes the rest once it is released, so the release happens
	// first: the point of the test is that the reader does not cut the
	// stream, not that the upstream is fast.
	time.Sleep(300 * time.Millisecond)
	close(release)

	n, err = body.Read(buf)
	if errors.Is(err, ErrDeadlineExceeded) {
		t.Fatal("the reader cut the stream at the first-byte deadline")
	}
	if n == 0 {
		t.Fatalf("read after the deadline returned no bytes (err %v)", err)
	}
}

func mustRequest(ctx context.Context, url string) *http.Request {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		panic(err)
	}
	return req
}
