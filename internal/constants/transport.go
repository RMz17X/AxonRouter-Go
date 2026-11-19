package constants

import (
	"net/http"
	"time"
)

// HTTPTransportConfig encapsulates connection pooling and timeout configurations
// for upstream HTTP transports in reverse proxying and streaming workloads.
type HTTPTransportConfig struct {
	// MaxIdleConns controls the maximum number of idle (keep-alive) connections across all hosts.
	MaxIdleConns int

	// MaxIdleConnsPerHost controls the maximum idle (keep-alive) connections to keep per-host.
	// Go standard library defaults this to 2, which causes connection thrashing under high concurrency.
	MaxIdleConnsPerHost int

	// IdleConnTimeout is the maximum amount of time an idle (keep-alive) connection will remain idle before closing itself.
	IdleConnTimeout time.Duration

	// TLSHandshakeTimeout specifies the maximum amount of time waiting to wait for a TLS handshake.
	TLSHandshakeTimeout time.Duration

	// ExpectContinueTimeout specifies the amount of time to wait for a server's first response headers
	// after fully writing the request headers if the request has an "Expect: 100-continue" header.
	ExpectContinueTimeout time.Duration

	// ResponseHeaderTimeout specifies the amount of time to wait for a server's response headers after fully writing the request.
	ResponseHeaderTimeout time.Duration
}

// DefaultHTTPTransportConfig defines the high-throughput production defaults for upstream LLM reverse-proxy connections.
//
// ResponseHeaderTimeout is left at two minutes on purpose, and it is no longer
// the thing that bounds the wait for an upstream.
//
// The edge proxy in front of this deployment (Cloudflare, on the Free plan)
// closes an idle connection at roughly 100 seconds. The gateway's wait for an
// upstream's response headers used to be 120 seconds — longer than the edge's
// patience. The two raced, and the edge won: on 2026-11-18 an upstream that
// never sent its response headers held the request for 121 seconds, the edge
// cut the connection at that point, and the client received a 524 it could
// not act on. 809 such responses were measured in 36 hours.
//
// The wait is now bounded by the request's first-byte deadline (90 seconds by
// default, see internal/handlers/chat/first_byte_deadline.go), which is
// shorter than the edge's patience, so the gateway answers first and the
// client gets the gateway's own status rather than the edge's.
//
// This transport timeout stays at two minutes because it is now a backstop
// rather than the limit: the deadline fires first, so this value only decides
// what happens on a path that carries no deadline at all. Lowering it would
// cut requests that answer slowly but successfully — the measured
// time-to-first-token p99 was 75.9 seconds, and a cut at 75 would have
// failed requests that the gateway can still serve.
var DefaultHTTPTransportConfig = HTTPTransportConfig{
	MaxIdleConns:          256,
	MaxIdleConnsPerHost:   128,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 2 * time.Minute,
}

// Configure applies the connection pool and timeout settings to an existing *http.Transport.
func (c HTTPTransportConfig) Configure(t *http.Transport) {
	if t == nil {
		return
	}
	t.ForceAttemptHTTP2 = true
	t.MaxIdleConns = c.MaxIdleConns
	t.MaxIdleConnsPerHost = c.MaxIdleConnsPerHost
	t.IdleConnTimeout = c.IdleConnTimeout
	t.TLSHandshakeTimeout = c.TLSHandshakeTimeout
	t.ExpectContinueTimeout = c.ExpectContinueTimeout
	t.ResponseHeaderTimeout = c.ResponseHeaderTimeout
}

// NewTransport instantiates a fresh *http.Transport with these settings applied and Proxy: nil.
func (c HTTPTransportConfig) NewTransport() *http.Transport {
	t := &http.Transport{
		Proxy: nil,
	}
	c.Configure(t)
	return t
}
