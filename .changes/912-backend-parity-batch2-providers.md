# Backend API parity — Batch 2: providers family

Second batch of the AxonRouter backend-parity program (see
~/workspace/axon-backend-parity-map.md), executed routes-first per
Ricki's 2026-11-04 sequencing flip. All 25 routes the mechanical diff
found missing in the providers family, implemented against Axon's
live contracts (route.js first; route.ts where it is the only source)
in 21 new files under internal/handlers/dashboard/ + registrations in
internal/handlers/router.go:

**Catalogue & pricing**: `/api/provider-models` (system aggregate +
synced models; synced store in kv scope `syncedAvailableModels`),
`/api/models/availability` (account/model locks with clearCooldown
semantics), `/api/pricing` (GET/PATCH/DELETE; Axon's PROVIDER_PRICING
defaults verbatim — gh + tokenrouter, 113 models; user overrides in
kv scope `pricing`, validation messages verbatim).

**Stats & admin**: `/api/providers/stats`, `{id}/proxy-stats`,
`{id}/reset-status`, `{id}/unlock-model`, `bulk-reset-status`,
`bulk-proxy` — real classifications and flattened, secret-stripped
connection shapes; unknown ids give Axon's 404 bodies.

**Testing & discovery**: `{id}/test-model`, `{id}/test-models`,
`test-batch` (real gateway self-call pings; verdicts persisted),
`{id}/scan-free-models` (JSON + SSE probe/classify),
`{id}/sync-models`, `noauth-sync` (opencode synced live in smoke),
`kilo/free-models` (live fetch + 1 h cache).

**Auto-switch & instructions** (route.ts contracts):
`{antigravity,codex}/auto-switch` + `/active` + `/rotate` — settings,
threshold clamping, rotation across seeded OAuth accounts, and the
host token files Axon writes; `{codex,commandcode,morph}/instructions`
— verbatim defaults, custom content persisted under DATA_DIR.

Verification (direct checks, no test suites): central
`go build ./...` green; the implementing agent's seeded live smoke
probed every route (auto-switch rotation A→B read back, pricing
round-trips, scan SSE event sequence, instructions 400 on oversize
content verbatim); the integrator re-probed pricing, provider-models,
providers/stats, codex instructions, and models/availability on a
fresh scratch server — all contract-shaped.

Partial / deviations (nothing stubbed): proxy-stats custom proxy
groups await the proxy-infra batch (Axon's no-group branch serves
meanwhile); scan-free-models' dedicated-free test lacks the registry's
trial-key fields (Category==free || NoAuth stands in); sync-models
coverage is bounded by the existing writeConnectionModels provider
set, whose 400s relay exactly as Axon relays its inner route; the LLM
ping honours Axon's pin headers at send time but the Go chat entry
routes by model string rather than hard-pinning the connection;
instructions settings have no runtime consumer in the Go gateway yet
(the routes manage settings + files with the exact contract);
auto-switch writes host token files as Axon does — in tension with
the locked Docker-only direction, flagged for the P5/P7a pass.
