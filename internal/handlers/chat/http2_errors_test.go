package chat

import (
	"errors"
	"fmt"
	"testing"

	fhttp2 "github.com/bogdanfinn/fhttp/http2"
	xhttp2 "golang.org/x/net/http2"
)

func TestIsH2TransportError_TypedErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "plain unrelated error",
			err:  errors.New("something went wrong"),
			want: false,
		},
		{
			name: "x/net StreamError",
			err:  xhttp2.StreamError{StreamID: 1, Code: xhttp2.ErrCodeRefusedStream},
			want: true,
		},
		{
			// The transports build the error by value (streamError() returns
			// a StreamError value), and the stdlib's own h2 stack exposes the
			// same value shape, so the wrapped operand is a value too. A
			// pointer would not match the value target the classifier uses:
			// errors.As walks the chain and compares the dynamic type.
			name: "wrapped x/net StreamError",
			err:  fmt.Errorf("stream failed: %w", xhttp2.StreamError{StreamID: 1, Code: xhttp2.ErrCodeCancel}),
			want: true,
		},
		{
			name: "x/net GoAwayError",
			err:  xhttp2.GoAwayError{LastStreamID: 3, ErrCode: xhttp2.ErrCodeEnhanceYourCalm, DebugData: "calm down"},
			want: true,
		},
		{
			name: "wrapped x/net GoAwayError",
			err:  fmt.Errorf("upstream rejected: %w", xhttp2.GoAwayError{LastStreamID: 3, ErrCode: xhttp2.ErrCodeNo}),
			want: true,
		},
		{
			name: "fhttp StreamError",
			err:  fhttp2.StreamError{StreamID: 5, Code: fhttp2.ErrCodeRefusedStream},
			want: true,
		},
		{
			name: "wrapped fhttp StreamError",
			err:  fmt.Errorf("stealth transport stream failed: %w", fhttp2.StreamError{StreamID: 5, Code: fhttp2.ErrCodeRefusedStream}),
			want: true,
		},
		{
			name: "fhttp GoAwayError",
			err:  fhttp2.GoAwayError{LastStreamID: 7, ErrCode: fhttp2.ErrCodeEnhanceYourCalm, DebugData: "too fast"},
			want: true,
		},
		{
			name: "wrapped fhttp GoAwayError",
			err:  fmt.Errorf("stealth transport goaway: %w", fhttp2.GoAwayError{LastStreamID: 7, ErrCode: fhttp2.ErrCodeNo}),
			want: true,
		},
		{
			// A pointer-typed error is a different dynamic type, so the
			// value-target typed route does not match it; the text fallback
			// is what classifies it. This case pins that behaviour so the
			// typed route is not mistaken for a universal matcher.
			name: "pointer-typed x/net StreamError",
			err:  fmt.Errorf("stream failed: %w", error(&xhttp2.StreamError{StreamID: 1, Code: xhttp2.ErrCodeCancel})),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isH2TransportError(tc.err)
			if got != tc.want {
				t.Fatalf("isH2TransportError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsH2TransportError_UntypedFallbacks(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "server sent GOAWAY and closed connection (stdlib text shape)",
			err:  errors.New("http2: server sent GOAWAY and closed the connection; LastStreamID=1, ErrCode=NO_ERROR, debug=\"\""),
			want: true,
		},
		{
			name: "transport received GOAWAY from server (x/net and stdlib shape)",
			err:  errors.New("http2: Transport received GOAWAY from server ErrCode:ENHANCE_YOUR_CALM, debugText:\"rate limit\""),
			want: true,
		},
		{
			name: "client connection lost",
			err:  errors.New("http2: client connection lost"),
			want: true,
		},
		{
			name: "stream error untyped",
			err:  errors.New("stream error: stream ID 3; REFUSED_STREAM"),
			want: true,
		},
		{
			name: "untyped case insensitive matching",
			err:  errors.New("SERVER SENT GOAWAY"),
			want: true,
		},
		{
			name: "unrelated error should not match",
			err:  errors.New("context deadline exceeded"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isH2TransportError(tc.err)
			if got != tc.want {
				t.Fatalf("isH2TransportError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsProxyTransportError_H2Integration(t *testing.T) {
	// Verifies isProxyTransportError delegates to isH2TransportError for both typed and untyped h2 errors.
	h2Errors := []error{
		&xhttp2.GoAwayError{LastStreamID: 1, ErrCode: xhttp2.ErrCodeEnhanceYourCalm},
		xhttp2.StreamError{StreamID: 1, Code: xhttp2.ErrCodeRefusedStream},
		&fhttp2.GoAwayError{LastStreamID: 1, ErrCode: fhttp2.ErrCodeNo},
		errors.New("http2: server sent GOAWAY and closed the connection"),
		errors.New("http2: client connection lost"),
		errors.New("stream error: stream ID 5; CANCEL"),
	}

	for _, err := range h2Errors {
		if !isProxyTransportError(err, 0) {
			t.Errorf("isProxyTransportError(%v) = false; want true", err)
		}
	}
}