### Batch 10 — the three owner-decision items, landed (audit items 2, 4, 5b) + tunnel/status stop-report

Owner order 2026-11-04 22:05 WIB ("selesaikan semua") unparked the
three batch-9 owner-decision items. All three are implemented;
item 4 (delete `/api/tunnel/status`) hit its stop rule and was NOT
deleted — the consumer evidence is recorded below for the owner's
ruling. Verified live against mock upstreams (rig:
`~/tmp-smoke/b10/b10_verify.py`, 40/40 checks PASS, including the
batch-9 probe set and chat stream + non-stream regression);
`go build ./...` + `go vet ./...` green, gofmt clean on touched
files, go.mod/go.sum and web/ untouched.

#### Item 1 — GET /v1/models/info reshaped to Axon's document (audit item 2)

`internal/handlers/chat/models_info.go` (new; the old handler was
removed from chat.go). The lookup is now Axon's static-catalogue
lookup (`src/app/api/v1/models/info/route.js`): the id must name a
registry catalogue model (alias key, then canonical id), an
optional `?kind=` must match the catalogue kind, and the virtual
`<provider>/search` / `<provider>/fetch` models exist for
providers carrying a search/fetch config. Success document:
`{id, name, kind, owned_by, endpoint}` + sourced extras; Axon's
CORS header on every response of the route; Axon's 400/404 error
documents verbatim.

Owner-ordered behavior changes (accepted when the item was
ordered): the Go-only keys (`object`, `context_length`,
`context_window`, `max_completion_tokens`, `max_input_tokens`,
`max_output_tokens`, and the combo block) are deleted, and combo
ids flip from 200 to Axon's 404. No SPA consumer exists
(`grep "models/info" web/src/` is empty), so no consumer needed
adjusting. The per-key model-access gate stays — it is gateway
policy on every /v1 surface, not document shape.

Field sourcing, honestly:
- Sourced: id/kind/owned_by/endpoint (registry ids + per-model
  kinds + Axon's KIND_ENDPOINT table); `contextWindow` when the
  capabilities chain yields one (gpt-4o → 128000 in the smoke);
  the virtual web models' fixed `params` arrays (Axon contract
  constants).
- `name`: Go's catalogue is ids-only (`ProviderModels` is
  `map[string][]string`) — no display names exist, so name falls
  back to the model id, Axon's own fallback (`model.name ||
  model.id`). The virtual models' names use the provider id
  where Axon uses its display name, same reason.
- NOT sourced, therefore omitted (never fabricated): `params`,
  `capabilities`, `options`, `dimensions` on catalogue models —
  Axon's registry carries them per model; Go's registry has no
  per-model source (the capabilities chain is a boolean feature
  struct, a different vocabulary from Axon's string-array
  catalogue field). `searchTypes` / `maxResults` / `required` on
  the virtual search model — they come from a provider
  `searchConfig` Go's registry does not carry.
- `voicesUrl` is implemented exactly per Axon's rule (kind `tts`
  + provider in {elevenlabs, edge-tts, deepgram, inworld,
  local-device}) but cannot fire on today's registry data: none
  of those five providers has a model whose registered kind is
  `tts` (elevenlabs' TTS ids resolve through a separate catalogue;
  its registered kinds here are the `scribe_*` STT models). The
  branch lands with the rule; the data does not trigger it.

#### Item 2 — videos subsystem: live upstream proxy with account pinning (audit item 4)

New `internal/handlers/media/video_core.go` (the single-attempt
proxy, Axon's `open-sse/handlers/videoCore.js`) and
`video_handlers.go` (create/poll/content, Axon's
`src/sse/handlers/videoGeneration.js`); media.go's three create
handlers re-pointed, the canned `HandleVideoGet` deleted, and
`GET /v1/videos/{id}/content` registered.

- GET poll + content proxy the upstream job through the creating
  connection: create answers `x-axonrouter-connection-id`, polls
  echo it as `x-connection-id` and are served by that connection
  (its own baseUrl + credentials). Axon's 400s verbatim for
  unknown pin / non-video pinned provider / credential mismatch;
  unpinned polls resolve `?provider=` else default `xai`.
- Create rotation only on 401/403/429 (max 10 attempts); a 5xx is
  returned, never re-sent. The core refreshes a 401/403 once via
  the gateway's OAuth refresh before surfacing it. 120 s fetch
  timeout (408 on timeout, else 502), upstream error bodies
  secret-sanitized and truncated like Axon's, success JSON passed
  through verbatim, content streamed with upstream Content-Type
  (fallback video/mp4) + Content-Disposition/Length.
- Provider scope: xai + openrouter — the providers whose
  videoConfig exists in both registries (Go `VideoURL` = Axon's
  baseUrl for both). The OpenRouter adapter's rules are ported
  (create posts to the collection root, generations only, JSON
  bodies only, plan errors at 400). Axon's other videoConfig
  providers (beatapi, vertex, unikey) have no Go VideoURL and
  stay on the generic media lane; vertex's dedicated Axon adapter
  is not ported.
- Kept on the existing lane by design: combos, the special web
  providers (uc-web, veoaifree-web, grok-web — not video
  providers in Axon's registry), and providers with no video
  config (e.g. openai/sora) still go through forwardMediaRequest,
  including its input caps and access gate (the core path runs
  the same gates first).
- Connection baseUrl override: when a connection's stored
  baseUrl points at a different host than the registry VideoURL,
  the video base derives from it (strip a trailing /v1, append
  /v1/videos) — the media lane's existing override convention,
  and what the pinning proof exercises.
- Usage: Axon's per-call `saveCapabilityUsage` has no Go
  counterpart store; calls are recorded through the gateway's
  usage history (LogUsage) per create attempt outcome and per
  poll/content, the closest existing equivalent.
- Smoke proof: two mock upstreams + two xai connections (bad key
  first by priority) — create rotated 401 → good connection, pin
  header names the good connection, poll and content hit ONLY
  the creating connection's mock, content returns its exact
  bytes; unknown-pin 400 verbatim; unpinned poll proxies the
  real job payload (no canned body anywhere).

#### Item 3 — translator step 3: provider-request construction (audit item 5b)

New `internal/handlers/batch10_translator_step3.go` +
`internal/translator/claude_request.go` /
`claude_request_messages.go` (the OpenAI→Claude request
translator port); `batch6_translator_translate.go` case 3 wired
to it and its header comment updated. Additive only, per the
owner's constraint: build-only constructors beside the existing
code; the live chat path is untouched, and the chat path's
private Claude conversion was duplicated into the translator
package rather than moved. Nothing is sent — the step returns
`{url, headers, body}` like Axon's route.

- Flow mirrors Axon's route: translate the OpenAI intermediate
  body to the target format, resolve the active connection
  (none → Axon's 400 `No active connection for provider: <p>`
  verbatim), then DefaultExecutor buildUrl / buildHeaders /
  transformRequest semantics over Go's registry data.
- Translations: openai = identity; claude = the full port of
  `openai-to-claude.js` (Claude Code system prompt, system
  extraction, tool_result splitting, same-role merge passes,
  cache_control rules, tools/tool_choice conversion,
  response_format system guidance, adjustMaxTokens with the
  model's declared ceiling); gemini = the existing
  TranslateOpenAIToGemini.
- buildUrl: compat node prefixes resolve against the
  connection's base with Axon's fallback bases
  (openai-compatible → /chat/completions, anthropic-compatible
  → /messages); Gemini-native formula `/{model}:{action}`;
  `{accountId}` substituted from providerSpecificData, missing
  → Axon's CONFIG_ERROR text at 500; otherwise the registry
  BaseURL, which in Go is already the full endpoint URL.
- buildHeaders: Content-Type + registry StaticHeaders, then
  auth per the descriptor the wire format implies — combined
  descriptors take apiKey || accessToken (Axon's applyAuth),
  the anthropic-compatible split takes the apiKey branch first;
  anthropic-version pinned when unset; `Accept:
  text/event-stream` when streaming.
- NOT ported (DefaultExecutor refinements with no Go registry
  counterpart): selectAnthropicBeta (Go's registry already bakes
  the beta/UA strings into StaticHeaders, e.g. the claude entry),
  Nous attribution tags (fire only for one proxy host's compat
  nodes), the dropClientMetadata quirk and stripUnsupportedParams
  (per-provider quirk/parameter tables Go's registry does not
  carry), injectReasoningContent. The openai-compatible
  json_schema → json_object fallback IS ported.
- Specialized-executor providers (Axon's executors/index.js:
  antigravity, azure, bedrock, codebuddy-cn/intl, codex,
  commandcode, cursor, freebuff, gemini-cli, github, grok-cli,
  iflow, kilocode-free, kimchi, kiro, llmtech-free, ollama-local,
  opencode/-go/-zen, openrouter, qoder, tokenharbor, trae,
  vertex, windsurf, workbuddy, xiaomi-mimo, xiaomi-tokenplan)
  answer a truthful `{success:false, error}` at 500 naming the
  executor. (`cc` is deliberately not gated: in Go's registry it
  aliases the claude provider, not commandcode.)
- Known format limitation, inherited from batch 6: Go's registry
  carries no per-node responses apiType, so an openai-compatible
  node always builds /chat/completions (Axon appends /responses
  for responses-typed nodes). Step 3's target-format resolution
  keys off the wire format (x-api-key ⇒ claude) where the batch-6
  step-1/2 helper keys off provider ids; steps 1–2 behavior is
  unchanged.
- Smoke proof: openai build (registry URL, Bearer from the
  stored apiKey), anthropic build (x-api-key + anthropic-version,
  Claude translation with ceiling-clamped max_tokens and the
  tool_result split), deepseek generic build, mistral
  no-connection 400 verbatim, codex gated 500, cloudflare-ai
  accountId 500; chat stream + non-stream regression green.

#### Item 4 — DELETE /api/tunnel/status: STOPPED, not deleted

The task's stop rule fired: the route is consumed by live,
unrelated UI and is not an ngrok leftover.

- Provenance: the ngrok surface was `POST
  /api/tunnel/ngrok/start|stop` (deleted in the owner-ordered
  removal, .changes/920), whose fragment explicitly KEPT the
  Cloudflare/Tailscale tunnel family as a separate feature.
  Batch 9's D13 premise was a scope question; 920 answers it.
- The handler (`internal/handlers/dashboard/tunnel.go`,
  HandleTunnelStatus) serves the live `internal/tunnel` package
  (cloudflared manager: Enable/Disable/Status + Tailscale funnel
  state) and returns `{tunnel:{...}, tailscale:{...},
  download:{...}}`.
- Live SPA consumers: `web/src/api/client.ts` (`getTunnelStatus`
  + `TunnelStatusResponse`), `EndpointView.svelte` (the tunnel
  settings surface, `applyTunnelStatus` throughout),
  `HermesToolCard.svelte`, `SttExampleCard.svelte`,
  `TtsExampleCard.svelte`.
- Live proof in the smoke: `GET /api/tunnel/status` answers 200
  with the tunnel/tailscale/download keys. Route, handler, and
  UI are left fully in place for the parent's/owner's ruling; if
  deletion is still ordered, it is a whole-family removal
  (status/enable/disable/tailscale-* + EndpointView surface +
  internal/tunnel), not a single-route delete.

#### Known-stale tests (not edited, per the no-test-edits rule)

- `internal/handlers/chat/custom_model_limits_test.go` and
  `internal/integration/custom_model_limits_test.go` assert the
  pre-batch-10 models/info shape (`context_length`,
  `max_output_tokens`) for a custom model — custom models are
  not in the static catalogue, so the route now answers Axon's
  404. Both assert owner-deleted behavior and need owner-side
  updating.
- No existing test asserts the old canned video GET body or the
  old step-3 deferral message (searched; none found).
