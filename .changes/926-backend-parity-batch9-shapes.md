### Backend parity batch 9 — deferred shape fixes (safe subset)

Contract: `~/workspace/9router-go-batch9-worklist.md` (the audit; Axon's
live `route.js` wins over `route.ts` throughout). Scope was the audit's
Part 3 order: translator step 2 (item 5a), the `/v1/audio/voices`
engine wrapper (item 1), `/v1/models/{kind}` granularity (item 3),
then the G1 error-envelope sweep over the audit's E/D suspicion list.
Owner boundaries held: no executor or chat-core restructuring; the
dashboard SPA's own contracts are ground truth for shared handlers
(every change below names its SPA-consumer check). OWNER-DECISION
items were NOT touched — restated at the bottom.

#### Item 5a — translator step 2 completed (POST /api/translator/translate)

- `openai-responses` bodies now convert through the chat layer's
  existing `translator.ResponsesToChatRequest` (pure wiring).
- New `internal/translator/gemini_request.go`: faithful ports of
  Axon's `gemini-to-openai.js` and the dedicated
  `antigravity-to-openai.js` (max-token adjustment 64000/32000,
  thinking-budget → reasoning_effort bands, functionResponse →
  tool-message conversion, antigravity thought parts →
  reasoning_content, schema normalization). DEVIATION from the audit
  text: the audit described antigravity as "unwrap + the gemini
  conversion"; Axon's registry registers a dedicated antigravity
  translator with observably different output, so the dedicated one
  was ported (Axon source wins).
- Verified live: all three sources return `{success:true,
  result:{body}}` (previously the `{success:false}` 500 envelope);
  antigravity detection needs the `antigravity` User-Agent, Axon's
  own heuristic; the pre-existing claude path is byte-unchanged.

#### Item 1 — GET /v1/audio/voices engine wrapper

- New `internal/handlers/media/batch9_voices_v1.go`
  (`HandleAudioVoicesV1`), registered ONLY on the engine spelling.
  Axon contract: `provider` required, whitelist exactly
  {elevenlabs, deepgram, inworld, edge-tts, local-device} — missing
  or other → 400 `{error:{message:"provider must be one of:
  elevenlabs, deepgram, inworld, edge-tts, local-device",
  type:"invalid_request_error"}}`; success `{object:"list",
  data:[{id, name, lang, gender, model:"<alias>/<id>"}]}` (flat
  `data.voices` with `?lang=`, flattened `byLang` groups without);
  `Access-Control-Allow-Origin: *`. Reuses the in-package fetchers
  of the SPA handler; a fetch failure answers 502 `server_error`
  (the fetchers carry no typed upstream status; the audit prescribes
  502).
- The shared SPA handler and all `/api/media-providers/tts/*`
  registrations are byte-identical. SPA evidence: the internal
  shape is consumed only via those routes (`web/src/lib/
  ttsProviders.ts`, `TtsExampleCard.svelte`); nothing calls the
  engine spelling.
- Verified live: both whitelist 400s verbatim; edge-tts →
  322 voices in the Axon row shape, CORS header present; `?lang=id`
  → exactly the 2 id voices; the SPA spelling still returns the
  internal `{byLang}` shape.

#### Item 3 — GET /v1/models/{kind} granularity

- Kind seeding: the 7 Cloudflare task specs of the batch-6 task
  adapter verified `kind:"task"` against Axon's registry
  (`open-sse/providers/registry/cloudflare-ai.js` — the audit named
  providerModels.js; the registry file is the actual source) and
  seeded into `ProviderModelKinds` under both `cf` and
  `cloudflare-ai`. SCOPE NOTE: kinds only — the 7 ids are not in
  Go's `ProviderModels` cf lists (a pre-existing subset gap, 25
  vs Axon's ~57 ids), so they surface in kind listings via a
  connection's enabled/custom models, as the live smoke shows.
- New `internal/handlers/chat/models_kinds.go` +
  `buildModelsListForKinds`: Axon's exact slug map
  {image, tts, stt, embedding, image-to-text→[imageToText],
  web→[webSearch, webFetch], task}; connection-scoped (no catalog
  dump, no noAuth appendage); combos first with Axon's
  comboMatchesKinds + hasActiveTarget gate; per-model kind
  resolution custom → registry → id heuristic; token keys only on
  llm-serving entries; per-connection web lane entries
  `{id:"<alias>/search"|"/fetch", object, owned_by, kind}`.
  Kind-mode entries now stamp their resolved `kind` (the smoke
  caught its absence on plain model entries; fixed, legacy lists
  verified to carry no `kind` key).
- `HandleModelsByKind` rewritten on that builder; a non-slug
  single segment falls through to the shared model lookup with
  Axon's 404 `{error:{message:"The model '<id>' does not exist or
  you do not have access to it.", type:"invalid_request_error",
  code:"model_not_found"}}`. Deleted Go-only behaviors:
  provider-granular listings, `systemone`/`rerank` slugs,
  image-to-text match-all, `created`/`endpoint` keys; the dead
  helpers in access.go went with them.
- Two Axon details deliberately NOT carried (recorded): the
  builder-level `providerMatchesKinds` gate (Go's registry
  `ServiceKinds` is an incomplete port — cloudflare-ai carries
  ["llm","image","systemone"] vs Axon's six kinds — gating on it
  would wrongly suppress the cf task listing; per-model
  resolution is the operative filter) and the `comboOnlyModels`
  setting (no Go counterpart anywhere; Go's builders ignore it
  on every listing).
- Verified live: all 7 slugs answer `{object:"list", data}` on
  both mounts; `llm` is not an Axon slug and 404s as a lookup,
  matching Axon; a seeded cf task custom model appears under
  `/v1/models/task` with `kind:"task"` and stays out of the LLM
  list; a non-slug segment returns Axon's 404 shape verbatim.
- KNOWN STALE TESTS (not edited, per the no-test-edits rule):
  `internal/handlers/media/extras_endpoints_test.go`
  `TestHandleModelsByKind` / `_tts` assert the old
  provider-granular behavior and will fail if the suite runs;
  signatures are unchanged so the tree compiles.

#### G1 — error-envelope sweep (Axon dashboard errors are flat `{error:"msg"}`)

Method: every candidate verified against Axon's route source
before changing; the engine envelope (`handlerutil.WriteJSONError`,
nested `{error:{message,type,code}}`) is correct on `/v1` and was
never touched there. The dashboard package's existing
`writePlainError` was reused; a sibling `writeDashboardError` was
added for the root handlers package.

| Item | Route(s) | Verdict |
| --- | --- | --- |
| D1 | /api/keys GET/POST + rotate/reset-quota/delete/toggle | CHANGED: all errors flat; POST empty name → 400 "Name is required"; POST success → 201 `{key, name, id, machineId}` (was 200 `{status,id,key}`). GET success stays a BARE ARRAY — SPA-locked (`getApiKeys: request<APIKey[]>`, client.ts:1534) against Axon's `{keys}`. machineId echoes the stored request value; Axon derives it server-side — shape matches, derivation differs (recorded). Sibling files apikeys_models/apikeys_policy are Go-only routes — not-a-diff. |
| D2 | /api/settings GET/PATCH, database import | CHANGED: errors flat via writePlainError; protected keys (`password`, `mitmSudoEncrypted`) silently stripped per Axon's PROTECTED_SETTING_KEYS (was 400); stored-hash password change without currentPassword → 400 "Current password required", wrong current → 401 flat; GET gains env-backed `enableRequestLogs`/`enableTranslator`; no-store headers on GET + PATCH success. Not-a-diff: Axon's jevApiKeys redaction is a no-op in Go's data model (Go never stores those keys). |
| D3 | /api/version, /api/version/update | GET not-a-diff (Go's payload is a superset the SPA type matches). CHANGED: update/check failure sites → `{success:false, message}` at the same statuses (Axon's POST failure shape). Live trigger not probed (it performs a real self-update); shape source-verified at both sites. |
| D4 | /api/auth/login | CHANGED: malformed JSON → 400 "Malformed JSON body"; empty password → 400 "Password is required" (both flat + no-store, Axon's order). The rest of the handler was already a faithful flat port (429 lockout, remainingBeforeLock, tunnel/SSO 403s). Recorded: RESET_HINT names the 9router-go CLI (deliberate product naming); no CSRF cookie (different session machinery). |
| D5 | /api/auth/logout, /api/auth/status | Already-flat / not-a-diff. Status payload matches Axon except the OIDC keys (absent per the owner OIDC removal) and ssoType default "saml" (removal fallout). |
| D6 | /api/usage/stats | CHANGED: the handler's four 500 sites → flat "Failed to fetch usage stats". NOT changed: period validation — Go's resolver deliberately accepts custom `<n>d`/`<n>h` windows and falls back instead of Axon's 400 (SPA-locked superset; `PeriodSelect.svelte` documents the custom windows; the SPA sends Go's `views` param, not Axon's `include`). Live merge (activeRequests/recentRequests/errorProvider) already present. Empty-period default differs ("today" vs Axon's "7d"; the SPA always sends a period) — recorded. |
| D7 | /api/usage/chart | Not-a-diff: the handler never errors (degrades to empty buckets); its period superset is the same SPA-locked decision as D6. |
| D8 | /api/usage/providers, /stream, /request-details | Providers: envelope already Axon-exact; 500 flattened to "Failed to fetch providers"; data source differs (Go: configured connections; Axon: providers seen in request history) — recorded, out of envelope scope. Stream: SSE `data:` frame shape matches; added Axon's `retry: 3000` preamble; the unreachable nested 500 flattened. Request-details: list params are SPA-locked (`limit`/`offset`, client.ts:2155) vs Axon's page/pageSize; list 500 → flat "Failed to fetch request details"; single `{detail}` envelope already matched; its errors flattened, 404 aligned to Axon's "Request detail not found". Recorded divergence: Go's single-detail returns the full stored payload (its documented purpose; the SPA modal consumes it) where Axon redacts by default. |
| D9 | /api/combos/analytics | CHANGED: 500 → Axon's `{error:"Failed to fetch combo analytics", details}`; success envelope completed with `period` (echo, "today" default) and `routingMetrics: null` (Go has no routing-metrics service; null is also Axon's own failure value). SPA reads only `members` (client.ts:672/2164). |
| D10 | /api/combos GET/POST/PUT/DELETE + auto-free | CHANGED: GET → `{combos}` (SPA tolerates both: `Array.isArray(res) ? res : res.combos`); POST → Axon's validations (400 "Name is required", charset 400, duplicate-name 400 via GetComboByName) and 201 with the created combo object (SPA call sites ignore the body); parse/body failures → Axon's catch-all 500 "Failed to create combo"; PUT errors flattened, 404 → "Combo not found", success body kept (SPA-typed, ignored); DELETE errors flattened, success → `{success:true}`; auto-free (Go-only route) errors flattened, statuses/messages kept. Recorded: Go's Combo.Models serializes as a JSON string where Axon's is an array — pre-existing across the whole combos surface, SPA-normalized, not changed here. New server-side name validation mirrors Axon's own create modal rules; media views that create combos programmatically now get Axon's 400 for names Axon would also reject. |
| D11 | /api/models/custom, /disabled, /alias | CHANGED: custom GET 500 → "Failed to fetch custom models"; POST parse failures → Axon's catch-all 500 "Failed to add custom model", missing key/id → 400 "providerAlias and id required", success → `{success:true, added}` (the stored entry; SPA types `{success}`); DELETE → 500 "Failed to delete custom model", success gains `success:true`. Disabled GET → Axon envelopes `{disabled}` / `{ids}` with `?providerAlias=` (SPA's parseDisabledModelsMap accepts both shapes; parseDisabledIds documents `{ids}`); PUT parse failures → 500 "Failed to disable models", success gains `success:true`. Alias GET 500 → "Failed to fetch aliases"; PUT parse → 500 "Failed to update alias", required → 400 "Model and alias required" (Axon capitalization), success was already Axon-exact; DELETE required → "Alias required", 500 → "Failed to delete alias". The Go-only batch form (models_bulk.go) and provider-models delete were flattened with Go messages kept. |
| D12 | /api/models/test | Mostly not-a-diff: result keys `{ok, latencyMs, status, error}` match Axon's ping result (Go adds `note`/`blocked`/`resetAt`, all SPA-typed); CHANGED: missing-model 400 → Axon's bare `{error:"Model required"}`. Recorded gaps: Axon accepts `connectionId` to pin the probe — Go's request type lacks it and the SPA never sends it (client.ts:1819), not wired; Go rejects unknown kinds with 400 where Axon falls through to the chat probe — deliberate Go guard, kept. |
| D13 | /api/tunnel/status | RECORD ONLY — not touched, parent scope question: the route is still registered (router.go ×2, dashboard/routes.go) and serves the tunnel settings state, while its ngrok backend was deleted in the owner removals (.changes/920) and Axon's route answers `{ngrok}`. Whether Go's route is a live tunnel surface or a leftover is the parent's call. |
| D14 | /api/metrics | Not-a-diff for the envelope question: content type matches exactly (promhttp's `text/plain; version=0.0.4; charset=utf-8` = Axon's header). Recorded divergences, not swept: series naming is Go's own vocabulary (`router_*` vs Axon's `axonrouter_*`, and the sets do not correspond); Go mounts the endpoint behind dashboard auth deliberately (handler.go documents why) where Axon is public with CORS/OPTIONS/HEAD handlers. |
| D15 | /api/providers/client | Already Axon-shaped (batch 2 evidently covered it): `{connections, providerOptions, pagination, totals}` all match; the one nested 500 → flat "Failed to fetch providers". Axon's `statusCounts` key is absent in Go and has no SPA consumer (zero references in web/src) — recorded, not added. |
| D16 | /api/cli-tools/hermes-profiles (+ hermes-settings siblings) | CHANGED (envelope only): the success shape `{profiles}` already matched; the four WriteJSONError sites in hermes_settings.go carried Axon-verbatim messages in the nested envelope — all four now flat, messages unchanged. |
| E1 | /v1/models envelope | CHANGED, spelling-selective: engine spellings (/v1/models, /api/v1/models, /models, /v1) now answer exactly `{object:"list", data}` (new streaming writer `WriteModelsListExact`; the old meta+data+models writer is removed — its only caller was this handler, no tests reference it); /api/models answers Axon's own dashboard shape `{models}`; Axon's `?provider=` post-filter implemented for the engine spellings (owned_by equals, or id starts with `<provider>/`). SPA check: no web/src consumer fetches /api/models or reads `mode`/`connections` from any spelling (model data comes from /api/models/meta + catalog). NOT adopted: Axon's `Cache-Control: public, max-age=60` + `x-axonrouter-cache` — this list is filtered per API key, so a shared cache must never store it (recorded deviation). |
| E2 | CORS on /v1 | PARTIALLY FIXED: `Access-Control-Allow-Origin: *` now on the models list (engine spellings) and /v1/audio/voices, the two surfaces Axon documents per-route. NOT done: global preflight (OPTIONS) support — Axon exports per-route OPTIONS handlers; Go's engine mounts go through the RequestLogger alias fold, so a correctly-scoped preflight middleware is a separate deliberate change, not a sweep edit. Recorded for the parent. |
| E3 | model lookup entry keys | Verified live on the connected install: the lookup returns the builder entry verbatim (capabilities, context_length, max_completion_tokens, pricing) — internally consistent with /v1/models. `pricing` is a standing Go addition also present on list entries; otherwise not-a-diff. |

#### OWNER-DECISION items — untouched, restated

- Item 2, models/info success-shape reshape: not implemented.
- Item 4, videos subsystem (`/v1/videos/{id}` polling + `/content`):
  not implemented (audit premise correction stands: Axon has no
  job store either — it live-proxies upstream).
- Item 5b, translator step 3 (provider-request construction):
  not implemented; the route still answers Axon's
  `{success:false, error}` 500 shape naming the deferral.

#### Verification (direct checks only; no test suites run)

- `~/bin/heavy ~/go-sdk/bin/go build ./...` and `go vet ./...`
  green on the final tree; gofmt clean on every touched file.
- Web: `bun run build` green; `svelte-check --tsconfig
  tsconfig.app.json` = 77 errors / 57 warnings — exactly the
  pinned baseline, no regression (no web files were changed).
- Live smoke (scratch binary + DATA_DIR under ~/tmp-smoke/,
  requireLogin flipped off, mock upstream +
  ALLOW_PRIVATE_NETWORKING=true, API key created in-script and
  never printed): translator step 2 for openai-responses, gemini
  and antigravity bodies → `{success:true, result:{body}}` with
  Axon-shaped conversions, claude path unchanged; voices
  whitelist 400s verbatim, engine list shape + `?lang=` filter,
  SPA shape unchanged; all 7 kind slugs + non-slug 404 + seeded
  task model end-to-end; chat round-trip non-stream and stream
  → 200 through the mock (core path untouched); G1 probes live:
  keys POST 201 + flat errors, login 400s flat, settings
  protected-key strip + no-store + env keys, combos 201/400s/
  `{combos}`/`{success:true}`, analytics envelope, disabled
  `{disabled}`/`{ids}`, alias capitalization, models/test bare
  400, request-detail flat 404, usage stream `retry: 3000`
  preamble, E1 envelopes + `?provider=` filter (9 ds models /
  empty for a non-matching provider).
