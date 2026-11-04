# Backend API parity — Batch 4: proxy infra family

Fourth batch of the AxonRouter backend-parity program (see
~/workspace/axon-backend-parity-map.md), routes-first per Ricki's
2026-11-04 sequencing flip. All 17 routes the mechanical diff found
missing in the proxy-infrastructure family, implemented against Axon's
live contracts (route.js wins over route.ts; remove-all, circuit
breakers, routing latency and the tunnel are .ts-only contracts):

**Proxy groups** (new store `proxyGroups` table + repo, Axon's
proxyGroupsRepo shape): `GET|POST /api/proxy-groups` (four built-in
default groups merged with customs; default sticky settings persist in
`settings.defaultProxyGroupSettings`; reserved-name 400, duplicate 409),
`GET|PUT|DELETE /api/proxy-groups/{id}` (PUT on a default id writes only
its sticky settings; DELETE counts connections bound via
`providerSpecificData.proxyGroup` and refuses with 409 + boundCount),
`POST /api/proxy-groups/{id}/remove-all` (clears
`providerSpecificData.proxyGroupId` from connections and matching
`settings.providerProxyDefaults` entries), `POST
/api/proxy-groups/{id}/sync` and `POST
/api/proxy-groups/verify-subscription` (subscription fetch + proxy-list
parser ported from proxyAutoFetcher: scheme URLs, user:pass@host,
host:port:user:pass, host:port; reconcile retains/inserts/deletes pools
labelled with the group id, cap 5000; verify actively probes up to 12
parsed proxies against minRequired).

**Pool fitness + geo** (new `internal/proxy/poolstate` registry, port of
proxyPoolFitness.js + poolGeo.js; marks persist under the settings key
`proxyPoolFitness`, the same key upstream uses): `GET
/api/proxy-pools/fitness`, `POST /api/proxy-pools/fitness/clear-all`,
`POST /api/proxy-pools/{id}/fitness/clear`, `POST
/api/proxy-pools/{id}/geo-probe` (probe chain ipwho.is → ip-api →
ipapi.co → ipinfo through the pool's own proxy, failure kinds
rate-limit/server/no-ip/timeout/network, HTTP 200 on probe failure —
upstream parity, including the verbatim `no proxy url` network detail).

**Health + recovery**: `GET|POST /api/proxy-pools/health-check`
(incremental sweep port: stride selection, testStatus/lastTestedAt/
lastError verdict writes, module-level lastHealthCheckAt/lastSummary)
and `POST /api/proxy-pools/recover` (proxyAutoRecovery port:
dead/unhealthy/degraded candidates, 30-minute per-pool cooldown,
`?force=true` bypass, computeProxyTestHealth verdicts with the
3-failure failover and 30-failure dead thresholds).

**Circuit breakers + routing latency + tunnel**: `GET|DELETE
/api/circuit-breakers` (route.ts shape over the gateway's live
`internal/resilience` registry; `BreakerStatus` gained `LastFailureAt`),
`GET /api/routing/latency` (new `internal/routinglatency` sliding-window
percentile tracker, fed by wrapping the four `/v1` POST handlers in
SetupRoutes), `POST /api/tunnel/ngrok/start` + `/stop`.

**Rider closed**: batch 2's recorded proxy-stats partial is finished —
`GET /api/providers/{id}/proxy-stats` group mode now resolves custom
groups by name, then id; a group with poolIds owns membership by id set
and contributes its isSticky/stickyLimit, with Axon's label-match
fallback otherwise.

Deviations, deliberate: the Go breaker registry is keyed per provider,
Axon's per connection, so `connectionId` carries the Go breaker key —
the registry the request path actually consults; Go's DEGRADED state
passes requests and therefore reports as `closed`; entries are sorted
by name. Routing-latency error samples are responses ≥ 500 (Axon marks
handler throws; a stream that fails after a 200 is counted as success).
The sweep's cross-process cache lock and zone-health recording are not
ported (single-process guard covers the route contract; zone health is
not exposed by it). Upstream's DELETE counts bindings via `proxyGroup`
while remove-all clears `proxyGroupId` — the inconsistency is mirrored
verbatim, not repaired. The ngrok port drives the operator's ngrok
agent CLI (no Go SDK equivalent exists here): no binary in PATH →
`installed: false`, phase `not_installed`, surfaced honestly rather
than as a fake success; the tunnel target is this gateway's own port
($PORT, default 20130) instead of Axon's hardcoded 12711. Docker-only
tension, flagged like the host-file reads in earlier batches: a tunnel
is a host process by nature — inside a container it manages the
container's agent and port, useful only with host networking. The
pre-existing dashboard pool test writes testStatus `passed`/`failed`;
batch 4's sweep/recovery write Axon's active/failed/degraded/
unhealthy/dead vocabulary and that older divergence is left untouched.

Verification: `go build ./...` green through the heavy gate, go vet
clean on all touched packages, gofmt clean on touched files (the
pre-existing unformatted files were not touched), all five
CROSS_TARGETS build with CGO_ENABLED=0 (go.mod change is only
golang.org/x/net moving to a direct require, per AGENTS.md §4.F), no
test suites run (standing rule). Live seeded smoke against a scratch
DATA_DIR server — 44/44 checks: groups CRUD incl. reserved-name 400,
case-insensitive duplicate 409, default-group sticky PUT persisted via
settings, bound-delete 409, remove-all clearing both connection refs
and provider defaults; sync + verify-subscription against a local list
server (initial sync on create, idempotent re-sync, no-fetchUrl 400);
fitness rows seeded via the settings key, scope clear, provider
clear-all, wildcard model → null; geo-probe dead-proxy failure AND a
live success through a local egress proxy (full 6-field geo), plus the
verbatim URL-less `no proxy url` failure; health-check initial null
state, sweep verdicts, recovery of a seeded unhealthy pool
(consecutiveFailures 5 → 6, status stays unhealthy); circuit-breakers
list/reset; routing latency empty shape + Cache-Control: no-store,
then a recorded sample after an API-key-authenticated /v1 call; ngrok
start/stop on the honest not_installed path (no ngrok binary on this
VM, so a real tunnel could not be exercised); proxy-stats rider proven
with a strategy pointing at the custom group (poolIds membership,
isSticky/stickyLimit from the group). The smoke caught one real bug,
fixed in this batch: `normalizePoolIDs` rejected the reconciler's
[]string, silently emptying a synced group's poolIds.
