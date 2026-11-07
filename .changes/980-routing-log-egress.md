# 980 — Egress-consistency auditing in the routing log

## Problem

The per-request lines that land in `logs/routing.log` already named
the connection (`conn=…`) and the egress path's label (`egress=…`, a
pool name or "direct") — but never the address the request actually
left from. For a rotating pool the label is stable while the address
changes underneath it, and a provider that rate-limits or blocks by IP
cannot be diagnosed from a label. An account whose path silently moved
to a different egress mid-day — exactly the drift the anti-suspend work
hunts — was invisible in the log.

## What was built

- `internal/handlers/chat/egress_ip.go` — the tracker. Each chat-lane
  log entry for a connection now also carries:
  - `egress_ip` — the last-known egress IPv4 of the connection's path,
    discovered by an echo lookup THROUGH the connection's own client
    (`getClientForConnection`: same pool/proxy/relay/direct transport
    the request itself used), against the same echo targets the
    dashboard's exit-node probe uses (v4.ident.me,
    whatismyip.akamai.com, ipv4.icanhazip.com).
  - `egress_changed` — `old->new`, present only when this connection's
    path IP differs from the one its previous entry carried.
- Freshness without blocking: lookups are cached per path for 10
  minutes and refreshed in a background goroutine (single-flight per
  path; a failed lookup keeps the last-known IP and retries after 1
  minute). The request path only reads the cached value — it never
  waits on an echo. A path's first-ever entry therefore carries no
  `egress_ip` yet; the value appears from the next entry on.
- Path identity (the cache key) is credential-free: the resolved
  pool's id (`pool:<id>`), a relay's host (`relay:<host>`), a short
  SHA-256 prefix of a legacy proxy target (`proxy:<hash>` — a proxy
  URL can carry credentials and is never used raw), or `direct`.
- Both caches are in-memory only (deliberate): a restart forgets the
  previous per-connection IPs, so the first entry after a boot carries
  no `egress_changed` marker — there is nothing honest to compare
  against.
- Hook points (`internal/handlers/chat/fallback.go`): the success
  usage record and both failure records (upstream failure and the
  proxy-blocked early return) — the same entries that already carried
  `egress=`. The snapshot is taken once per attempt, so sibling lines
  of one attempt agree and a change is marked once, on the attempt
  that observed it. Emission is via `UsageLogInfo.ConnIdentityKV`
  (`internal/handlers/shared/types.go`: new `EgressIP` /
  `EgressChanged` fields), which the `[usage]` line and the fallback
  failure lines already print. Scope: the chat lane — the media lanes
  build their own usage records and do not carry the fields.

## Verification

- Temporary in-package probe (stub transports + a local stub HTTP
  proxy; deleted after the run, suite not run): PASS —
  - direct path: first fill returns immediately with no IP (proves the
    request path never blocks on the echo); after the background
    refresh lands, fills carry `egress_ip=203.0.113.7` with no marker;
  - forced change: stub answer flipped and TTL lapsed → the next fill
    carries `egress_ip=203.0.113.9` and
    `egress_changed=203.0.113.7->203.0.113.9`; `ConnIdentityKV` emits
    both keys;
  - proxied path: echo travelled through the connection's own proxy
    (stub proxy answering every absolute-URI GET), path key is
    `proxy:<hash>`, never the raw URL.
- Boot smoke (staging PG, scratch DB, dropped after): gateway boots
  on the new binary, `/api/version` answers, and
  `<DATA_DIR>/logs/routing.log` is created and written.
- `go build ./...` clean; `go vet` clean on internal/handlers/chat and
  internal/handlers/shared.
