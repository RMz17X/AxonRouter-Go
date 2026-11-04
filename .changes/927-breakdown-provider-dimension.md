### Usage Breakdown gains a Provider dimension (owner-ordered divergence from Axon)

- `AxonBreakdownTable.svelte`: the Breakdown dimension select now offers five
  options — Model, Provider, Account, API Key, Endpoint — matching the
  reference control the owner supplied. AxonRouter's own Breakdown table has
  only the other four; the Provider dimension is an owner-ordered addition on
  top of the parity surface (2026-11-04), not an Axon port.
- Implementation is client-side only: the provider dimension reuses the model
  view's leaf map (`stats.byModel`, leaves are model × provider) and regroups
  it by each leaf's `provider`, with the models that ran on that provider as
  the expandable detail rows. No backend change, no new endpoint, no new
  payload field; the Costs/Tokens modes, sorting, expansion persistence, and
  cost-split rules behave exactly as for the other dimensions.
- Verified: svelte-check 77 errors / 57 warnings (baseline, zero added);
  web build green; live proof on the seeded demo data — switching the select
  to "Usage by Provider" regroups the table (openai 20 req expanded into
  gpt-4o-mini 13 + gpt-4.1 7; openrouter 8), totals reconcile with the Model
  view.
