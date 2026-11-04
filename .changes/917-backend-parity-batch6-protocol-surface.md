# Backend parity batch 6 — protocol surface (v1, v1beta, protocols, translator, morph)

HIGH-RISK batch: this family is the gateway's core request path. Per the
batch brief, Phase A (verdict table) was completed and recorded BEFORE
implementation; Phase B is bounded by it. Axon contract sources:
`~/workspace/AxonRouter/src/app/api/{v1,v1beta,protocols,translator,morph}/**/route.(js|ts)`
(route.js wins over route.ts) plus the Axon server layer
(`src/server/webServer.mjs`: `/v1/*` → `/api/v1/*`, `/v1beta/*` →
`/api/v1beta/*` rewrites; `src/dashboardGuard.js` PUBLIC_PREFIXES:
`/v1`, `/v1beta`, `/api/v1`, `/api/v1beta`, `/codex`, `/responses`).

Go serving model (verified in `internal/handlers/router.go` +
`internal/middleware/logging.go`): engine routes are registered once at
relative paths in `SetupRoutes`; `RequestLogger` strips repeated leading
`/v1/` before routing, so relative registrations answer at the bare and
`/v1` spellings. Before this batch only `/api/v1/models(+/*)` existed at
the `/api/v1` spelling and nothing existed under `/v1beta` at all.

## Phase A — verdict table (43 unique Axon paths)

Alias note applying to every v1 row: before this batch, engine routes
answered at the `/v1` spelling only (models excepted); Axon answers both
`/v1/**` and `/api/v1/**`. Phase B adds the `/api/v1` + `/api/v1beta`
alias mounts wholesale (see "Alias mounts" below), so each EXISTS row
below is also ALIAS-ONLY → fixed, unless marked otherwise.

### v1 (26)

| Path | Verdict | Notes |
| --- | --- | --- |
| GET /v1 | MISSING → PORTED | Axon re-exports the models list GET. Go 404'd at both spellings. Registered to the models handler. |
| POST /v1/api/chat | EXISTS-SAME | Go `HandleOllamaChat` at relative `/api/chat`; alias-fixed for `/api/v1/api/chat`. |
| POST /v1/audio/speech | EXISTS-SAME | `HandleAudioSpeech`. |
| POST /v1/audio/transcriptions | EXISTS-SAME | `HandleAudioTranscriptions`. |
| GET /v1/audio/voices | EXISTS-SHAPE-DIFF (deferred) | Axon: `provider` required ∈ {elevenlabs, deepgram, inworld, edge-tts, local-device} (else 400 `{error:{message:"provider must be one of: …", type:"invalid_request_error"}}`), success `{object:"list", data:[{id,name,lang,gender,model:"<alias>/<id>"}]}` transformed from its internal voices API. Go: one shared handler returns the internal `{voices, languages, byLang}` shape, defaults provider to edge-tts, and also serves minimax/gemini. Deferred: the shared handler's shape is the dashboard SPA's internal contract (`/api/media-providers/tts/voices`), and the prefix strip erases the spelling before routing, so the `/v1` transform cannot be applied spelling-selectively without a core-path middleware marker. Parent ruling requested. |
| POST /v1/chat/completions | EXISTS-SAME | Core path; acceptance-proven after this batch (see below). |
| POST /v1/embeddings | EXISTS-SAME | `HandleEmbeddings`. |
| POST /v1/images/generations | EXISTS-SAME | `HandleImages`. |
| POST /v1/messages | EXISTS-SAME | `HandleMessages`. |
| POST /v1/messages/count_tokens | EXISTS-SAME (micro-fix applied) | Go's estimator is an explicit port of Axon's `estimateAnthropicInputTokens`. One delta fixed: invalid JSON — Axon 400 `{"error":"Invalid JSON body"}` (flat), Go answered 200 `{input_tokens:0}`. Now 400 with Axon's shape. |
| GET /v1/models | EXISTS-SAME | Both spellings already; same handler lineage and `{object:"list", data}` envelope. |
| GET /v1/models/info | EXISTS-SHAPE-DIFF (errors fixed; payload deferred) | Semantics match (`?id=` required, single-model metadata). Error shapes fixed to Axon's: 400 `{error:{message:"Missing required query param: id (e.g. ?id=openai/dall-e-3)", type:"invalid_request_error"}}`, 404 `{error:{message:"Model not found: <id>", type:"not_found"}}` (Go had flat `WriteJSONError` strings). The 404 needed more than the shape: `resolveModel` never rejects a slashed id (passthrough resolution for the chat path), so the handler now also gates on `findModelForLookup` — the existence oracle the model-lookup route already uses — leaving chat dispatch untouched; unknown ids 404, listed ids (combos included) answer as before. Success payload still differs — Axon `{id:"<alias>/<model>", name, kind, owned_by:<alias>, endpoint:<per-kind endpoint>, params?/capabilities?/options?/dimensions?}` vs Go `{id, object:"model", owned_by:<provider>, endpoint:"/v1/chat/completions" (always), context_length, context_window, max_completion_tokens, max_input_tokens, max_output_tokens, combo?…}` — and Axon honours a `kind` query param Go ignores. Deferred: reshaping needs the catalogue kind/name plumbing shared with the by-kind row below. |
| GET /v1/models/{kind}, /v1/models/{provider}/{model} | EXISTS-SHAPE-DIFF (deferred) | Single-model lookup matches, including Axon's 404 `{error:{message:"The model '<id>' does not exist or you do not have access to it.", type:"invalid_request_error", code:"model_not_found"}}`. Kind listings differ: Axon filters the model catalogue by kind (model-granular entries; slugs image, tts, stt, embedding, image-to-text, web, **task**). Go returns provider-granular entries for image/tts/stt/embedding/image-to-text (model-granular only for web) and does not map the `task` slug at all (falls through to lookup → 404). Deferred: rebuilding by-kind over the model catalogue changes an existing surface; parent ruling requested. |
| POST /v1/responses | EXISTS-SAME | `HandleResponses`. |
| POST /v1/responses/compact | EXISTS-SAME | `HandleResponsesCompact`. |
| POST /v1/search | EXISTS-SAME | `HandleSearch`. |
| POST /v1/systemone | EXISTS-SAME | `HandleSystemone`. |
| POST /v1/task | MISSING → PORTED | Axon's non-chat task lane; exactly one adapter exists upstream (Cloudflare Workers AI, 7 models with per-model field specs). Ported with connection rotation mirroring `markAccountUnavailable` fallback policy (no fallback on 400/404/413/422/499; rotate otherwise; cap 10 = MAX_FALLBACK_ATTEMPTS). |
| POST /v1/unified | MISSING → PORTED | Axon: validate (mode/model/per-mode required fields, `{error, code}` 400s) → self-dispatch to the modality target path → wrap JSON in `{mode, provider, model, usage, correlation_id, output, data}` with `x-correlation-id` / `x-axonrouter-mode` headers; non-JSON passes through with `x-axonrouter-output-kind`. Ported with the same self-call mechanism. Deviation: Axon also writes a unified-endpoint request-detail record (`saveRequestDetail`); Go's port does not write that extra record — the modality sub-request itself is logged by the gateway like any `/v1` call. |
| POST /v1/video/generations | MISSING → PORTED | Axon's route.ts is a baseline route (no provider call): key-gated in-handler, validates JSON/model/prompt with `{error, code}` shapes (`invalid_json`, `missing_model`, `missing_prompt`), returns `{created, data:[{object:"video.generation", status:"accepted", message:"Video generation baseline route accepted the request.", prompt, model}]}`. Ported as-is (auth via the engine group, see global notes). |
| POST /v1/videos/generations | EXISTS-SAME | `HandleVideoGenerations`. |
| POST /v1/videos/edits | EXISTS-SAME | `HandleVideoEdits`. |
| POST /v1/videos/extensions | EXISTS-SAME | `HandleVideoExtensions`. |
| GET /v1/videos/{id} | EXISTS-SHAPE-DIFF (deferred) | Axon polls the upstream video job (`handleVideoGet`). Go returns a canned `{id, status:"completed"}` — the video job lifecycle does not exist in Go's media lane. Pre-existing gap, not a regression; parent ruling (same infra question as content). |
| GET /v1/videos/{id}/content | MISSING (blocked, not stubbed) | Axon serves the finished job's binary content. Go has no video job/content store to serve from (see previous row); an honest port requires the job lifecycle. Blocked with reason. |
| POST /v1/web/fetch | EXISTS-SAME | `HandleWebFetch`. |

### v1beta (2)

| Path | Verdict | Notes |
| --- | --- | --- |
| GET /v1beta/models | MISSING → PORTED | Gemini-shaped list built from the static catalogue: every provider model as `models/{provider}/{id}` (methods `["generateContent"]`), gemini models additionally unprefixed (methods `["generateContent","streamGenerateContent"]`), `inputTokenLimit: 128000`, `outputTokenLimit: 8192`, deduped by name. Go's catalogue carries ids only, so `displayName` falls back to the id (Axon: `model.name \|\| model.id`). |
| POST /v1beta/models/{model}:generateContent, :streamGenerateContent (+ `{provider}/` prefixed form) | MISSING → PORTED | Axon: parse provider/model + action; Gemini-native passthrough only for TTS requests (AUDIO response modality or a Gemini TTS model, unprefixed/gemini-prefixed), otherwise convert Gemini→internal (systemInstruction + contents text parts + generationConfig subset), run the chat pipeline, convert back (JSON → GenerateContentResponse; SSE → Gemini chunks, `\r\n\r\n`, no `[DONE]`). Ported on the Go chat handler, conversions mirroring Axon's functions field-for-field. Deviations: the native lane rotates pooled gemini connections but does not write Go cooldown/strike state (Axon calls `markAccountUnavailable`/`clearAccountError`); native timeout 45 s per Axon's `GEMINI_NATIVE_TTS_FETCH_TIMEOUT_MS` default. |

### protocols (6) — dashboard-side, `/api/protocols/**`

| Path | Verdict | Notes |
| --- | --- | --- |
| GET /api/protocols/status | MISSING → PORTED | Self-probes `/api/mcp/status`, `/api/mcp/tools`, `/api/mcp/invoke`, a2a handshake + message and aggregates per Axon's logic (mcp `online/offline/degraded`, a2a `staging/planned`, toolCount, transports, capabilities). While `/api/mcp/*` is unported (batch 8), it reports mcp `degraded` and a2a `staging` — Axon's own output when its MCP backend is down. |
| GET /api/protocols/a2a/handshake | MISSING → PORTED | Static `{protocol:"a2a", status:"ok", version:1, capabilities:[…]}`, `Cache-Control: no-store`. |
| POST /api/protocols/a2a/message | MISSING → PORTED | Ack echo `{protocol:"a2a", status:"ack", received:{agent, message, correlationId}}` (nulls when absent; invalid JSON tolerated as `{}`). |
| GET /api/protocols/mcp/handshake | MISSING → PORTED | Proxies own-origin `/api/mcp/status`, reshapes to `{protocol:"mcp", status: status.status \|\| "offline", version:1, capabilities:[…]}`. |
| GET /api/protocols/mcp/tools | MISSING → PORTED | Proxies own-origin `/api/mcp/tools` JSON verbatim. Deviation: when the backend answer is not JSON, Axon throws into Next's HTML 500; the Go port answers 500 with a JSON error body (status parity, body differs). |
| POST /api/protocols/mcp/invoke | MISSING → PORTED | Proxies own-origin `/api/mcp/invoke` forwarding `authorization` / `x-api-key`; payload + status passthrough; non-JSON upstream → `{ok:false, error:"Invalid response"}` at the upstream status; transport failure → 500 `{error:"Failed to invoke MCP tool", message}`. All per Axon. |

### translator (7) — dashboard-side, `/api/translator/**`, management auth

| Path | Verdict | Notes |
| --- | --- | --- |
| GET /api/translator/console-logs | EXISTS-SAME (N/A) | Axon's route is a thin re-export (`export { GET, DELETE } from "@/app/api/console-logs/route"`); Go already serves it in `SetupConsoleLogRoutes`. No work. |
| GET /api/translator/console-logs/stream | EXISTS-SAME (N/A) | Same re-export pattern; Go already serves the stream (plus a level GET/PUT Axon exposes elsewhere). No work. |
| GET /api/translator/load | MISSING → PORTED | Whitelisted stage-file read (8 fixed filenames), `{success, content}` / 400 `File parameter required` / 400 `Invalid file name` / 404 `File not found`. Files live under `<DATA_DIR>/logs/translator` (fallback: CWD) — Axon roots at `process.cwd()/logs/translator`; the DATA_DIR root is the Go/Docker state-volume equivalent, and load/save are consistent with each other, which is the observable contract of the pair. |
| POST /api/translator/save | MISSING → PORTED | Whitelisted stage-file write (creates the directory), `{success:true}` / 400 `File and content required` / 400 `Invalid file name`. |
| POST /api/translator/send | MISSING → PORTED | Executes the given provider-format body through the provider executor on the gateway's chosen active connection (`GetBestConnection`), Axon error mapping (`{success:false, error:"Provider error: <status>", details}` at the upstream status; success streams back with `text/event-stream` headers), 401/403 → OAuth refresh + one retry (the chat layer's `forceRefreshOAuthToken`). Deviation: Axon refreshes via `refreshTokenByProvider` before re-executing; Go's retry uses the chat pipeline's own refresh, and executors additionally self-refresh when a connection id is set. |
| POST /api/translator/translate | MISSING → PORTED (partial; step 3 deferred) | Step 1 (parseModel + detectFormat + getTargetFormat) ported — detectFormat mirrors `open-sse/services/provider.js` heuristics field-for-field; parseModel covers the provider/model and bare forms (Axon's static BUILTIN_MODEL_ALIASES table is not replicated — Go resolves model aliases through its DB/combo layer, not a static table). Step 2 (source → OpenAI) ported for `openai` (identity) and `claude` (the Go translator's real Claude→OpenAI request translation, `_toolNameMap` stripped like Axon). Deferred with reasons: step 3 (OpenAI → target + executor `buildUrl`/`buildHeaders`/`transformRequest`) — Go executors expose no build-only request-construction surface; adding one restructures every executor. Step 2 for `gemini`/`antigravity`/`openai-responses` sources — the Go translator registry has no Gemini→OpenAI or Responses→OpenAI *request* translator (its Gemini code serves Gemini upstreams and the v1beta lane). Those cases answer Axon's catch shape `{success:false, error}` at 500 with the reason named. Parent ruling requested. |

### morph (3) — dashboard-side, `/api/morph/**`, management auth

| Path | Verdict | Notes |
| --- | --- | --- |
| POST /api/morph/test-key | MISSING → PORTED | Reads `settings.morph {baseUrl, apiKeys[]}` from the raw settings row; 400 `Email is required`, 404 `Morph API key not found`; POSTs the fastValidation probe (`morph-v3-fast`, fixed instruction message) to `{baseUrl origin}/v1/chat/completions` (Axon's `new URL("/v1/chat/completions", baseUrl + "/")` semantics — baseUrl path is replaced); builds the status patch exactly per `test-key/shared.ts` (2xx → active; 429 → cooldown + 14 d `nextRetryAt`; exhausted on 402 or credit/quota wording on 403; invalid-key wording on 400/401/403 → inactive; else unknown); persists the patched entry via the settings raw update; answers `{ok, email, status, isExhausted, lastError}` at 200 when active, else 409; transport failure → 500 `Failed to test Morph API key`. Deviation: Axon's settings write is a single atomic transform; Go's port does read-modify-write through `GetSettingsRaw` + `UpdateSettingsRaw` (the established dashboard pattern). |
| GET /api/morph/usage/stats | MISSING → PORTED | Axon's route validates `period` ∈ {24h, 7d, 30d, 60d, all} (else 400 `Invalid period`) and reads its DB query layer, whose window quirk is mirrored exactly: only `24h` gets a 24 h window — every other period aggregates a 7 d window — while `totalRequestsLifetime` counts all rows. Rows: Go `usageHistory` with `provider='morph'` (Axon filters its `usage_events` on `source='morph'`). `totalCredits` is always 0 in Axon's DB variant, and here. Capability is derived by reverse-mapping the model through Axon's capability default models (`morph-v3-large` → apply, `morph-warp-grep-v2.1` → warpgrep, `morph-compactor` → compact), else `unknown` (Axon's own fallback when its category is empty). `byApiKey` keys on `Unknown email` — Axon's fallback label; Go's ledger stores the masked upstream credential, not the key's email label. |
| GET /api/morph/usage/requests | MISSING → PORTED | Same source; `limit` clamped to [1, 500], default 200; rows mapped exactly per Axon's `mapMorphRecentRow` (`method:"POST"`, `source:"morph"`, resolved/requested = model, `upstreamStatus: null`, `error: null`, credits 0). |

### Alias mounts (Phase B)

`RequestLogger` now rewrites `/api/v1` → `/v1`, `/api/v1/*` → `/v1/*`,
`/api/v1beta` → `/v1beta`, `/api/v1beta/*` → `/v1beta/*` before the
existing `/v1/` strip — the same virtual-prefix mechanism the codebase
already used for `/v1`, mirroring Axon's `webServer.mjs` rewrites. One
handler set serves both spellings; no logic is duplicated. Side effect
(superset, consistent with Go's existing bare + `/v1` duality): Go-only
engine routes (files, batches, rerank, scrape, sound-generation,
audio/music) also answer at `/api/v1/**`, where Axon 404s.
`ExtractApiKey` additionally accepts `x-goog-api-key` and `?key=` /
`?apiKey=` on `/v1beta` paths, the credential channels Gemini clients
actually send (Axon's `extractGeminiClientApiKey`: Bearer,
x-goog-api-key, ?key=).

### Global notes (pre-existing, not changed by this batch)

- OPTIONS: Axon exports CORS `OPTIONS` handlers on most v1 routes; Go's
  engine surface has no per-route OPTIONS handling. Unchanged.
- Engine auth: Go's `RequireApiKey` always requires a valid key and
  answers Go's nested error envelope; Axon's unified/video-generations
  handlers self-check keys only when keys are configured and use
  `{error:{message, type:"auth_error"}}`. The two ported baseline routes
  sit in Go's engine group like every other engine route, so their
  key-missing/invalid responses follow the gateway-wide shape.
- Go also serves engine routes at bare spellings (no prefix) — a
  superset Axon does not have. Unchanged.

## Phase B — implementation

Files (new files are `batch6_*`, one responsibility each; no web/
changes; nothing committed):

- `internal/middleware/logging.go` — RequestLogger folds `/api/v1` →
  `/v1` and `/api/v1beta` → `/v1beta` before the existing `/v1/` strip
  (the alias mounts).
- `internal/middleware/auth.go` — ExtractApiKey accepts
  `x-goog-api-key` / `?key=` / `?apiKey=` on `/v1beta` paths.
- `internal/handlers/router.go` — engine registrations: `GET /v1`
  (models list), `GET /v1beta/models`, `POST /v1beta/models/*`,
  `POST /task`, `POST /unified`, `POST /video/generations`; dashboard
  registrations: protocols ×6, translator load/save/translate/send,
  morph ×3.
- `internal/handlers/chat/batch6_v1beta.go` — v1beta models list +
  model action (convert lane through HandleChatCompletions with
  JSON/SSE conversion back; native Gemini TTS passthrough with pooled
  credential rotation, 45 s timeout).
- `internal/handlers/chat/batch6_translator_send.go` —
  `/api/translator/send` on GetBestConnection + the provider executor,
  status-buffering capture writer, 401/403 OAuth-refresh retry.
- `internal/handlers/media/batch6_task.go` — `/v1/task` with the
  Cloudflare Workers AI adapter (7 model specs, image/audio field
  conversion, rotation, buildErrorBody envelopes).
- `internal/handlers/batch6_unified.go` — `/v1/unified` (validation,
  same-origin self-dispatch, envelope) + the `/v1/video/generations`
  baseline route.
- `internal/handlers/batch6_protocols.go` — protocols family
  (a2a static/ack, mcp self-proxy ×3, status aggregate).
- `internal/handlers/batch6_translator_files.go` — translator
  load/save over the whitelisted stage files.
- `internal/handlers/batch6_translator_translate.go` — translate
  steps 1-2 (detectFormat port; claude→openai via the real Go
  translator); step 3 deferred in-handler with the reason.
- `internal/handlers/dashboard/batch6_morph.go` — morph test-key
  (status patch + settings persistence) and usage stats/requests over
  usageHistory.
- `internal/handlers/chat/chat.go` — contained fixes only:
  count_tokens invalid-JSON → Axon 400; models/info 400/404 → Axon
  shapes + the findModelForLookup existence gate.

Verification (direct checks only, per the no-unit-tests rule):
`~/bin/heavy ~/go-sdk/bin/go build ./...` green; `go vet
./internal/handlers/... ./internal/middleware/...` green; gofmt clean
on every touched file (three pre-existing unformatted files elsewhere
were left alone).

### Core-path acceptance + smoke evidence

Scratch server (binary in `~/tmp-smoke`, DATA_DIR
`~/tmp-smoke/b6-data`, port 18222, stdin from a regular file,
requireLogin flipped off in the settings row) with a self-controlled
mock upstream (python, port 18177) seeded as an `openai` connection
whose data.baseUrl points at the mock. Note: the gateway's SSRF guard
blocks private-IP upstreams by default — the scratch server ran with
the documented operator switch `ALLOW_PRIVATE_NETWORKING=true`, the
same switch local-by-design providers use; no code change. A python
probe suite (46 checks, API key created and used in-script only)
passed end to end:

- Core path: `GET /v1/models` lists the mock-backed model; unauthenticated
  → 401; `POST /v1/chat/completions` non-stream returns the mock's exact
  payload (content + usage) and stream returns the mock's SSE chunks
  ending in `[DONE]`; the `/api/v1` alias spelling returns the same
  payload; `GET /v1` root returns the models list.
- v1beta: models list shape incl. unprefixed gemini entries with
  `streamGenerateContent`; `:generateContent` converts the mock's
  OpenAI answer into Gemini candidates (finishReason STOP,
  usageMetadata totals match); `:streamGenerateContent` emits Gemini
  SSE chunks with finishReason and no `[DONE]`. The native TTS lane
  was additionally proven against the REAL Google endpoint: with a
  seeded (fake-key) gemini connection, an AUDIO-modality request was
  forwarded to generativelanguage.googleapis.com with the pooled key
  and Google's 400 "API key not valid" body passed through verbatim.
- task: all four validation/credential error shapes (wrong provider,
  non-task model, missing required field, no credentials).
- unified: text envelope (rt_ correlation id, mock payload in
  `output`, `x-axonrouter-mode: text`), video mode wrapping the
  baseline, unsupported-mode and missing-model 400 codes.
- video/generations baseline: accepted envelope + missing_prompt 400.
- protocols: a2a handshake/ack; mcp handshake "offline", tools 500,
  invoke 404 + `{ok:false, error:"Invalid response"}`; status
  aggregate mcp "degraded" / a2a "staging" — Axon's own degraded
  output while `/api/mcp/*` is unported (batch 8).
- translator: save→load round-trip, invalid-name/missing-param 400s,
  absent-file 404; translate step 1 (claude detected, anthropic
  target), step 2 (real claude→openai conversion), step 3 deferred
  500 envelope, missing-step 400; send streams the mock's reply with
  `text/event-stream` headers; send missing-fields 400.
- morph: test-key good key → 200 active and the settings read-back
  shows the persisted patch (status active, lastError cleared);
  402 key → 409 exhausted (read-back: isExhausted true, lastError =
  upstream body); unknown email 404; missing email 400. Usage stats
  over 3 seeded usageHistory rows (totals, byModel counts,
  totalRequestsLifetime, credits 0), invalid period 400, requests
  rows with capability reverse-mapping (morph-v3-large → apply).
- models/info: missing-id 400 and unknown-id 404 in Axon's shapes;
  happy path 200 at both `/v1` and `/api/v1` spellings after the
  existence gate. count_tokens: invalid JSON → Axon 400; valid body
  → input_tokens > 0.

## Deferred / blocked summary (for the parent's ruling)

1. GET /v1/audio/voices — response transform + provider validation
   (spelling-selective fix needs a core-path marker).
2. GET /v1/models/info — success-payload reshape (name/kind/per-kind
   endpoint, `kind` param).
3. GET /v1/models/{kind} — model-granular kind listings incl. the
   `task` slug.
4. GET /v1/videos/{id} + /content — video job lifecycle does not exist
   in Go's media lane.
5. POST /api/translator/translate — step 3 and step 2 for
   gemini/antigravity/openai-responses sources.
