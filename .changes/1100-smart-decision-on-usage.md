# Smart-router decision on the usage page + smart combo member removal (1100)

- Every request served by a smart combo now records the router's decision
  in its request detail (`smartDecision`): the verdict (`easy` / `hard` /
  `fallback`), the source path (selected, switch, retained, strict,
  below-threshold, no-decision), the model it led with, and the router
  confidence/threshold when there was an evaluation. Recorded at every
  applySmartRouting exit and merged into both detail payload builders
  alongside the caller/request-id fields.
- Usage page (Request Details): list rows and the detail modal show an
  AI Easy / AI Hard / Fallback chip next to the model/combo, and the
  detail body gains a Smart Router card with the source and led model.
- Combo editor: removing a model row now also removes it from the
  Easy/Hard pools. A smart combo's saved list is rebuilt from the pools
  on save, so a pooled member previously resurrected after every removal
  — no member of an all-pooled combo (e.g. `auto`) could be deleted.
