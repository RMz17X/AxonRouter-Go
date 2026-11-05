# NoAuth proxy UX: proxy group or proxy type only (owner rule)

Owner rule (2026-11-06): for OpenCode free and every other NoAuth provider,
the proxy choice is a proxy GROUP or a proxy TYPE — nothing else. The NoAuth
proxy UI offered a third axis on top of the group chips: a rotation strategy
(None / Round-robin / Random) under which the group selection was disabled
and requests rotated across ALL active pools regardless of any group. That
mode is gone, and the missing type choice is added.

## What changed

- `NoAuthProxyCard` (the one card, see below): the selectable targets are
  now Direct (None), one chip per proxy group, and one chip per proxy type
  (labelled `Type: <type>`, with active-pool counts from the pools list).
  The Rotation Strategy control, its state, and its helper text are
  deleted. Saving from the card always deletes a stored `rotateStrategy`
  for the provider, so a strategy left behind by the old UI cannot keep
  rotating invisibly behind a UI that no longer shows it. A type selection
  is stored as `proxyPoolId: "type:<type>"` with no `proxyGroup` mirror —
  the resolver reads the `type:` prefix natively.
- `ProviderDetailView` carried a second, inline copy of the NoAuth proxy
  section (group chips + rotation buttons writing the same strategy
  fields). It is replaced by `<NoAuthProxyCard>` — one home for the NoAuth
  proxy choice, per the no-redundancy rule — and its now-dead state,
  save handler, settings-load extraction, group derivation, and the
  `proxyGroupsList` fetch that only fed the deleted copy are removed.
  `MediaProviderDetail` already used the component and inherits the fix.

The engine is untouched: `resolveProviderProxyPoolID` already resolves
`group:`/`type:` selectors for provider strategies, and with no
`rotateStrategy` stored the NoAuth rotation branch simply never engages.

## Verification

- Build green. svelte-check measured against the true pre-change tree
  (HEAD versions of the two touched files restored into a copy of this
  tree): 80 errors / 58 warnings before, 80 errors / 56 warnings after —
  zero added. (The 77/57 figure in AGENTS.md §6.E predates the
  post-release wave; the tree had already drifted to 80/58.)
- The built bundle contains the new card copy ("Routes through the
  selected proxy group or type") and the string "Rotation Strategy" no
  longer appears in any built asset.
- Live API proof on a throwaway instance (fresh DB, pools seeded in
  groups cloudflare/residential of types socks5/http), replaying the
  card's exact save logic: a stale `rotateStrategy: round-robin` was
  seeded first; after a group save the strategy read back
  `{proxyPoolId: "group:cloudflare", proxyGroup: "cloudflare"}` with the
  rotation gone; after a type save `{proxyPoolId: "type:socks5"}`; after
  Direct the provider's strategy entry was removed entirely.
- A remote visual pass was attempted and could not reach the VM-local
  rig (the browser runs on another host; loopback is not shared), so the
  rendering evidence is the bundle proof above rather than a screenshot.
