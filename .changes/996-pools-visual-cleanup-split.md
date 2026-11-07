# 996 — Proxy Pools: visual cleanup and the view split into components

## Symptom

The pools page read as messy (owner report): each row carried up to ten
badges plus four emoji exit chips, the latency badge used emoji glyphs,
filters mixed three different control styles, the same test action had
three names ("Test All" / "Health Check" / "Test All Nodes") and two
icons, the Deploy Relay menu was a hand-rolled white/zinc dropdown
sitting next to the shared Menu component, and everything — three tabs,
eight dialogs — lived in one 2,762-line file.

## Fix

Frontend only:

- Row diet: the four relay badges become one "{Type} relay" badge; the
  bound count becomes plain muted text; the four exit chips (with 📍/🏢
  emoji) become one mono line — exit IP · city, country · ASN · ISP.
  The latency badge drops its ⚡/⏳/🐢 glyphs (colour thresholds kept).
- One vocabulary: the test action is "Test All" / "Test Selected"
  everywhere with the single `science` icon. The group filter joins
  status and type on the shared CountedSelect. The Deploy Relay dropdown
  is restyled onto theme tokens with monochrome icons and rendered from
  one item list instead of six hand-written buttons (it keeps its
  descriptive trigger — the shared Menu's visible label is only "Menu").
- The view is split along its real seams into
  `web/src/components/proxypools/`: `PoolRow.svelte`,
  `GroupsTab.svelte`, `FitnessTab.svelte`, and `RelayModals.svelte`
  (the Deploy Relay menu + all six deploy dialogs and their handlers;
  its only link back is an `onDeployed` refetch callback). The shared
  `formatDateTime` and the `GroupSummary` type move to
  `proxypools/helpers.ts`. ProxyPoolsView drops from ~3,050 lines
  (post-993–999) to ~1,490; RelayModals stays whole at ~1,080 because
  the six deploy dialogs share one progress idiom that reads best in
  one place.

## Verification

- svelte-check with `--tsconfig ./tsconfig.app.json`: 89 errors /
  56 warnings — identical totals to the pristine baseline; the two
  diagnostics touching these files (a Badge tone complaint and an
  unused `_`) are the original monolith's own, carried verbatim.
- Web build succeeds.
