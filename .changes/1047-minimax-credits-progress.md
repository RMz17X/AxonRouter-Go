### MiniMax credits get a progress bar (peak-relative), like Antigravity's quota rows

- The Quota Tracker drew Antigravity windows as progress bars with a
  percentage but showed a MiniMax account's credits as a bare amount:
  the table deliberately suppresses the bar and the percentage for
  credit-balance rows, and MiniMax reports only a balance — no grant
  total, no consumed amount — so no honest percentage existed.
- The usage read now maintains a per-connection reference on the
  connection blob (`creditsPeak`): the highest balance ever read for
  that connection, raised only by a live read that beats it. The
  Credits row is rewritten against the peak (total = peak,
  used = peak − balance, remaining = balance,
  remainingPercentage = balance / peak × 100), so the share is real:
  it drains as credits are spent and refills when a top-up raises the
  peak.
- The quota table renders the bar and the percentage for MiniMax
  credit rows (other credit-balance providers are untouched), and the
  amount line shows the current balance via the row's `remaining`
  field, falling back to `total` for providers that do not set it.
