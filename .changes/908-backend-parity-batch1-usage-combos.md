# Backend API parity — Batch 1: usage reads & combos actions

First batch of the AxonRouter backend-parity program (see
~/workspace/axon-backend-parity-map.md). Twelve endpoints AxonRouter
exposes that 9router-go lacked, implemented against its live contracts:

**Usage reads** (registered in both router blocks, package-level handlers
in the usage family's established style, Axon's flat `{"error": "…"}`
bodies): `/api/usage/active-requests` (tracker state + the recent-rows
fold), `/api/usage/analytics` (see the contract note below),
`/api/usage/analytics/failures` (window-function aggregation over the
request-details ledger), `/api/usage/history` (Axon's getUsageStats
payload, incl. its distinct 10-bucket last10Minutes), `/api/usage/logs`
and `/api/usage/request-logs` (usageHistory projected into Axon's log-row
shape), `/api/usage/quotas` (connections carrying a stored quotas
snapshot, binding-window remainingPct/resetAt).

**Combos & actions**: `/api/combos/hidden` (GET/POST/DELETE; kv scope
`hiddenCombos`) — and `/v1/models` now honours the hidden list, as Axon's
models route does; `/api/combos/reorder` (order persisted as the ordered
id list in kv scope `comboOrder`; `GetCombos` applies it — the combos
table has no sortOrder column); `/api/combos/test` (real health-check:
expands combo leaves and probes each through the gateway's own
/v1/chat/completions with Axon's test body, headers, 20s timeout,
concurrency 4); `/api/usage/request-details/{id}/replay` (preview +
execute + comparison from the stored payload); `/api/usage/{connectionId}/
codex-reset-credits` (GET/POST; an Axon-exact port in internal/codexquota
alongside the existing OmniRoute-shaped endpoints, different contract).

**Contract rule established by this batch**: AxonRouter's server mounts
`route.js`, not `route.ts` — where the pair diverges, **route.js is the
contract**. `/api/usage/analytics` was first built from the .ts file and
rewritten to the live .js contract (getAnalyticsSummary: timeFrom/timeTo/
timeBucket/errorCategory filters with verbatim validation messages, true
PERCENTILE_CONT percentiles, DATE_BIN timeline). Later batches check both
files per route.

Recorded gaps (honest subsets, no fabricated data): analytics events are
per-request from the request-details ledger — Axon's attempt-level
`analytics_events` stream has no Go counterpart; replay re-sends the
stored conversation (messages were all the ledger ever recorded), not the
byte-exact original body; codex-reset-credits has no OAuth pre-refresh
retry (the dashboard package holds no token refresher); quotas'
`rawDosage`/`lockedAllUntil` are null (not recorded); combos/test sends
Axon's no-cache header but the chat layer does not honour one yet.

Verification: `go build ./...` green; live smoke against a scratch
DATA_DIR binary — all seven usage reads returned contract-shaped payloads
with hand-checked aggregates (analytics p50/p95 interpolation verified),
hidden/reorder round-tripped through the real API, /v1/models listed the
visible combo and omitted the hidden one, combos/test executed a real
self-call and reported the upstream 502 in Axon's result shape, replay
and codex endpoints returned their exact 404/400 contracts. No test
suites were run.
