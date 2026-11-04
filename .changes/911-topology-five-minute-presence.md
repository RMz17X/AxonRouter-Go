# Usage Overview topology: five-minute presence rule

Owner rule (Ricki, 2026-11-04): a provider with no requests for five
minutes disappears from the topology automatically. .changes/910
restored the native card but kept the old membership rule — a provider
stayed on the map if it had carried traffic anywhere in the selected
stats period, so an idle provider lingered for hours. This change
replaces membership with presence, in `AnalyticsView` only; the card
component is untouched.

- `topologyProviders` now gates every non-live source on freshness:
  the provider's last activity (per-provider `lastUsed` from stats,
  unioned with locally observed activity — live requests, pulses,
  errors, recent-row timestamps) must be under five minutes old.
  Active requests and the 3-second pulse are present by construction.
  The selected period no longer influences who is on the map; it still
  drives the KPIs, tables, and stream.
- A five-second clock (`topologyNow`) re-runs the derivation so nodes
  expire on time without new data arriving, and an observer effect
  keeps `observedActivity` stamped from the live feeds.
- The connection source keeps its #280 protection (configured-but-idle
  never appears) — tightened, not loosened: "used earlier today" no
  longer counts as present.

Verification: web build green; svelte-check 77 errors / 57 warnings,
exactly the pre-change baseline. Live smoke against the real server,
seeded so OpenAI's last request was 4m20s old, Anthropic's 6m,
OpenRouter's 30s: at load the map showed OpenAI + OpenRouter and never
Anthropic; sixty seconds later, with no reload and no new requests,
OpenAI was gone and OpenRouter remained — while the Recent Requests
feed still listed the aged rows (the feed is history; the map is
presence). Zero console errors. Screenshots:
~/workspace/shots-topo/expire-frameA.png / expire-frameB.png.
