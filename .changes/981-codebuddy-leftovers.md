# CodeBuddy leftovers: combo-picker credit pill, costAware editor toggle, CN X-Domain verdict

Closes the three deferrals fragment 976 recorded.

## (a) Credit pill in the combo picker

Phase 2 left the picker without the credit pill because the picker builds
its model list from the static `web/src/lib/models.ts` bundle, and copying
credit values there would create a second home for numbers that belong to
the one table in `internal/providers/codebuddy_catalog.go`. The pill now
reads the values from the server instead:

- `pickerExtras.ts` `loadPickerExtras` fetches `GET /api/models/caps` —
  the endpoint fragment 976 already wired `credit` into — for exactly the
  three providers whose caps can carry a price (`codebuddy-cn`,
  `codebuddy-intl`, `workbuddy`, exported as `CREDIT_CAPS_PROVIDERS`).
  Fetching caps for every catalog provider would be ~100 requests per
  picker open for data only these three have. Each fetch fails soft like
  the other extras, and the results land in
  `ResolvedPickerExtras.modelCaps`, keyed `"<provider>/<model>"`.
  `PickerExtrasSource.getModelCaps` is optional so existing test doubles
  keep working.
- `pickerData.ts` threads the map through: `PickerExtras.modelCaps`,
  `PickerModel.credit` / `PickerModel.free` (flattened scalars, like the
  caps booleans, so pill props compare by value), attached by
  `creditFields()` at all three catalog push sites (bundle models,
  customs, aliases). Node groups carry no credit — the family providers
  are catalog providers, not nodes.
- `ModelPill.svelte` renders the same two forms as the provider detail
  grid (`ProviderDetailView.svelte`): a FREE badge when the server marks
  the model free, otherwise `x{n.toFixed(2)}` with the "Credits per
  request" tooltip. On the selected (brand-filled) pill the badge/pill
  switches to a white-on-brand variant to stay readable.

The static bundle stays free of credit values; the backend table remains
the only source.

## (b) costAware toggle in the combo editor

The backend half shipped in fragment 976 (`db.ComboStrategy.CostAware`,
global `comboCostAware`, resolution per-combo → global → on, ordering in
`chat.OrderByCredit`); only the editor control was missing. It now sits
on `ComboCard.svelte` beside "Respect stored order" — the card is where
this dashboard keeps per-combo strategy controls — as a "Cheapest first"
checkbox with the JS dashboard's helper text as its tooltip ("Cheapest
first — tries free (x0.00) then low-credit members before pricey ones",
AxonRouter `SmartRoutingSection.js`).

Semantics mirror the JS combo modal exactly: the checkbox reads
`strategyInfo.costAware !== false` (an absent flag inherits the global,
whose default is on) and writes an explicit boolean in both directions
through the existing settings home — `CombosView.handleToggleCostAware`
merges `{ costAware }` into `settings.comboStrategies` and saves via
`api.patchSettings`, the same path as every other strategy field.
`ComboStrategyInfo` gains the `costAware?: boolean` field. Visibility
follows the stored-order rule (hidden for the smart strategy, which
orders members itself). No backend change.

## (c) CN X-Domain live probe — BLOCKED, no account available

Verdict: **blocked-no-account; the value stays UNVERIFIED and unchanged.**

The probe needs an existing codebuddy-cn connection with a token in the
local staging database. Direct query of the local staging PG
(`router9_staging` on 127.0.0.1:15432, the only database this work is
allowed to touch — the hijilaptop staging instance is off-limits) shows
the `providerConnections` table is completely empty: zero rows for the
whole CodeBuddy family, zero rows overall. No CN connection exists to
probe with, and creating accounts was explicitly out of scope.

Go's CN executor therefore keeps sending `X-Domain: www.codebuddy.cn`
while the JS dashboard sends `copilot.tencent.com`; which value the
upstream accepts remains unknown. The probe recipe for whenever a CN
connection exists in a reachable database: one minimal authenticated
request per candidate value through the connection's own proxy, token
never printed, record which value upstream accepts; if both work, leave
the value unchanged.

## Verification

- svelte-check (`--tsconfig tsconfig.app.json`): 89 errors / 56 warnings
  on this tree and the identical totals on a pristine `ffc7167a`
  worktree; the normalized error sets (file + message, line numbers
  stripped) are byte-identical between the two — zero new errors, zero
  removed. `make vet-svelte`'s ratchet still reports failure only
  because its pinned baseline (80) is stale against the tree's
  long-standing 89, the same gap fragments 975–977 recorded.
- Go build + vet clean (frontend-only change set for this fragment; the
  embedded bundle is rebuilt at release time).
- No unit-test suites run (standing rule); the picker extras source
  method is optional precisely so the existing picker test doubles keep
  compiling unchanged.
