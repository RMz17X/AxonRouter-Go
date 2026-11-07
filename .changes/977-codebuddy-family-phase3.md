# 977 — CodeBuddy family Phase 3 (final): bulk-JWT UI, intl identity backfill, userinfo probes, error-classification alignment, SSE finish_reason repair

Phase 3 of closing the CodeBuddy-family gaps (audit CB #18–#26, WB #9,
#10, #14–#19; CB #8). Builds on Phase 1 (fragment 975) and Phase 2
(fragment 976). JS sources are the behavioral spec; Go ports contracts,
not code.

## 1. Bulk JWT UI (CB #24, WB #9)

The route POST /api/oauth/{provider}/bulk-jwt existed with no frontend.
Now it has the dashboard surface upstream has:

- New `web/src/components/connections/BulkImportJwtModal.svelte`
  (upstream BulkImportJwtModal.js, in this codebase's modal idiom):
  paste box, the accepted formats, and the per-import result exactly as
  upstream renders it — "✓ N added · ⊘ N duplicates skipped · ✗ N
  failed" plus the per-failed-item "[index] error" list. Entries with
  both tokens become OAuth connections with auto-refresh (server-side
  behavior, unchanged).
- `ProviderDetailView.svelte`: a "Bulk Add" button (playlist_add) in
  the Connections toolbar, gated to the three family providers via
  `supportsJwtBulkImport` (upstream ConnectionsSection gating), and the
  modal mounted alongside the other connection modals; a successful
  import refreshes the list.
- `web/src/api/client.ts`: `api.bulkImportJwt(providerId, text)` +
  `BulkJwtImportResult` / `BulkJwtImportEntry` types.
- Add-connection bulk tab (upstream AddApiKeyModal behavior): for the
  family providers a JSON-shaped paste now routes to bulk-jwt instead
  of the per-key plan — `isJsonAccountPayload` → POST bulk-jwt and
  surface {success, failed, skipped}; JSON-shaped but token-less →
  "This JSON has no accessToken/refreshToken. Check the account export
  shape." The detectors are ported into `web/src/lib/bulk-add.ts`
  (bulkAdd.js), including the concatenated-pretty-JSON token-field
  fallback; plain `name|token` lines still take the per-key plan.

## 2. Intl identity backfill (CB #25)

Upstream's run-once `backfillCodeBuddyIntlIdentity`
(src/lib/oauth/providers/index.js), called from GET /api/providers and
GET /api/providers/client, repairs codebuddy-intl oauth rows that hold
an access token but no email/displayName, or still carry a generic
"Account N" name — the identity is recovered from the stored JWT.

- Hook: `DashboardHandler.backfillCodebuddyIntlIdentity`, called at the
  top of `HandleGetProvidersClient` — the handler serving those exact
  two routes (`internal/handlers/dashboard/codebuddy_intl_backfill.go`).
  This is the repo's lazy-repair idiom (cf. fragment 972's identity pin
  backfill on dashboard reads).
- Run-once per process behind a mutex, with upstream's reset-on-failure
  semantics: a pass that cannot read or write stays pending and the
  next list call retries it. A failed pass never fails the list call.
- Extraction precedence matches the family's device-save helpers
  (fragment 975): email → preferred_username → sub; displayName → name
  claim → given+family → email local part. Rename target is
  email || displayName, only for /^Account \d+$/ names.
- Write: `db.Repo.SetConnectionCodebuddyIntlIdentity` fills only empty
  fields (data.email, data.displayName, the email column, optionally
  the name) in one UPDATE — so the pass is idempotent by construction:
  rows already carrying identity are never in the patch set, and a
  second pass finds nothing to do. WorkBuddy and CN have no upstream
  backfill; none was invented.

## 3. Connection verification probes (CB #26, WB #10)

Upstream testUtils.js probes codebuddy-intl and workbuddy at the
Keycloak userinfo endpoint of the realm the token's `iss` claim names
(fallback realms www.codebuddy.ai / www.workbuddy.ai), marked
refreshable. Go's probe table had intl as a {tokenExists:true} stub and
workbuddy missing entirely.

- `connection_probe.go`: both providers now carry a real config —
  buildURL deriving `<iss>/protocol/openid-connect/userinfo`, GET,
  Bearer, `refreshable: true` — so they ride the file's established
  machinery (connection proxy, refresh-on-401 with the family's real
  refreshers from fragment 975, token persistence).
- The URL builder lives in `connection_probe_codebuddy.go`: `iss` is
  used only when it starts with https:// (trailing slash trimmed),
  exactly upstream's guard.
- CN stays {tokenExists:true} — upstream has no userinfo probe for it.
- ToS-disabled rows stay protected by fragment 973's guard: the probe
  still never dials for them.

## 4. Error classification alignment (CB #18–#23, WB #14–#19)

The old Go readings sentenced accounts on bare text: "restricted" /
"unusual activity" → 7 days at ANY status for the family, 14018 → a
generic 2 minutes, "request illegal" → an intl-only backoff. All
replaced by upstream's gated verdicts. New classifier fields carry what
a substring rule cannot: `ErrorRule.Exhausted / ModelScoped /
RequestLevel` and `ErrorClassification.Exhausted / ModelScoped`.

### Gated verdicts (new `internal/providers/errorclassify_codebuddy.go`)

Consulted before ErrorRules, where upstream puts them:

- **11140** (checkFallbackError's guard, code-gated — upstream gates on
  the code literal, which only the family emits): HTTP 403 + code 11140
  + restriction wording (WB_ACCOUNT_RESTRICTION_RE) → **2h, account
  lock, exhausted**. Bare 11140 → **5 min, account lock, NOT
  exhausted**. A safety wording in the body vetoes the guard, and the
  request-level safety rules decide instead. The classifier receives
  the extracted error text, whose "(code 11140)" suffix (and the raw
  JSON body shape) both match WB_11140_CODE_RE.
- **Family 429 sentences** (markAccountUnavailable's 429 block,
  provider-gated, applied in upstream's order — a later check may
  replace an earlier verdict):
  - "model busy" text or a bare 14003 token → **model-scoped 5s**,
    flat, backoff level reset to 0.
  - Throttle wording (too many requests | rate.?limit exceeded | rate
    limited | usage exceeds frequency limit) with NO quota wording →
    **model-scoped 2 min**, never exhausted.
  - "credits exhausted" / "insufficient credits" text or a bare 14018
    token → **account-wide, exhausted, 7 days** (the direct path).
    Upstream keys on the wording; the bare code is its constant
    companion on the wire, so either claims the verdict.

### Rule-table changes (`errorclassify.go`)

- **Generic credit family** → upstream ERROR_RULES exactly: the 16
  texts (insufficient_user_quota, 预扣费额度失败, 用户剩余额度,
  用户额度不足, "user's credit limit is insufficient", "quota is
  running low", credits exhausted, insufficient credits, insufficient
  balance, out of credits, quota reached, quota exhausted,
  insufficient_quota, insufficient quota, exceeded your current quota,
  monthly quota exceeded) → **30 days, exhausted** (new
  cooldownMonthlyExhausted), placed before the older 1-hour quota
  spellings so the request path's verdict wins on overlapping texts.
  The 1-hour spellings ERROR_RULES does not carry (insufficient credit
  singular, credit exhausted singular, no remaining credits, quota
  exceeded, monthly limit, frequency limit, payment required, free
  allowance, credit balance is too low) are unchanged.
- **11158** → one rule: status 400 only, **cooldown 0, model-scoped**
  (the plan gate: skip the member/model, the account stays healthy).
  The duplicated 2-minute copies are gone.
- **Request-level rules, global** (upstream ERROR_RULES):
  "did not pass the safety review", "safety review", "request illegal",
  "content filter" → no fallback, no lock, no cooldown — for
  codebuddy-cn and workbuddy too (WB #19). "conversation id missing"
  travels with them: the current upstream ERROR_RULES carries no entry
  for it (verified by grep), and its old intl-only backoff reading
  parked accounts over a request fault, so it takes the same
  request-level verdict as its sibling instead of a distinct invented
  one.
- Deleted: all bare-text family restriction rules (7d at any status),
  the generic "unusual activity"/"restricted" 7d+30min copies, the
  family "11140" substring rules, the generic 14018 2-minute rules,
  and the duplicated rule blocks.

### Consumers

- **Exhausted representation** — no new flag: Go's status model already
  derives `quota_exhausted` from the stored lastError text
  (`EffectiveStatusWithError` via `LooksLikeQuotaExhausted`), checked
  before the stale-marker recovery so a spent account stays exhausted
  after its timer lapses. `connection_status.go` itself needed no edit;
  the vocabulary in `errorclassify.go` gained the two family markers
  whose texts carry no credit wording — "14018" and "restricted due to
  unusual activity" — both Tencent-only strings.
- `cooldownSecForClassification` (retry_after.go): an Exhausted
  verdict now rides the banned class's unclamped path (a 7d/30d
  sentence must not be cut to the 72h reset ceiling or replaced by a
  429's Retry-After), and a ModelScoped verdict returns the
  classifier's exact duration (the flat 5s busy sentence must not be
  replaced by a reset reading).
- Scope: both failover loops (fallback.go, combo.go) now honor
  `cls.ModelScoped` ahead of the isModelScopedError text guess — the
  gated busy/throttle bodies carry no quota text for the guess to see,
  and parking the account for them was the bug being fixed.
- fallback.go: a `!ShouldFallback` classification now excludes the
  connection for the turn with NO lock writes, mirroring the combo
  path's standing guard (request-level refusals at retryable statuses
  previously still wrote account state).
- **Combo member attempt cap** (combo.go, both member loops):
  `comboMemberAttemptCap` returns 1 for codebuddy-cn / codebuddy-intl /
  workbuddy (upstream COMBO_MEMBER_ACCOUNT_ATTEMPTS_BY_PROVIDER — the
  burst is what the vendor's risk control answers with 11140) and the
  standing 10 for everyone else. Only attempts that actually reach the
  upstream count; a connection skipped as already-locked was never
  dialled and does not spend the budget.

## 5. Executor SSE repair (CB #8)

Upstream's streaming passthrough (open-sse/utils/stream.js) rewrites
CodeBuddy's `finish_reason: ""` to null on chunks and forwards only
the first [DONE]. Go now does the same on the family's streaming path
only: `codebuddySSENormalizer` (`internal/proxy/executor/
codebuddy_sse.go`) wraps the stall reader at forwardCodebuddy's single
stream call site — all three brands, one place — and transforms line
by line (never buffering the stream): a data chunk whose choices carry
an empty finish_reason is re-serialized with null; every other line,
including the terminal chunk, passes through byte-identical; duplicate
`data: [DONE]` lines are dropped after the first. The shared
execSSEStream/sseStream and every other provider's stream are
untouched.

## Deliberate non-changes

- **CB #7 X-Domain**: UNVERIFIED which value the upstream accepts —
  both sides left exactly as they are.
- **CB #9 conversation-UUID shape**: Go keeps its session-derived
  conversation ids; WorkBuddy already uses fresh ids per JS.
- The direct (non-combo) loop's treatment of non-retryable 400s is
  unchanged for all providers; the 11158/safety verdicts are carried by
  the classifier and the combo consumer, and a 400 never reaches the
  direct loop's locking branch.

## Remaining known deferrals (from phases 1–2)

- Combo-picker credit pill (phase 2 deferral).
- costAware combo-editor toggle (phase 2 deferral).

## Verification (direct checks, no unit suites)

- `go build ./...` and `go vet` on all touched packages: clean.
- Classifier probe program (26 JS-shaped payloads, raw-JSON and
  extracted shapes): 403+11140+restriction → 2h exhausted; bare 11140 →
  5m not exhausted; 11140+safety → request-level; 14018 text/bare @429
  family → 7d exhausted; credit texts off the direct path → 30d
  exhausted; model busy / bare 14003 @429 → 5s model-scoped, backoff
  reset; throttle wording → 2m model-scoped; throttle+quota wording →
  not throttled (30d exhausted via the generic rule); 11158 @400 → 0
  model-scoped; safety/request-illegal/conversation texts → no
  fallback, no cooldown, all providers incl. workbuddy; bare
  restriction text without the gated shape → generic status rules (the
  7-day reading is gone).
- SSE probe through the real ForwardCodebuddyCN against a fake
  upstream: "" → null on non-terminal chunks, terminal "stop" chunk
  byte-identical, duplicate [DONE] collapsed to one.
- Boot smoke against a throwaway database on the local staging
  PostgreSQL: gateway healthy; backfill patched the seeded
  identity-less codebuddy-intl row (email + displayName from the JWT,
  "Account 3" renamed to the email) and left identity-carrying, CN and
  WorkBuddy rows untouched; a second list call changed nothing
  (idempotent); bulk-jwt answered the client's exact request shape
  (JSON paste → created; same token again → skipped duplicates;
  unsupported provider → clean error); the intl and workbuddy test
  endpoints dialed the iss-derived userinfo URL while CN kept its
  instant tokenExists verdict.
- svelte-check: 89 errors / 56 warnings — identical to the pre-phase
  baseline, none in the touched files. `bun run build` succeeds.
- Mechanical test-literal updates (behavior changed by this fragment):
  `errorclassify_codebuddy_test.go` rewritten around the gated
  verdicts; `errorclassify_test.go` credit-exhaustion test split into
  the 30-day ERROR_RULES class and the retained 1-hour class.
