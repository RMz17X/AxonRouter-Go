package proxy

import (
	"net/http"
	"time"
)

// ClientWithResponseHeaderTimeout returns a shallow copy of client whose
// underlying transports wait up to d for response headers. It is for calls
// whose upstream legitimately holds headers for minutes (MiniMax H3-Image can
// take up to 600 s) without raising the two-minute default for every lane.
//
// Connection-specific proxies are preserved: the transport is cloned, not
// replaced, and the wrappers this package owns are rebuilt around the clone.
func ClientWithResponseHeaderTimeout(client *http.Client, d time.Duration) *http.Client {
	if client == nil || d <= 0 {
		return client
	}
	clone := *client
	clone.Transport = transportWithHeaderTimeout(client.Transport, d)
	return &clone
}

func transportWithHeaderTimeout(rt http.RoundTripper, d time.Duration) http.RoundTripper {
	switch t := rt.(type) {
	case *http.Transport:
		clone := t.Clone()
		clone.ResponseHeaderTimeout = d
		return clone
	case *FallbackTransport:
		clone := *t
		clone.Base = transportWithHeaderTimeout(t.Base, d)
		clone.Direct = transportWithHeaderTimeout(t.Direct, d)
		return &clone
	case forbidDirectReplayTransport:
		t.base = transportWithHeaderTimeout(t.base, d)
		return t
	case *relayTransport:
		clone := *t
		clone.base = transportWithHeaderTimeout(t.base, d)
		return &clone
	default:
		return rt
	}
}
