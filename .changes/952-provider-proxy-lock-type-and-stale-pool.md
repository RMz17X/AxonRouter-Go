# Provider Proxy Lock: type locks never resolved; stale proxyPoolId outranked the lock

**Symptom.** On a keyed provider's detail page, the Proxy Lock dropdown
offers proxy TYPES next to groups. Locking to a type (e.g. "Type: http")
showed as set in the UI, but traffic never used a pool of that type: with
no other binding it fell back to a direct connection. Separately, once a
strategy carried a `proxyPoolId`, changing or removing the lock in the UI
had no effect on traffic at all.

**Root cause.** Two storage-contract breaks between the lock UI and the
resolver:

1. `handleProxyGroupLockChange` stored the selection as a bare
   `proxyGroup` value for BOTH kinds — group names and type names alike.
   `resolveProviderProxyPoolID` converted any bare `proxyGroup` into the
   selector `group:<value>`, which `ActivePoolsByTarget` matches against
   the pools' group field. A type name is not a group name, so a type
   lock resolved to zero pools.
2. The resolver prefers `strategy.proxyPoolId` over `proxyGroup`, but
   the lock handler never wrote or cleared `proxyPoolId`. A value stored
   earlier (via the API or an older UI) silently kept control while the
   lock appeared to change or clear.

**Fix.**

- The lock's options now carry canonical selector values (`group:<name>`
  / `type:<name>`), `lockedProxyGroup` holds that selector form, and the
  handler owns both strategy fields on every change: a group writes
  `proxyPoolId: "group:<name>"` plus the `proxyGroup` mirror; a type
  writes `proxyPoolId: "type:<name>"` and deletes the mirror; None
  deletes both. The load path derives the displayed lock from the
  prefixed `proxyPoolId` first, so a type binding stored through the API
  also shows instead of reading as "None".
- The resolver heals legacy data: a bare `proxyGroup` naming a known
  proxy type now converts to `type:<value>` instead of `group:<value>`,
  mirroring `ActivePoolsByTarget`'s own bare-target precedence, so locks
  stored by the old UI take effect without a settings migration.
- The NoAuth card (950) already wrote the canonical forms; this brings
  the keyed lock to the same contract.

**Verification.** Web build green; svelte-check 80 errors / 56 warnings,
unchanged from the pre-fix tree; `go build ./...` and `go vet` on the
touched packages green. Live on a throwaway instance (provider `openai`,
dummy key; pools: cf-1 group=cloudflare type=socks5, resi-1
group=residential type=http, plain-http with NO group field, type=http —
all pointing at dead local ports so the chosen egress is observable),
one chat attempt per strategy state, read back from the server log's
egress field:

- `{proxyPoolId: "type:http"}` → egress `resi-1` (an http pool), failing
  closed with "assigned proxy failed, refusing to route direct".
- `{proxyGroup: "http"}` (the legacy bare form the old UI wrote) →
  egress `plain-http` — reachable ONLY by type, proving the heal.
- `{proxyPoolId: "group:cloudflare", proxyGroup: "cloudflare"}` →
  egress `cf-1`.
- No strategy → egress `direct`.
