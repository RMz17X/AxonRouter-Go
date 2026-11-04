# Backend API parity — Batch 5: CLI installer system (P7a) + OpenCode family + Cowork MCP

Fifth batch of the AxonRouter backend-parity program (see
~/workspace/axon-backend-parity-map.md), executing master-plan P7a
(L11): the CLI Tools surface stops writing config files into the
gateway host's home and becomes Axon's installer system — the gateway
serves per-tool setup scripts and the dashboard composes the
copy-paste command. Also in this batch: the OpenCode sync family
(6 routes, route.ts contracts) and the two Cowork MCP routes
(route.js). DECLINED, not ported, per L10: /api/cli-tools/antigravity-mitm
and /api/cli-tools/antigravity-mitm/alias (MITM deleted).

## Per-endpoint disposition — host-write retirement (decided before coding)

State before this batch: `internal/handlers/media/clitools.go`
(all-statuses), `cli_tool_settings.go` (dynamic clientconfig adapter
routes), `hermes_settings.go`, `pi_settings.go`.

| Endpoint | Before | Disposition |
|---|---|---|
| GET /api/cli-tools/all-statuses (+ /cli-tools/all-statuses alias) | read-only probe aggregate {installed, version, has9Router} | **KEEP unchanged.** Pure read/status; it is the dashboard's status source. Axon has no equivalent route (its page fans out per tool); the aggregate carries the same read semantics and no write side exists to retire. |
| POST /api/cli-tools/{id}-settings (clientconfig adapters: claude, codex, opencode, droid, crush, kilo, cline, omp) | adapter.Apply writes the tool's config files in the gateway host's home | **DELETE** (route deregistered; cli_tool_settings.go removed). Under Docker the write lands in the container and configures nothing while reporting success — exactly the L11 failure mode. |
| DELETE /api/cli-tools/{id}-settings | adapter.Reset rewrites host files | **DELETE** (same reasoning). |
| GET /api/cli-tools/{id}-settings for claude, codex, opencode, droid, kilo, cline | 9router "plan" shape {tool, installed, has9Router, files, plan} | **REPLACE with Axon's read shape** per tool (installed / settings-or-config content read back from the host file / hasAxonRouter / settingsPath-or-configPath + tool extras), GET only. The read still describes the host the gateway runs on — truthful in the first-class single-node mode, and shape-identical to Axon. |
| GET /api/cli-tools/{crush,omp}-settings | plan shape | **DROP.** Axon defines no crush/omp settings route — no contract to match. Their status remains covered by all-statuses. |
| GET /api/cli-tools/{copilot,cowork,deepseek-tui,grok-build,jcode,openclaw}-settings | not served | **ADD, GET only**, Axon read shapes (new coverage from the re-score). |
| POST/DELETE /api/cli-tools/hermes-settings (incl. applyToAll) | writes ~/.hermes profiles | **DELETE.** |
| GET /api/cli-tools/hermes-settings, GET /api/cli-tools/hermes-profiles | read, 9router field name has9Router | **KEEP, re-scored:** response key renamed to Axon's hasAxonRouter; profile/settings block shapes already matched Axon's hermesYaml parse ({default, provider, base_url, api_key} etc.) and are unchanged. Axon's check order (installed → profile) vs the Go handler's (profile → installed) kept as-is — recorded deviation, deliberate in the Go port. |
| POST/DELETE /api/cli-tools/pi-settings | writes ~/.pi/agent/models.json | **DELETE.** |
| GET /api/cli-tools/pi-settings | read {installed, config, has9Router, configPath} | **KEEP, re-scored to Axon's shape:** adds `pi: {models, baseURL}` (from providers["axonrouter"]) and renames has9Router → hasAxonRouter with Axon's rule (provider block presence). |

The `internal/clientconfig` package loses its HTTP surface with
cli_tool_settings.go; the package itself stays in-tree (its adapters
remain the config-format reference; no HTTP path can reach Apply/Reset
after this batch). Full package removal belongs to the P5 host-surface
pass, not this batch.

## Installer (P7a core)

- **`internal/clisetup`** — port of Axon's
  `src/shared/constants/cliToolTemplates.js` (templates as data) and
  `src/lib/cliSetup/{bashGenerator,powershellGenerator}.js`
  (renderers), one responsibility per file: `clisetup.go` (params,
  ordered-JSON `JObj` with a `JSON.stringify(v, null, 2)`-faithful
  writer plus a compact variant for content hashing), `templates.go`
  (all templates), `bash.go`, `powershell.go`. **Axon's template file
  defines 15 tools**, not the 13 in the batch brief — the extra two
  are `deepseek-tui` and `grok-build`; the setup route serves any
  template key, so all 15 were ported.
- **`GET /api/cli-tools/setup/{toolId}`** (`internal/handlers/media/batch5_setup.go`),
  Axon's `setup/[toolId]/route.js` contract: query params `baseUrl`
  (inferred from Host + `x-forwarded-proto` when absent; localhost →
  http, other hosts → https; default host `localhost:3777`), `apiKey`,
  `model`, `subagentModel`, `maxContextTokens`, per-tier
  `sonnet/opus/haiku/fable` → models map, `modelsList` (comma-split,
  trimmed, empties dropped; empty param = absent). `format=ps1` →
  PowerShell, anything else → bash. Unknown tool → 404 `text/plain`
  with the exact body `Error: Tool '<id>' not supported for automated
  setup.\n`. Success headers: `Content-Type: text/x-shellscript;
  charset=utf-8` (bash) / `text/plain; charset=utf-8` (ps1),
  `Cache-Control: no-store, no-cache, must-revalidate, max-age=0`,
  `Content-Disposition: inline; filename="setup-<id>.sh|.ps1"`.
  Registered **public** (no dashboard auth) — Axon's dashboardGuard
  lists `/api/cli-tools/setup` among its public dashboard paths.
- **Generator fidelity proof:** the Go renderers were diffed against
  Axon's own generators executed under node 24 (16 parameter cases
  covering all 15 tools × bash + PowerShell = 32 scripts):
  **all 32 byte-identical.**

## Settings re-score (reads)

`internal/handlers/media/batch5_settings_read.go`,
`batch5_settings_read2.go`, `batch5_settings_cowork.go`,
`batch5_ordered.go` implement the 12 per-tool GET settings routes in
Axon's read shapes (installed detection via PATH lookup with
config-path fallback, per-tool `hasAxonRouter` rules, settings/config
content echoed back preserving JSON key order, JSONC tolerated with
trailing-comma cleanup). `hermes_settings.go` was rewritten GET-only
(~490 → ~150 lines; its YAML builders in `hermes_yaml.go` remain as
library code with no HTTP write path). `pi_settings.go` was rewritten
GET-only in Axon's shape. Retired write routes now answer 405
(hermes/pi/claude settings POST/DELETE — handler gone, method not
registered) and dropped crush/omp settings answer 404 (verified in
smoke below).

## OpenCode family

All six routes ported from the route.ts sources + `src/lib/opencodeSync/*`
into `internal/handlers/dashboard/batch5_opencode_*.go` (schema,
tokens, generator, bundle, catalog, handlers — one responsibility per
file). Storage in the kv store under scope `opencodeSync`
(`preferences`, `tokens`).

| Route | Auth | Notes |
|---|---|---|
| GET/PATCH /api/opencode/preferences | dashboard | PATCH merges over current, then validates; response redacts secret env values to `********`; validation error strings are Axon's verbatim (`Invalid OpenCode variant`, `Invalid MCP server: …`, …) |
| GET /api/opencode/bundle/preview | dashboard | public bundle (no token), same builder as sync |
| GET /api/opencode/sync/bundle | `ocs_` token | 401 `{error:"Unauthorized"}` without/with bad token; touches token `lastUsedAt` on success |
| GET /api/opencode/sync/version | `ocs_` token | `{version, revision, schemaVersion:1}` |
| GET/POST /api/opencode/sync/tokens | dashboard | POST → 201 public record (uuid id, metadata = scalar values only, hash never exposed); tokens are `ocs_` + base64url(32 random bytes), stored as sha256 hex, verified timing-safe |
| PATCH/DELETE /api/opencode/sync/tokens/{id} | **none — by design** | Axon's route.ts carries no dashboard guard on this route; ported as-is (see Deviations) |

- Generator: model catalog from live connections (+ combos) merged
  over the static registry; context/output metadata inference
  heuristics ported 1:1 (gemini 1048576/65536, gpt 400000/128000 with
  gpt-5.4/5.3 → 1050000, claude 200000/32000 with claude-4 → 64000
  output, deepseek/qwen/glm families, default 200000/64000); bundle
  base URL `http://localhost:12711/v1` and default key
  `sk_axonrouter` from Axon's `runtimeDefaults.json`; presets for the
  openagent/slim variants ported verbatim.
- **Hash fidelity proof:** `version` (sha256 of the compact
  `JSON.stringify` of the stable-cloned public artifacts, revision =
  first 12 hex) was cross-checked against Axon's own
  `buildOpenCodeSyncBundle` run under node on an identical 5-model
  catalog + identical preferences (slim variant, remote MCP server,
  secret env var). **All three variants produce identical hashes** —
  slim `7c942159…`, openagent `0101575b…`, custom `08e78938…`.
  (Getting there caught a real bug: the hash input must be the
  *compact* stringify, not the 2-space pretty form used for config
  files; `clisetup.StringifyJSONCompact` now exists for it, and the
  stable-clone key sort approximates `localeCompare` — proven equal
  on every key set these artifacts produce.)

## Cowork MCP

`internal/handlers/media/batch5_cowork_mcp.go` (+ `batch5_ssrf.go`):

- `GET /api/cli-tools/cowork-mcp-registry` — proxies Anthropic's MCP
  registry (`api.anthropic.com/mcp-registry/v0/servers`, commercial +
  gsuite visibility, cursor pagination ≤ 20 pages), keeps only
  direct-connect servers (https, no `mcp.claude.com` hosts, no
  `api.anthropic.com/mcp`, no templated URLs, entries with
  `meta.requiredFields` skipped), dedupes by URL, 1-hour in-process
  cache with `?refresh=1` bypass; upstream failure → 500
  `{error, servers: [], total: 0}`.
- `POST /api/cli-tools/cowork-mcp-tools` — MCP `initialize`
  (protocolVersion `2025-06-18`) + `notifications/initialized` +
  `tools/list` probe, 8 s budget, JSON or SSE responses parsed,
  session id carried; 401/403 → `{requiresAuth: true, tools: []}`.
  URL validation ports Axon's layer-1 `assertPublicUrl`: blocked
  hostnames/suffixes (localhost, .internal/.local/.localhost),
  private/reserved IPv4 ranges (0/8, 10/8, 100.64/10, 127/8,
  169.254/16, 172.16/12, 192.168/16) and IPv6 (loopback, unspecified,
  fe80::/10, fc00::/7, v4-mapped, NAT64); loopback targets allowed
  only for genuinely local requests (loopback peer + loopback Origin,
  and never when `x-axonrouter-via-proxy` is present).

## Dashboard

- New `web/src/components/cli-tools/HostSetupCommand.svelte`: composes
  `<origin>/api/cli-tools/setup/<toolId>` with the page's base URL, a
  user-chosen client API key, and the per-tool model-mapping
  selections; renders the ready-to-paste
  `curl -fsSL "…" | bash` and `irm "…" | iex` commands with copy
  buttons.
- `CliToolsView.svelte` tool modal gains an API-key select (defaults
  to the first key) and per-tool mapping inputs for the 14 non-hermes
  template tools (claude: model + sonnet/opus/haiku/fable +
  maxContextTokens; codex/opencode: model + subagentModel;
  droid/copilot/cowork: modelsList; openclaw/cline/kilo/jcode/qwen/
  deepseek-tui/grok-build: model; amp: none). State resets per tool.
- `HermesToolCard.svelte`: the host-write apply / apply-all / reset
  actions are gone; the card now shows the HostSetupCommand
  (toolId `hermes`) next to the Manual Config section.
- `web/src/api/client.ts`: hermes apply/reset methods and their types
  deleted; `HermesSettingsResponse.has9Router` → `hasAxonRouter`
  (the all-statuses type keeps `has9Router` — that endpoint is
  unchanged by design).

## Verification

- `go build ./...` PASS (heavy gate); `go vet` on `internal/clisetup`,
  `internal/handlers/media`, `internal/handlers/dashboard`,
  `internal/handlers` PASS; gofmt clean on all touched files (the one
  remaining unformatted file, `internal/handlers/dashboard/apikeys_policy.go`,
  is the pre-existing baseline, untouched).
- Frontend: `bun run build` PASS; svelte-check **77 errors / 57
  warnings — exactly the baseline, zero added**.
- Scratch-server smoke (fresh DATA_DIR, requireLogin off): setup
  route 200 + exact headers for claude/codex/opencode/deepseek-tui/
  grok-build/amp, 404 exact-text for unknown tool, ps1 banner correct;
  all 14 settings GETs return Axon shapes (not-installed messages
  verbatim); retired writes → 405, dropped crush/omp → 404;
  preferences GET/PATCH round-trip incl. secret redaction and the
  verbatim 400s; tokens create (201, `ocs_` prefix, no hash in
  response) / list / rename / delete / 404-unknown / 401-on-version
  after delete; sync version == preview version; bundle artifact
  inspected field-by-field (plugins, slim preset agents with
  `axonrouter/` prefixes, MCP entry, env placeholder, inferred
  limits); cowork tools 400s (`url required`, SSRF `URL not allowed`
  via the proxy header) and registry live fetch returning shaped
  entries.
- **Acceptance (master-plan mandatory):** bash scripts for claude,
  codex, opencode fetched from the scratch server with gateway
  `http://127.0.0.1:20130`, key `sk-acc-test`, and explicit mappings,
  then executed in a throwaway HOME. Read-backs:
  - claude `~/.claude/settings.json`: `ANTHROPIC_BASE_URL` = test
    gateway, `ANTHROPIC_AUTH_TOKEN` = `sk-acc-test`, sonnet/opus/
    haiku tiers as chosen, `ANTHROPIC_MODEL` = model param,
    `CLAUDE_CODE_MAX_CONTEXT_TOKENS` = `200000`. ✓
  - codex `~/.codex/config.toml`: `model = "gpt-5"`,
    `[model_providers.axonrouter] base_url` = test gateway (no `/v1`
    suffix), Authorization Bearer header with the chosen key,
    `default_subagent_model = "gpt-5-mini"`. ✓
  - opencode `~/.config/opencode/opencode.json`:
    `provider.axonrouter.options.baseURL` = gateway + `/v1`,
    `apiKey` = `sk-acc-test`, `model` = `axonrouter/claude-sonnet-5`,
    explorer agent = `axonrouter/claude-haiku-4-5`. ✓
  - `format=ps1` for codex inspected as text: AxonRouter PowerShell
    banner, `$env:USERPROFILE\.codex\config.toml` target, backup step,
    same TOML payload. **Inspection only — no Windows host exists in
    this environment to execute it.**
- Screenshots: CLI Tools page + Claude Code modal (composer visible,
  both commands rendered) captured at 1440×1000 and 390×844 against
  the scratch server; layout clean at both widths.

## Blocked / partial

- PowerShell execution is unverifiable here (no Windows host); the
  ps1 *generator output* is byte-identical to Axon's (node diff,
  above), and the script text was inspected — the residual risk is
  PowerShell-runtime behaviour, which is Axon's own shipped logic.
- Cowork registry was verified against the live Anthropic registry
  from this VM; in a network-restricted deployment the route degrades
  to Axon's own 500 shape.

## Deviations

- **15 templates, not 13** — Axon's `cliToolTemplates.js` also defines
  `deepseek-tui` and `grok-build`; both ported (the route serves any
  key). The brief's list was written from an older count.
- **Tokens/{id} PATCH/DELETE are unauthenticated** — that is Axon's
  route.ts as shipped (no dashboard guard); ported faithfully rather
  than hardened unilaterally. Flagged for the parent/owner: if this
  should be tightened, it is a one-line route-group move, but it would
  diverge from the contract.
- **Catalog metadata gaps** — the Go static registry
  (`providers.ProviderModels`) carries alias → model-id lists only,
  no per-model name/label/cost/limit fields, so static-catalog entries
  fall back to `name = id` and omit cost/limit where Axon (with its
  richer static table) may include them. Connection/combo-sourced
  entries are unaffected. Inferred limits still apply in the bundle.
- **`localeCompare` approximated** — the stable-clone sort uses a
  comparator that matches ICU `localeCompare` for ASCII key sets
  (case-insensitive primary, lowercase-first tiebreak); proven
  hash-identical to Axon on all three variants with a mixed catalog.
  Exotic non-ASCII model ids could order differently in theory.
- **Hermes check order** — Go's hermes-settings resolves
  profile → installed; Axon's route checks installed → profile. Kept
  (pre-existing Go behaviour; response content is the same when both
  signals agree, which is the normal case).
- **Pi models.json fallback** — Go reads `~/.pi/agent/models.json`
  then falls back to `~/.pi/models.json`; Axon reads the agent path
  only. Kept (the extra read only fires when the agent file is
  missing/unparseable).
- **Cowork registry name/slug/title** — keys are emitted only when
  non-empty in the Go port's mapping (Axon assigns unconditionally
  and relies on `undefined` being dropped by JSON serialization;
  net effect on the wire is the same).
- **`internal/clientconfig` retained** — see Disposition: no HTTP
  path reaches it after this batch; physical removal is P5 scope.
- **`hermes_yaml.go` write builders retained** as library code (the
  route layer no longer calls them); removal belongs with P5 too.
