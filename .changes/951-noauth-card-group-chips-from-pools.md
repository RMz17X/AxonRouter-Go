# NoAuth card: group chips must come from the pools, not /api/proxy-groups

**Symptom.** On the NoAuth proxy card shipped in 950, the group chips were
built from `GET /api/proxy-groups`. That list mixes two things that are
not pool-group names: the DEFAULT groups are type-backed (key is the type
name, e.g. "Cloudflare Relay" with key `cloudflare`, and their
activeCount is counted by pool TYPE), and the CUSTOM groups are
membership-backed entities (a name plus a list of pool IDs). The card
encoded every entry as the selector `group:<key>`.

**Root cause.** The chat resolver's `group:<name>` selector
(`Repo.ActivePoolsByTarget`) matches one thing only: the pool rows' own
`group` string field. It never expands custom-group membership and never
treats a default group's key as its type. So a chip could display
"Cloudflare Relay — 3 active" (three pools of TYPE cloudflare) while its
selector resolved only pools whose group field literally read
"cloudflare" — usually zero, which in non-strict mode silently falls
back to a direct connection behind a UI claiming the provider is
proxied.

**Fix.** `NoAuthProxyCard` no longer fetches /api/proxy-groups. Its group
chips are derived from the pools list it already loads: distinct trimmed
`group` values with active-pool counts — the same derivation the rest of
the dashboard (provider detail group options) uses, so every chip shown
is a selector that resolves. The type-backed default groups remain
reachable through the card's Type chips (`type:cloudflare`, etc.), which
is the selector form the resolver actually honours for them.

**Verification.** Build green; svelte-check 80 errors / 56 warnings,
identical to the pre-fix tree of this change (and still zero added
against the 80/58 measured before the 950 work). Chip derivation is a
pure function of the pools payload already proven live in the 950 API
checks (pools seeded with group "cloudflare" produce the same chip that
was clicked through there).

Found during the post-950 proxy audit (2026-11-06), which also recorded
two further proxy findings left for an owner decision: the keyed
provider Proxy Lock's Type options are stored as a bare `proxyGroup`
that the resolver prefixes with `group:` (lock-to-type never resolves),
and a stale strategy `proxyPoolId` silently outranks the lock's
`proxyGroup` because the lock handler never clears it.
