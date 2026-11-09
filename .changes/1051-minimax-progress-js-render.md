### MiniMax credits: the progress bar returns — rendered the JS way, from the row's own numbers

- Fragment 1050 reverted the backend to the JS shape but also
  dropped the row's progress bar entirely, overshooting the owner's
  order. Reading the actual JS table
  (AxonRouter's ProviderLimits/QuotaTable.js) settles it: the JS
  renderer has NO credit-balance special case — every row draws its
  bar and percentage from its own reported used/total (the pattern
  CodeBuddy's packs make meaningful), and for the MiniMax Credits
  row `{used: 0, total: <balance>}` that is a full bar at 100%
  remaining. The bar suppression for credit rows was a deviation of
  this Go port, not JS behaviour.
- QuotaTable therefore renders bar + percentage for MiniMax credit
  rows again (the `isMiniMaxCredit` carve-outs), now on the JS data
  basis: the v1.9.65 backend row (used 0, total = live balance, no
  synthesized denominator of any kind — no peak, no fixed grant).
  Other providers' credit rows (deepseek, ollama) keep the port's
  amount-only rendering; fragment 1050's removal of the "N/A" reset
  placeholder for credit rows also stands (the owner asked what it
  was for; a balance has no reset time).
