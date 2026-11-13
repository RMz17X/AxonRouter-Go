title: SSRF guard: exempt a hostname-named egress proxy
version: v1.10.47

## Fixed
- A proxy configured as `connectionProxyUrl` with a hostname endpoint
  (`hatch-egress-proxy:3128`, resolving inside 198.18.0.0/15) was always
  refused by the socket-level SSRF guard: the allowlist matched only
  literal addresses, while the guard sees the resolved IP the transport
  actually dials. Every upstream call through such a proxy died with
  `proxyconnect tcp: dial tcp 198.19.0.1:3128: request to private/reserved
  IP blocked by SSRF protection`.
- The configured proxy names are now resolved and their answers compared
  against the dial address. The DNS answer is cached behind
  `golang.org/x/sync/singleflight` with a 5-minute TTL for answers and a
  30-second TTL for failures, so the hot path pays no lookup and a proxy
  that is briefly unresolvable recovers quickly.
- The exemption stays the proxy itself, never its reserved range: another
  port on the same address, another host that shares it, and a proxy that
  resolves elsewhere all stay refused, as do the reserved ranges as
  request targets.
- The shared lookup runs detached from caller cancellation
  (`context.WithoutCancel`): a singleflight flight inherits its first
  caller's context, so one unrelated client hangup would otherwise cancel
  the lookup for every waiter and cache the cancellation as a failure —
  refusing every proxied dial for the failure TTL.
