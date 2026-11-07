# Playground chat — active-connection models, Persona prompt injection (1008)

## Symptom

The dashboard had no way to chat with a model through the gateway itself.
Testing a connection, a combo, or a Persona preset meant pointing an
external client at /v1 with a real API key, and the only model lists
available were catalogs — /v1/models publishes providers, not the
per-connection inventory an operator actually wants to pick from, and
nothing let the operator try a one-off system prompt or a preset against
a model without first binding it as that model's standing configuration.

## Root cause

No playground surface existed: no endpoint aggregated "models on active
connections right now", no dashboard caller identity existed for usage
attribution (the ledger names its caller from the authenticated API key,
which a browser session does not have), and the Persona injector
(applyJailbreak) resolved its document only from the standing per-model
configuration, so a session-scoped prompt decision had nowhere to ride.

## Fix

- **Model inventory** — `GET /api/playground/models` (dashboard session
  group) returns combos first, then one entry per model for every usable
  connection: `{type: 'model'|'combo', provider, connectionId?,
  connectionName?, model}` with `model` the wire id a client would send.
  Usable mirrors the serving path's model-independent eligibility
  (pinnedConnectionIneligible's pre-model checks): `isActive`, and not
  inside an account cooldown park (`db.ConnectionBlockedUntil`;
  ToS-disabled accounts are excluded via `isActive`, which
  DisableConnectionWithReason flips off). Hidden providers are not
  offered, as in /v1/models. Each connection's model set is computed by
  `appendConnectionModels` — the same per-connection builder /v1/models
  uses — with a fresh seen-set per connection, so every connection is
  attributed its own full list.
- **Chat** — `POST /api/playground/chat` translates the playground body
  into an ordinary OpenAI chat body and dispatches it through
  `HandleChatCompletions` itself on a clone of the caller's request: the
  same proxy binding, guardrails, stall gate and usage logging as client
  traffic, SSE streamed chunk-for-chunk, and client abort cancelling the
  same context. Caller identity is a real apiKeys row named exactly
  **playground** (loaded or minted once, cached on the handler, no
  allowlist = unrestricted like any pre-policy key), placed on the
  context under the same slot the auth middleware fills, so usage rows
  and request details name the caller "playground". The handler echoes
  the request's `req_<hex>` id in an `X-Request-ID` response header
  (operator surface only; /v1 keeps not echoing it).
- **Prompt precedence** — the request's prompt decision rides the context
  as a `playgroundPromptOverride` that `applyJailbreak` honors before the
  standing resolver: a non-empty `systemPrompt` injects as a single-mode
  prepend doc; else `presetId` loads that Persona preset from the
  existing store and injects it; else `applyStandingPersona: false`
  suppresses the standing configuration. Any override present means the
  standing resolver is never consulted, so the sources can never stack.
  Injection itself is the extracted `applyJailbreakDoc` — the one
  injector, shared by the standing path and the playground (per-format
  placement via the existing machinery, usage counter, Gemini safety
  flag). Routes are registered in both route tables
  (SetupDashboardRoutes and dashboard.RegisterRoutes, the latter via a
  new `DashboardHandler.Chat` wired in SetupRoutes).
- **Frontend** — new `playground` tab (nav main group, route
  `/dashboard/playground`, PAGE_META) with components under
  `web/src/components/playground/`: PlaygroundView (composition + send /
  stream orchestration), ModelPicker (grouped by provider — connection,
  combos first, empty state when nothing is active), PromptPanel (session
  prompt textarea, preset picker from the existing jailbreak presets API,
  standing-Persona indicator + apply toggle read from the existing
  /api/jailbreak/models map), ParamsPanel (temperature, max tokens),
  ChatThread (streaming render, stop via AbortController, per-reply meta
  line: model + latency + req id). Conversation state is browser-only;
  the full history is sent each turn. API client gains
  `getPlaygroundModels` + `playgroundChatStream` in web/src/api/client.ts.

## Verification

- `go build ./...` green and `go vet ./internal/handlers/...` clean via
  the heavy gate (covers the new dashboard→chat import: no cycle).
- Temporary probe `TestProbePlaygroundPromptPrecedence` (single named
  test, run alone, file deleted after, absence confirmed in git status):
  PASS on all four legs at the applyJailbreak seam with a repo-less
  handler — session-prompt override injects a prepended system message;
  suppression-only override leaves the body untouched; no override with a
  nil repo is a safe no-op; a chat-mode preset doc injects its turns
  through the same machinery. The models aggregation could not be
  exercised the same way (the Repo is a concrete PostgreSQL-backed type
  with no fake), so it reuses `appendConnectionModels` verbatim and was
  verified by build + code-path identity with /v1/models.
- Web build via heavy (`tsc -b && vite build` + precompress) green;
  svelte-check (`--tsconfig ./tsconfig.app.json`) at the standing
  baseline: 89 errors / 56 warnings, zero mentions of the new files.
- Grep proof of no second implementation: `injectSystemPrompt` exists
  only in internal/handlers/chat/jailbreak.go, and the playground chat
  handler's only dispatch is `h.HandleChatCompletions`.
- No existing test files were edited (nav.test.ts reads NAV_ORDER
  generically; the new id needs no mechanical expectation updates).
