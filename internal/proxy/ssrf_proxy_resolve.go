package proxy

import (
	"context"
	"net"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"axonrouter/proxy/internal/netguard"
)

// The proxy-exemption half of the SSRF guard has to answer one question: is
// the address the transport is about to dial the operator's own egress proxy?
//
// The guard sees a resolved "ip:port" — Go resolves the name before the
// socket hook runs — so a proxy the operator named by hostname
// ("hatch-egress-proxy:3128", resolving inside 198.18.0.0/15) never matched
// the string allowlist and every upstream call died with
//
//	proxyconnect tcp: dial tcp 198.19.0.1:3128: request to private/reserved
//	IP blocked by SSRF protection ... (strictProxy=true)
//
// The answer is therefore computed by resolution: the configured proxy names
// are looked up and their addresses compared against the dial target. The
// exemption stays the proxy itself, never its reserved range, so a dial to
// another port on the same address — or to another host that happens to share
// the address — is still refused.

// matchesResolved decides a hostname-named proxy by resolution. Every name the
// allowlist remembers that is not already a literal IP is resolved (shared per
// name via resolveProxyIPs) and compared against the dial address.
func (a *proxyAllowlist) matchesResolved(ctx context.Context, address, host string) bool {
	ip := netguard.ParseIP(host)
	if ip == nil {
		return false
	}
	_, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		port = ""
	}
	a.mu.RLock()
	names := make([]string, 0, len(a.hosts))
	for h := range a.hosts {
		if netguard.ParseIP(h) == nil {
			// A bare remembered host can own any port; a host:port entry is
			// compared on its own port. The DNS answer is for the bare
			// hostname — a resolver has no notion of ports.
			name, p, err := net.SplitHostPort(h)
			if err != nil {
				name = h
				p = ""
			}
			if p == "" || p == port {
				names = append(names, name)
			}
		}
	}
	a.mu.RUnlock()
	for _, name := range names {
		for _, resolved := range resolveProxyIPs(ctx, name) {
			if resolved.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// proxyResolutionTTL bounds how long a proxy name's resolved addresses stay
// trusted. Podman/compose networks reassign container IPs on recreate, so a
// stale answer must not exempt a stranger forever.
const proxyResolutionTTL = 5 * time.Minute

type proxyResolution struct {
	ips     []net.IP
	expires time.Time
}

var (
	resolverMu       sync.RWMutex
	resolverCache    = map[string]proxyResolution{}
	resolverFailures = map[string]proxyFailure{}
	resolverInFlight singleflight.Group

	// lookupIPAddr is net.DefaultResolver.LookupIPAddr bound to the default
	// resolver, indirect so a test can supply answers without touching the
	// process resolver (the same seam pattern as proxyFromEnvironment).
	lookupIPAddr = func(ctx context.Context, name string) ([]net.IPAddr, error) {
		return net.DefaultResolver.LookupIPAddr(ctx, name)
	}
)

// proxyFailure remembers a failed resolution so the next dial does not pay the
// lookup again before proxyResolutionFailureTTL passes.
type proxyFailure struct {
	expires time.Time
}

// proxyResolutionFailureTTL bounds how long a failed resolution is remembered.
// It is deliberately shorter than proxyResolutionTTL: a proxy that was briefly
// unresolvable (a container still starting) must start answering as soon as it
// is up, not wait a full success TTL.
const proxyResolutionFailureTTL = 30 * time.Second

// resolveProxyIPs answers with the addresses a proxy name currently resolves
// to, cached for proxyResolutionTTL and collapsed across concurrent dials by
// singleflight so a burst of proxyconnects shares one lookup.
//
// The failure is cached here rather than left to singleflight: a group keeps a
// failed result only for its in-flight window, so a name that is briefly
// unresolvable at boot would be retried on every dial. Caching the error
// ourselves bounds that retry cost while still letting a transient failure
// recover at proxyResolutionFailureTTL.
//
// The lookup runs detached from the caller's context. A shared flight
// inherits its FIRST caller's context, and this resolver serves every dial
// through the same proxy: one client hanging up mid-lookup would cancel the
// lookup for all concurrent waiters and, worse, cache the cancellation as a
// failure — refusing every proxied dial for the failure TTL and
// re-introducing the symptom from an unrelated aborted request. The lookup
// itself is still bounded by the stdlib resolver's per-attempt timeouts; a
// hung answer recovers at the success/failure TTLs.
func resolveProxyIPs(ctx context.Context, name string) []net.IP {
	resolverMu.RLock()
	hit, ok := resolverCache[name]
	failed, failing := resolverFailures[name]
	resolverMu.RUnlock()
	now := time.Now()
	if ok && now.Before(hit.expires) {
		return hit.ips
	}
	if failing && now.Before(failed.expires) {
		return nil
	}
	v, _, _ := resolverInFlight.Do(name, func() (any, error) {
		addrs, err := lookupIPAddr(context.WithoutCancel(ctx), name)
		now := time.Now()
		if err != nil {
			resolverMu.Lock()
			resolverFailures[name] = proxyFailure{expires: now.Add(proxyResolutionFailureTTL)}
			resolverMu.Unlock()
			return nil, err
		}
		ips := make([]net.IP, 0, len(addrs))
		for _, addr := range addrs {
			ips = append(ips, addr.IP)
		}
		resolverMu.Lock()
		resolverCache[name] = proxyResolution{ips: ips, expires: now.Add(proxyResolutionTTL)}
		delete(resolverFailures, name)
		resolverMu.Unlock()
		return ips, nil
	})
	ips, _ := v.([]net.IP)
	return ips
}
