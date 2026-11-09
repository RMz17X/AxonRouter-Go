### MiniMax credits: back to the JS shape — balance only, no invented percentage, no N/A

- Fragment 1047's peak-relative percentage for the MiniMax Credits
  row invented a denominator (the highest balance ever read) that
  MiniMax never reports: balances differ per account and per top-up,
  so the bar sat at ~100% and drained by hundredths of a percent —
  a progress display with no information. The owner ordered the
  AxonRouter JS / CodeBuddy pattern instead: progress bars are drawn
  only from provider-reported used/total (CodeBuddy's credit packs,
  MiniMax's own M Plan windows), never from a synthesized total.
- Backend: `applyMiniMaxCreditsPeak` and its two call sites are
  removed. The Credits row returns to the exact upstream shape
  (open-sse/services/minimaxCodeUsage.js): `{used: 0, total:
  <balance>, isCreditBalance: true, currency: "credits"}` — a bare
  balance, no remainingPercentage. (Connection blobs written by
  v1.9.62–v1.9.64 keep an inert `creditsPeak` field; nothing reads
  it anymore.) M Plan window rows are untouched: their percentages
  are provider-reported and keep their bars.
- Frontend (QuotaTable): the `isMiniMaxCredit` carve-outs that drew
  a bar and a percentage for the MiniMax credit row are removed, so
  every credit-balance row renders as an amount again. Credit rows
  also no longer render "N/A" in the reset slot (mobile name row and
  desktop reset column): a balance has no reset time, and the JS
  table leaves the cell empty rather than inventing a placeholder.
