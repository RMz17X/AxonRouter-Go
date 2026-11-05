## Proxy resolution readout: a binding that resolves to nothing is now visible

Every proxy-binding bug in this area shared one shape: the dashboard
showed a selection as set while the resolver matched zero pools and
requests silently went direct. The fix is a readout that shows the
resolution itself, next to the controls that set it.

- New read-only endpoint GET /api/providers/proxy-resolution?id=<provider>
  (registered in both route tables). It derives the selector from the
  stored provider strategy exactly as the chat resolver does — same
  strategy-key walk (id, alias, canonical), same sentinels, same legacy
  bare-type heal — and matches candidates with the production
  Repo.ActivePoolsByTarget. It reports the binding kind (group / type /
  pool / rotation / direct / none), the selector, the active candidate
  pools, and whether it resolves at all. A small exported
  db.IsKnownProxyType wrapper keeps the heal's type set in one place.
- A shared ProxyResolutionNote component renders one line: the resolved
  pool names and count, or an amber warning — "matches no active pool —
  requests will go direct" — when the selection resolves to zero. It
  sits under the provider Proxy Lock (connections detail) and inside
  the NoAuth proxy card, refreshing after every save.
- While wiring the card: its save now deletes both rotation spellings,
  not only rotateStrategy. The stored rotateStrategy key is the live
  one (the settings parser maps it onto the resolver's
  ProxyRotateStrategy field); a stray proxyRotateStrategy key is never
  parsed back, but leaving it would keep a phantom setting on display.
  A card save expresses the complete group/type/none choice, so
  neither spelling may survive it.
