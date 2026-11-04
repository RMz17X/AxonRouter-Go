# Usage Overview: Axon-style topology replaced by 9router-go's native card

Owner decision (Ricki, 2026-11-04): the native provider topology's
animation is better than the Axon-style reimplementation shipped in
.changes/907 — the Overview uses the native component. This is a
deliberate, recorded exception to the Overview content-parity scope:
every other Overview element keeps AxonRouter parity.

- `ProviderTopologyCard.svelte` restored from upstream 9router-go into
  `web/src/components/analytics/` — verbatim except one line: the
  destructured `onRefresh` binding is dropped (upstream declares the
  prop but never reads it; the unused binding was the single
  svelte-check error the restore added). The prop stays in the Props
  interface and callers may still pass it.
- `AnalyticsView` renders it with the upstream prop set
  (`providers` from the existing `topologyProviders` derivation,
  `activeRequests`, `pulseProvider`, `lastProvider`, `errorProvider`,
  `onRefresh` → `loadStats(period)`), in the same Overview grid cell
  the Axon topology occupied. The data machinery (catalog name
  resolution, window-gated connection inclusion) is unchanged — it was
  never removed, only the rendering differed.
- `AxonTopology.svelte` deleted (one home per thing).

Verification: web build green; svelte-check 77 errors / 57 warnings —
exactly the pre-change HEAD baseline, zero added. Live smoke against
the real server with seeded connections + usage: the card renders all
providers around the router node with artwork and zoom controls at
desktop and phone sizes, zero console errors; with real gateway
requests in flight the topology region changes between frames 0.7 s
apart (the card's SMIL pulse rings and edge particles are
activity-gated — an idle map is static by design, as upstream ships
it). Screenshots: ~/workspace/shots-topo/.
