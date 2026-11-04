# mimo-free provider removal (owner order)

Ricki, 2026-11-04: "mimo free hapus aja ya karena udah gak bisa" — the
free endpoint (api.xiaomimimo.com/api/free-ai/...) is dead upstream
(batch 2's noauth-sync already returned "No models returned" for it).
The provider is removed from the product entirely, as its own commit
after batch 3 landed (serialized tree work).

**Scope discipline — what did NOT die:** `xiaomi-mimo`, the separate
paid API-key provider (api.xiaomimimo.com/v1), is untouched: its
registry entry, OAuth authorize flow, batch-3 api-key/auto-import
routes, TTS support, and models all stay. OpenCode's `oc/mimo-*-free`
model entries are OpenCode's catalogue, not this provider — untouched.
The bare `mmf` catalog shell (a name-only CategoryAPIKey registry
entry with no executor config, no base URL, and no handler references)
also stays — but its one static model, `mimo-auto`, belonged to the
free provider and is removed from both model registries.

## Removed

- `internal/handlers/chat/mimofree.go` (274 lines: bootstrap/JWT
  session, anti-abuse system marker, dedicated chat executor) and its
  three test files (`mimofree_test.go`, `mimo_test.go`,
  `mimo_marker_test.go` — they tested only this feature's symbols).
- `internal/handlers/chat/combo.go`: the `mimo-free` dispatch branch;
  combo forwarding is now the unconditional standard path.
- `internal/handlers/suggestedmodels.go`: `filterMimoFree` + its
  switch case.
- `internal/providers/provider_registry.go`: the `mimo-free` registry
  entry (NoAuth, hidden, models.dev fetcher).
- `internal/providers/providers.go`: the `mimo-free` executor config
  (free-ai base URL, `mimo-dynamic` default key).
- `internal/providers/aliases.go` (`mmf` → `mimo-free`),
  `registry_aliases.go` (`mimo-free` → `mmf`): alias pair removed.
- `internal/providers/registry_models.go`: `mimo-free: {mimo-auto}`
  and `mmf: {mimo-auto}` static model assignments.
- `internal/handlers/chat/models_list.go`: three comments that used
  mimo-free/mmf as the aliased-provider example now use
  opencode/oc or a generic hidden provider.
- Web: `web/src/lib/models.ts` alias entry (`mimo-free: mmf`) and the
  static `mmf: [mimo-auto]` model list; `providers.ts` alias-dedup
  comment rephrased generically; `providers.test.ts` display-link
  exemption set drops the two dead ids.

## Verification (direct checks; no test suites per standing rule)

- `go build ./...` green; `go vet` clean on providers, handlers,
  handlers/chat; gofmt clean on all touched files (one pre-existing
  upstream file, internal/handlers/dashboard/apikeys_policy.go from
  fork commit a832c429, is unformatted upstream and left alone).
- svelte-check: 77 errors / 57 warnings — exactly the pristine
  baseline, zero added. (Corrected 2026-11-04: the original draft of
  this fragment claimed 73/53 from a misread truncated output; the
  full output shows 77/57.)
- Live smoke (fresh DATA_DIR, requireLogin off, API key created
  in-script): GET /api/providers/catalog — no `mimo-free` entry,
  `xiaomi-mimo` present (154 providers); GET /v1/models — 1521
  models, zero `mimo-free/`, `mmf/`, or `mimo-auto` entries,
  xiaomi-mimo models intact; POST /v1/chat/completions with
  `mimo-free/mimo-auto` → 502 "no active connections for provider:
  mimo-free" (generic path — the dedicated free executor is gone).
