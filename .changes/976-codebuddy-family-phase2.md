# 976 — CodeBuddy family Phase 2: catalogs, credit, intl caps/thinking, CN/WB usage, quota UI

Phase 2 of closing the CodeBuddy-family gaps (audit CB #1–#6, #15–#17;
WB #11–#13). Builds on Phase 1 (fragment 975). JS sources are the
behavioral spec; Go ports contracts, not code.

## 1. Catalogs CN + Intl (CB #1, #2, #4)

- New home `internal/providers/codebuddy_catalog.go`: the three canonical
  lists as package-level slices — `codebuddyCNModels` (33, JS registry
  order), `codebuddyIntlModels` (23), `workbuddyModels` (21).
  `registry_models.go` now references the slices under BOTH the canonical
  id and the alias key (`codebuddy-cn`/`cbcn`, `codebuddy-intl`/`cbai`,
  `workbuddy`/`wb`), so paired keys share one backing slice and can never
  diverge again (CB #4).
- CN counts: `cbcn` 13 → 33, `codebuddy-cn` 14 → 33 (union replaced by the
  JS registry list verbatim, incl. `default`, `deepseek-v4-flash`,
  `glm-4.6`, `hy3-x`, `step-5-preview`, `mimo-v2.6-pro`).
- Intl counts: `cbai`/`codebuddy-intl` 15 → 23. Removed the wrongly-present
  CN-generation rows (glm-5.0, glm-5.0-turbo, glm-5.1, glm-5v-turbo,
  glm-4.7, minimax-m3, minimax-m2.7, kimi-k2.7, kimi-k2.5, hy3-preview,
  deepseek-v4-pro, deepseek-v3-2-volc); added the virtual presets
  (default/fast/balanced/primary/deep-model) and the GPT/Gemini rows.
- Frontend static lists in `web/src/lib/models.ts` (`cbcn`, `cbai`)
  replaced with the same ids and the JS registries' display names.

## 2. Credit metadata (CB #3)

- ONE home: `modelCredits` in `internal/providers/codebuddy_catalog.go`,
  keyed by canonical provider id, values verbatim from the JS registries.
  Absent model = unpriced (Intl/WB `default-model`); explicit 0 = free.
  `providers.GetModelCredit(provider, model)` resolves aliases.
- Four consumers, all reading that one table:
  1. Catalog API — `aggregateProviderModels` (GET /api/provider-models)
     adds `credit` to system entries when priced.
  2. Pill — GET /api/models/caps entries gain `credit`; the provider
     detail grid renders `x{credit.toFixed(2)}` next to the model id.
  3. FREE badge + free-only filter — `isModelFree` consults the table
     first (credit 0 ⇒ free), feeding the existing FREE badge and the
     toolbar's free chip on the detail page.
  4. Combo order — `chat.OrderByCredit` (port of JS `orderByCredit`:
     free first, ascending credit, unpriced last, stable) applied as the
     base order before rotation in both combo fallback handlers, with the
     JS opt-out chain (per-combo `costAware` → global `comboCostAware` →
     on; new fields on `db.ComboStrategy` / `db.SettingsData`). Go's
     smart/tokenmaxxing strategies are exempt — they compute their own
     dynamic ranking and have no JS counterpart in this position; the
     capability reorder still runs last, as upstream.
- Deliberately NOT surfaced on /v1/models: the OpenAI-style list carries
  capabilities/pricing upstream, never registry credit; its typed
  `ModelInfoObject` has 9 construction sites, most on combo/custom/live
  paths where credit does not apply.

## 3. Intl capabilities + thinking (CB #5, #6)

- `capabilities.go`: full `codebuddy-intl` map in `providerCapabilities`
  from `open-sse/providers/capabilities.js` (19 catalog rows) plus the
  five virtual-preset rows merged from the JS registry layer (the merge
  the workbuddy block already performs — Go has no registry caps layer,
  and without preset rows the GetThinkingLevels caps gate swallows the
  preset patterns). Includes the all-false `claude-opus-5` /
  `claude-sonnet-4.6` guard rows (resolve here, block the global
  *claude* patterns).
- The stale `providerTokenLimits["codebuddy-intl"]` block is removed: it
  borrowed CN's figures (393216 output for deepseek-v4.1-flash) and
  outranked the capability table, which now declares Intl's own numbers
  (128000) from the Intl catalog.
- `thinking_levels.go`: the intl block (13 patterns) sits with the other
  family blocks ABOVE the unqualified `*deepseek-v4.*` glob, mirroring
  JS order; the dead trailing entry (`deepseek-v4*` → [low, high,
  xhigh]) is deleted. Intl values verbatim from JS — presets
  [medium]/[high], `deepseek-v4*`/`glm-5.3*`/`kimi-k2.8-preview` →
  [low, high, **max**], `gpt-5.6-*`/`gpt-6-astra` → [none…max],
  `gpt-5.5`/`gpt-5.4` → [low, medium, high, xhigh]. NOT normalized
  against CN/WB (which use xhigh for deepseek) — each provider keeps its
  own JS values.

## 4. Usage fetchers CN + WorkBuddy (CB #15, WB #11)

- `usage_providers.go`: the existing intl port is generalized into
  `fetchCodeBuddyUsage(ctx, providerID, url, headers, …)` with thin
  wrappers `fetchCodeBuddyCnUsage` / `fetchCodeBuddyIntlUsage` /
  `fetchWorkBuddyUsage`, mirroring JS's one shared billing handler
  (`open-sse/services/usage/codebuddy-cn.js`) with per-provider hosts
  (CN copilot.tencent.com, Intl www.codebuddy.ai, WB www.workbuddy.ai)
  and per-registry static identity headers (CN: CLI shape; WB:
  WorkBuddy/5.5.6 set incl. X-Agent-* and X-Domain; Intl: the existing
  port's IDE headers, untouched — a pre-existing divergence from the JS
  registry's CLI headers, left as-is).
- Parsing parity is inherited from the intl port, which already mirrors
  JS exactly: precise-string fields preferred over numeric, refill vs
  bonus split by the >2-day CycleEnd vs DeductionEnd gap, refill rows use
  the Cycle* balances with cadence names (Daily ≤1.5d, Weekly ≤10d, else
  Monthly; duplicates suffixed), bonus rows use lifetime Capacity* and
  are numbered by soonest cycle end, plan from the first refill's
  PackageName/SubProductName. Message strings keep JS's quirk of naming
  "CodeBuddy CN" literally in the HTTP-status and payload-error branches
  for all three providers; the credential/transport branches name the
  actual provider id.
- Dispatch: `codebuddy-cn` and `workbuddy` cases added to
  `fetchProviderUsage`; the generic lock-derived fallback now only runs
  on genuine fetch failure, as before.
- Quota normalizer (`web/src/components/quota/types.ts`): `workbuddy`
  joins the `codebuddy-cn`/`codebuddy-intl` case, so WorkBuddy quotas
  render with the same recurring/expires semantics (the Phase 1
  deferral, unblocked now that the fetcher exists).

## 5. Quota UI extras (CB #16/#17, WB #12/#13)

- Total Credit card: `isCreditPoolProvider` + `getTotalCredit` ported
  into `quota/types.ts` (only finite positive totals participate; used
  clamped per pack; remaining %, pack count). `QuotaTable.svelte` gains
  a `provider` prop and renders the card above the per-pack rows for the
  three credit-pool providers; `QuotaTrackerView` passes `conn.provider`.
- Credits button: Go's connection row carries its actions in a menu
  (not JS's icon cluster), so the on-demand fetch lands as a "Show
  credits" / "Refresh credits" menu item (credit_card icon) on
  credit-aware providers in ProviderDetailView, with the result rendered
  inline in the badge row exactly as upstream ConnectionRow does:
  "Credits left: X / Y (resets …)", or "Credits: <message>". Aggregation
  mirrors JS fetchQuota (skip unlimited, remaining = Σ max(0, total −
  used), earliest resetAt). Uses the existing
  `api.getConnectionUsage(id, force=true)`.

## Mechanical test-file fixes (listed per the no-test-suite rule)

- `internal/providers/model_limits_precedence_test.go`:
  TestProviderTokenLimits_CodebuddyIntl expectation 393216 → 128000 —
  the row it pinned borrowed CN's figure through providerTokenLimits;
  the Intl capability table (JS capabilities.js) declares 128000.
- `internal/handlers/dashboard/codebuddy_cline_catalog_test.go`: intl
  cases' wantID minimax-m3 → minimax-m3.1-flash — minimax-m3 is not in
  the JS Intl registry; the test's intent (intl serves its own
  catalogue) is unchanged.

## Deferrals / out of scope

- Combo picker (ModelSelectModal equivalent) price pill: the Go picker
  builds from the static `models.ts` bundle, which must not become a
  second home for credit values; the pill ships on the provider detail
  page (JS ModelRow's home) and via both catalog APIs instead.
- No costAware toggle in the combo editor UI: JS exposes it inside its
  Smart Routing section; the Go settings fields and resolution chain are
  in place, the editor control is a separate UI change.
- Phase 3 items untouched (bulk-JWT UI, intl backfill, connection
  probes, error-classification numbers, executor minors).
