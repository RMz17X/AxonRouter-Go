### OpenCode free lane (`oc` alias) and media task panel

- `oc` and `opencode` now share the same free-tier gate ledger: a 429 on either
  alias parks the serving proxy pool under one strike counter, and the
  classified-refusal detector recognizes both wire ids.
- The NoAuth free lane rotates proxy pools after a 429 refusal with the
  documented 60/600/1200/1800/2400/7200s ladder, and after a 403 shape refusal
  without a strike, bounded by `EGRESS_ROTATE_MAX`.
- Pinned and combo attempts park the pool that answered a free-tier 429 instead
  of only logging the refusal.
- The media lane recognizes the `oc` alias when attaching the stable session
  header, and the OpenCode stealth ClientHello is pinned to Bun 1.4.3.
- The video example card shows a task card (id, status, model, duration,
  resolution, next step) with a Poll button and the returned video URL.
- A MiniMax H3 `payment_required` / HTTP 402 balance refusal now rotates to the
  next MiniMax connection (bounded at 8 attempts) instead of returning
  immediately; 401/403 keep the single rotation and 5xx is never replayed. A
  402 also sinks that connection in the snapshot ranking.
- MiniMax credits snapshots are stored per connection from dashboard quota
  reads; video create tries the freshest known solvent accounts first (24h
  freshness, richest first) and never falls back to accounts the snapshot
  already marked empty.
- MiniMax Code video creates now dispatch to the Matrix MCP create path before
  the registry `VideoURL` gate, so the dedicated rotation and balance ranking
  are actually reachable instead of falling through to the generic media lane.
- MiniMax video task polls recover the provider and creating connection from the
  task store, so the media panel Poll button works on the same account without
  a manual connection pin; a pinned MiniMax connection is accepted on the query
  path too.
- Balance snapshot writes no longer repeat DDL on every 402 attempt; the
  table and index are ensured once at startup, so a full fleet sweep pays one
  upsert per account instead of two extra schema statements.
