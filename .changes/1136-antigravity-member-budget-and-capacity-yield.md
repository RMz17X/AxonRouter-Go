title: Smart combo — antigravity member budget 3, capacity verdicts yield the member at once
version: v1.10.44

## Fixed
- With Gemini first in the smart combo's hard pool, a request that met
  Google's capacity drought never reached the members below it: the
  per-member account-attempt budget for antigravity was the generic
  10, and every 503 "No capacity available" / 500 draw was classified
  retryable, so the combo rotated antigravity accounts and model
  variants until the request died at its own 10-attempt ceiling
  (measured on staging, 2026-11-12 night: 303 of 546 retried `auto`
  requests never left antigravity, zero antigravity→MiniMax
  transitions, median 120s burned per stuck request). Two changes:
  the antigravity member budget is now 3 (its variant tiers), and a
  new providers.IsModelCapacityError classifier (503 + "no capacity
  available") breaks the member loop immediately in both combo
  loops — a capacity verdict is scoped to the model, not the
  account, so rotating accounts against it is wasted budget, and
  the healthy account is not cooled down.
