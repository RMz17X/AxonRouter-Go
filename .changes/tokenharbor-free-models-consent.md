### TokenHarbor free-model consent refusal

- A TokenHarbor `403` carrying `free_models_disabled` (or the "confirm
  free-model data sharing" wording) is now its own classification instead of
  falling through to the generic transient cooldown. The account and its key
  are valid — only the operator's dashboard consent is missing — so the
  account is parked for six hours rather than disabled, and every free model
  on it stops spending a 403 ticket per sweep.
- The consent cooldown wins over `Retry-After` and over any reset string in
  the body, because the refusal only clears when a human clicks consent; the
  hedged-candidate path parks the account the same way.
- `cooldownSecForClassification` now resolves the provider alias before the
  TokenHarbor branch, so `th`/`thh` connections take the same cooldown path as
  the canonical id.
- The hedged-candidate recorder parses the upstream body once instead of once
  per predicate, and the triplicated email-verification branch is a single
  check.
