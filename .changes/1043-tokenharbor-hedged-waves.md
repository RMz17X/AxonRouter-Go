### TokenHarbor: hedged waves under a 60s admission budget (client-feel tuning)

- Owner directive: for TokenHarbor models the only metric is how fast
  the client feels an answer; backend 429s and spent attempts are free
  collateral because the pool holds ~30K accounts, and there is no
  model substitution. The protections written to spare accounts ran
  against that directive: the sequential path capped TokenHarbor at 2
  account attempts and the executor deliberately never hedged.
- Measured upstream physics (2026-11-09, from hijilaptop): the free
  tier's GPU capacity is one global queue per model whose dominant
  failure shape is silent queueing, not door rejection — a 100-key
  probe answered 14/100 within 45s (success median 19.4s), the rest
  read-timed-out with zero HTTP errors; response headers arrive in a
  ~60s admission tick while first content can lag another ~60s or
  never arrive, so headers are not an admission signal.
- `shouldHedgeProvider` gains a TokenHarbor profile (8 candidates,
  2s stagger, settings overrides still win) and the direct path runs
  `executeHedgedWaves`: waves of fresh candidates until one commits,
  the pool is walked, or the shared admission budget ends the request.
  Fast-failing waves roll immediately into the next wave; identical-
  failure classes (model-fit, unsupported media, model concurrency)
  stop the walk at once.
- Hedged requests for queue-bound providers (any provider with an
  admission budget; today TokenHarbor at 60s) commit on the first body
  bytes, not on response headers, and when the budget dies with no
  winner the gateway cancels everything and returns a 429 that the
  error masker turns into the retryable 503 — so the client retries
  with fresh tickets instead of hanging in silence (the old path once
  left a client without any verdict past 150s). The budget only guards
  the silent admission phase: once a stream flows it is never cut by
  it, however long the generation runs. A manual replica of this
  method measured client TTFT 6.3–34.7s, 3/3 landed, against the
  sequential path's 2/5 in the same conditions.
