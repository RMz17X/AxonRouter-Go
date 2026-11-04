# Usage Overview: AxonRouter parity

The Usage **Overview** section now mirrors AxonRouter's Usage Overview tab —
content, layout, and live behaviour — while the Cache Analytics, Compression
Analytics, and Details sections are unchanged. The Overview's old
arrangement-driven panels are gone: the layout is fixed, with Axon's three
sub-tabs (Overview / Breakdown / Combo Routing), its five-option period
segmented control (Today / 24h / 7D / 30D / 60D), and its five KPI cards
(Requests with the failed count, Input Tokens with the non-cached note,
Cached Tokens with the cache-hit share, Output Tokens with the output share,
Estimated Cost with the token-share In/Out split).

New components under `web/src/components/analytics/Axon*.svelte`:

- **Overview sub-tab** — `AxonKpiCards`; `AxonTopology` (SVG ellipse around
  the router node, electric-beam edges for active providers, provider
  visibility = active + 5-minute retention with an idle pill, legend,
  pan/zoom); `AxonRecentRequests` (480px card); `AxonRequestStream`
  (per-minute block grid over the last 30 minutes); `AxonRealtime`
  (live banner + active-requests modal, Success/Failed filters, account
  filter, search, error-trace modal deep-linking to the Details section via
  an `open-request-details` window event that AnalyticsView now answers).
- **Breakdown sub-tab** — `AxonVolumeGrid` (Axon's waffle "Volume Grid",
  Tokens/Cost modes, 12 rows, 31-column folding) and `AxonBreakdownTable`
  (Model / Account / API Key / Endpoint dimensions fetched per-dimension via
  `?views=`, sortable headers, expandable groups persisted in localStorage,
  100-row detail cap, Axon's cost allocation — cached tokens peeled out of
  input before the token-share split — and its per-dimension empty
  messages).
- **Combo Routing sub-tab** — `AxonComboRouting` (grouped combo table with
  the `auto-switch` summary rows, share badges, and Axon's latency-suffix
  asymmetry between summary and member rows).

Backend additions:

- `GET /api/combos/analytics?period=` (`internal/handlers/combos_analytics.go`)
  aggregates the request-details ledger by (combo, model, provider) into
  Axon's `{members, combos}` shape; the ledger's status column is exactly
  `success`/`error`, so the split is exact.
- Usage stats gain `last10Minutes`: 30 zero-filled per-minute request
  buckets ending at the current minute, computed for every period from
  `usageHistory` timestamps.
- `usagetracker.RecentRequest` gains `endpoint`, `apiKey` (always the
  masked form — raw keys never ride the struct), `account`, and `isStream`;
  the history-seeded rows and both chat `PushRecent` sites now fill
  endpoint/masked key/account. `isStream` is populated nowhere yet:
  `UsageLogInfo` carries no stream flag, so the field stays absent on live
  rows until the dispatch layer grows one.

Removed: the five replaced Overview components (`SummaryKpiCards`,
`ProviderTopologyCard`, `UsageBreakdownTable`, `UsageChart`, `ComboFlowCard`),
the Overview entries of the panel-arrangement system (`usagePanel.ts` —
stored arrangements referencing them are normalised away), and the topology
row-pairing in `usagePanelPlan.ts`. Settings no longer offers arrangement
controls for panels the Overview does not draw. Known follow-up left for the
maintainer: `usagePanelPlan.test.ts` still exercises the removed overview
panel ids.

Deliberate deviations, all forced by data this build does not have: the KPI
cost card has no "Rates" link (there is no pricing settings page); the
Realtime table omits Axon's Stream/JSON pills, egress select, and
Capability/Type columns (completed detail rows carry no stream flag, egress,
or endpoint); the active-requests modal shows no STREAM/JSON chip for the
same reason.

Bundle (AGENTS.md §6.F, vs the reskin commit): dist stays 4.5M; CSS
133,146 → 131,814 B raw (brotli 16,960 → 16,913); JS 1,515,336 → 1,546,449 B
(+2.1% — the Axon components are richer than the five they replaced).
svelte-check: 77 errors / 57 warnings vs 78 / 59 on pristine HEAD — zero
added; the ratchet baseline (75) predates the fork merge and was already
stale before this change, so it is left untouched. Go: `go build ./...`
passes; no test suites were run.
