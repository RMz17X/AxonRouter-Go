package chat

import (
	"context"
	"net/http"

	"axonrouter/proxy/internal/auth"
)

// clientIPContextKey carries the resolved client IP from the HTTP entry
// points to the usage loggers. Unexported, like the guardrail key's, so
// nothing outside this package can spoof a caller address.
type clientIPContextKey struct{}

// withClientIP records the caller's IP on the context. An unresolved IP
// ("unknown" included) is still recorded: the usage rows should say the
// address could not be resolved rather than pretend there was none, but
// an empty value leaves the context untouched.
func withClientIP(ctx context.Context, ip string) context.Context {
	if ctx == nil || ip == "" {
		return ctx
	}
	return context.WithValue(ctx, clientIPContextKey{}, ip)
}

// clientIPFromContext returns the recorded client IP, or "".
func clientIPFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(clientIPContextKey{}).(string)
	return v
}

// requestClientIP resolves the client IP for a gateway request through
// the same resolver the dashboard login uses (auth.LoginClientIP): the
// edge-stamped real IP when the peer proof checks out, TRUSTED_PROXIES /
// TRUST_PROXY forwarding chains, Cloudflare headers when trusted, else
// the socket address. One resolver for the whole product, so the IP a
// usage row names is the IP the login rate limiter would name.
func requestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	return auth.LoginClientIP(r)
}
