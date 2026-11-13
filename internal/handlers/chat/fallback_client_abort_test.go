package chat

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// Upstream #5639: a client that hung up is not a provider failure.
//
// The fork already reported the abort as 499 at the response site, but two
// things still ran after it: the retryable-status branch cooled a healthy
// account down (a 502/503/504 that arrived while the client was leaving), and
// the account loop went on to the next connection instead of stopping.
//
// These tests pin the classification the guards are built on. The
// account-walk and account-lock behaviour itself needs a database, and the
// sqlite_legacy suite this repository carries does not compile any more (the
// store moved to Postgres), so the guards are pinned here at the level they
// read: the abort must be recognised as an abort, and it must be recognised
// as an abort even when the error also carries an upstream status.

// TestClientCanceled_AbortIsRecognisedAcrossShapes pins that every shape a
// client abort arrives in is classified as one: the context alone, the 499
// this package already produced, and the two together.
func TestClientCanceled_AbortIsRecognisedAcrossShapes(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if !isClientCanceled(canceled, nil) {
		t.Error("a canceled context must be recognised as a client abort even without an error")
	}
	if !isClientCanceled(context.Background(), clientClosedError()) {
		t.Error("the 499 this package produces must be recognised as a client abort")
	}
	if !isClientCanceled(canceled, clientClosedError()) {
		t.Error("a canceled context plus the 499 must be recognised as a client abort")
	}
}

// TestClientCanceled_UpstreamStatusIsNotAnAbort pins the other direction, and
// the reason the guard cannot simply look at the error: an upstream that
// answered a transient 502 while the client was still connected is a provider
// failure, and the abort decision has to come from the CONTEXT.
func TestClientCanceled_UpstreamStatusIsNotAnAbort(t *testing.T) {
	live := context.Background()
	ue := &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"bad gateway"}}`)}
	if isClientCanceled(live, ue) {
		t.Error("a 502 on a live request is a provider failure, not a client abort")
	}
	// A gateway deadline is not a client hangup either: the dashboard model
	// probe sets one of its own and must not be reported as 499.
	deadline, cancel := context.WithDeadline(context.Background(), timeInThePast())
	defer cancel()
	if isClientCanceled(deadline, ue) {
		t.Error("a gateway deadline must not be classified as a client abort")
	}
}

// TestClientClosedError_IsTheCanonical499 pins the shape every abort path
// answers with, so a client cannot tell which path produced it.
func TestClientClosedError_IsTheCanonical499(t *testing.T) {
	err := clientClosedError()
	if err.StatusCode != StatusClientClosedRequest {
		t.Errorf("StatusCode = %d, want %d", err.StatusCode, StatusClientClosedRequest)
	}
	if want := `{"error":{"message":"client closed request","type":"client_closed_request","code":499}}`; string(err.Body) != want {
		t.Errorf("body = %s, want %s", err.Body, want)
	}
}

// TestIsProxyTransportError_H2ResetShapesAreTransportErrors pins the h2
// classification the gap audit folded into #5639: an HTTP/2 stream reset is
// the egress leg dying, so it belongs to the proxy pool's fitness and not to
// the model. Go spells these shapes differently from undici, and the
// GOAWAY form was not recognised at all — a pool that kept answering GOAWAY
// took no penalty and stayed in rotation.
func TestIsProxyTransportError_H2ResetShapesAreTransportErrors(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"goaway", `Post "https://api.example.com/v1/chat/completions": http2: server sent GOAWAY and closed the connection; LastStreamID=42, ErrCode=NO_ERROR`},
		{"enhance your calm", `stream error: stream ID 7; ENHANCE_YOUR_CALM`},
		{"refused stream", `stream error: stream ID 9; REFUSED_STREAM`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !isProxyTransportError(errors.New(tt.text), 0) {
				t.Errorf("isProxyTransportError(%q) = false, want true — an h2 reset is the egress leg dying", tt.text)
			}
		})
	}
}

// TestIsProxyTransportError_ClientCancelIsNotTransportError pins the other
// direction: a client that left says nothing about the pool, so it must not
// be charged to the pool's fitness.
func TestIsProxyTransportError_ClientCancelIsNotTransportError(t *testing.T) {
	if isProxyTransportError(errors.New("upstream request: context canceled"), 0) {
		t.Error("a client cancel must not be charged to the proxy pool")
	}
}

// timeInThePast returns a moment already gone, for the deadline test.
func timeInThePast() time.Time {
	return time.Now().Add(-time.Hour)
}
