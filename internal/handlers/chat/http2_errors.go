package chat

import (
	"errors"
	"strings"

	fhttp2 "github.com/bogdanfinn/fhttp/http2"
	xhttp2 "golang.org/x/net/http2"
)

// HTTP/2 transport failure classification.
//
// The router treats a broken egress as a pool-level fault: a proxy edge whose
// HTTP/2 connection keeps resetting must be penalised and rotated out, or it
// stays in rotation and burns attempts (see pool_fitness.go's
// classifyPoolFailure and combo.go's pool rotation).
//
// AGENTS.md §4.E forbids classifying an error by its text. The typed route
// (errors.As) is therefore the primary one, and the text matcher below is a
// narrow fallback for the shapes the HTTP/2 stacks emit WITHOUT a typed value
// — verified by probe (tmp_h2probe):
//
//	x/net + stdlib: a GOAWAY with a non-NO_ERROR code aborts the stream with
//	    fmt.Errorf("http2: Transport received GOAWAY from server ErrCode:%v", ...)
//	  → *url.Error wrapping a PLAIN error. errors.As finds no typed value:
//	    As xnet.StreamError = false, As xnet.GoAwayError = false.
//
//	x/net + stdlib: an RST_STREAM from the peer surfaces as a typed
//	    StreamError. The stdlib's internal StreamError carries an As() bridge
//	    into x/net/http2.StreamError, so one x/net target covers both stacks;
//	    the fhttp (stealth TLS) stack's own StreamError is checked separately.
//
//	x/net + stdlib: a GOAWAY followed by a clean close surfaces as a typed
//	    GoAwayError value, which x/net and stdlib each define separately with
//	    no As() bridge between them — hence one target per stack.
//
//	x/net + stdlib: a dropped connection surfaces as the plain
//	    errors.New("http2: client connection lost").
//
// Everything the typed route cannot reach is covered by the fallback matcher,
// which is deliberately limited to those documented shapes.

// h2TransportErrorTexts are the plain-text HTTP/2 transport shapes that carry
// no typed error value. Each entry is matched case-insensitively against the
// error text. The list is closed on purpose: a shape that is not listed here
// is not classified as an HTTP/2 transport failure.
var h2TransportErrorTexts = []string{
	// x/net + stdlib: the server sent a GOAWAY with an error code and the
	// stream was aborted. The transport builds this with fmt.Errorf and no
	// typed value, so no errors.As target matches it.
	"transport received goaway from server",
	// x/net + stdlib: the server sent a GOAWAY and closed the connection.
	// Reached as a typed GoAwayError on both stacks; kept here as well so a
	// re-wrapped copy (fmt.Errorf without %w) is still classified.
	"server sent goaway",
	// x/net + stdlib: the connection dropped without a GOAWAY frame.
	"http2: client connection lost",
	// x/net + stdlib + fhttp: a stream was reset. Reached as a typed
	// StreamError; kept here for the re-wrapped copy.
	"stream error",
	// The HTTP/2 error codes that name the reason for a reset or a GOAWAY.
	// Reached through a typed StreamError or GoAwayError; kept here so a
	// re-wrapped copy that dropped the surrounding text is still classified.
	"enhance_your_calm",
	"refused_stream",
}

// isH2TransportError reports whether err is an HTTP/2 transport failure on the
// egress leg (the proxy relay or the connection to the upstream), not a
// failure of the model itself.
//
// The typed route (errors.As) runs first, per AGENTS.md §4.E; the text
// fallback only covers the shapes that arrive without a typed error value.
func isH2TransportError(err error) bool {
	if err == nil {
		return false
	}
	if isTypedH2TransportError(err) {
		return true
	}
	return hasH2TransportErrorText(err.Error())
}

// isTypedH2TransportError recognises the HTTP/2 errors that carry a typed
// value in the error chain.
func isTypedH2TransportError(err error) bool {
	// x/net/http2.StreamError. The stdlib's internal StreamError implements
	// an As() bridge into this exact type, so one target covers the stdlib
	// stack as well.
	//
	// Both the value target and the pointer target are checked: the error
	// chain holds one dynamic type, and errors.As compares that type
	// exactly. A transport that stores the error by value only matches the
	// value target; one that stores it by pointer only matches the pointer
	// target. The transports in this repo build these errors by value
	// (streamError() returns a StreamError value), but a caller that wraps
	// the same error behind a pointer must still be classified.
	var xStream xhttp2.StreamError
	if errors.As(err, &xStream) {
		return true
	}
	var xStreamPtr *xhttp2.StreamError
	if errors.As(err, &xStreamPtr) {
		return true
	}
	// x/net/http2.GoAwayError. x/net and the stdlib each define their own
	// with no As() bridge between them, so both are checked.
	var xGoAway xhttp2.GoAwayError
	if errors.As(err, &xGoAway) {
		return true
	}
	var xGoAwayPtr *xhttp2.GoAwayError
	if errors.As(err, &xGoAwayPtr) {
		return true
	}
	// fhttp/http2 (the stealth TLS client's own HTTP/2 stack).
	var fStream fhttp2.StreamError
	if errors.As(err, &fStream) {
		return true
	}
	var fStreamPtr *fhttp2.StreamError
	if errors.As(err, &fStreamPtr) {
		return true
	}
	var fGoAway fhttp2.GoAwayError
	if errors.As(err, &fGoAway) {
		return true
	}
	var fGoAwayPtr *fhttp2.GoAwayError
	if errors.As(err, &fGoAwayPtr) {
		return true
	}
	return false
}

// hasH2TransportErrorText is the documented fallback for the HTTP/2 transport
// shapes that arrive as plain text: the transports build some of them with
// fmt.Errorf or errors.New, so no typed value exists to match.
func hasH2TransportErrorText(text string) bool {
	text = strings.ToLower(text)
	for _, shape := range h2TransportErrorTexts {
		if strings.Contains(text, shape) {
			return true
		}
	}
	return false
}
