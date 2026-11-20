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
  next MiniMax connection (up to 10 attempts) instead of returning immediately;
  401/403 keep the single rotation and 5xx is never replayed.
