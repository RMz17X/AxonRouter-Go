### MiniMax video credits and task panel

- The video example card shows a task card (id, status, model, duration,
  resolution, next step) with a Poll button and the returned video URL.
- A MiniMax H3 `payment_required` / HTTP 402 balance refusal now rotates to the
  next MiniMax connection (bounded at 8 attempts) instead of returning
  immediately; 401/403 keep the single rotation and 5xx is never replayed. A
  402 also sinks that connection in the snapshot ranking.
- MiniMax credits snapshots are refreshed by the auto-claim sweep and after
  paid video/image requests; video create tries the freshest known accounts
  that can cover one generate (>= 800 credits, 24h freshness, richest first)
  and never falls back to accounts the snapshot already marked empty.
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
