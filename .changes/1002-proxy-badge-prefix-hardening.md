# 1002 — Proxy badge: repeated-prefix hardening

## Symptom

With the provider strategy stored as `proxyGroup = "group:ISP-BD"`
(the selector form), every Antigravity account badge painted red
("inactive") while traffic was verifiably flowing through the group:
the provider page derived its lock selector by unconditional
prefixing, producing `group:group:ISP-BD` — a group name no pool
carries — and the per-account badge's provider fallback stripped
only one prefix, counting pools in a group literally named
"group:ISP-BD".

## Fix

- The provider lock derivation (ProviderDetailView) and the NoAuth
  proxy card now build the selector with the shared
  `normalizeProxyTarget` (`web/src/lib/proxyBindings.ts`, mirroring
  the backend's `db.NormalizeProxyTarget`): an already-prefixed value
  passes through, a bare known type becomes a type selector, any
  other bare value becomes a group selector — per comma-separated
  part, so multi-group values derive correctly too.
- The badge's provider fallback strips repeated `group:`/`type:`
  prefixes (`/^((group|type):)+/`), so even a double-prefixed value
  stored by an older build or by hand still resolves to the real
  group name and counts its pools.
- The badge's selector parser (shared `parseProxyTargetParts`) takes
  a part's kind from its first prefix and strips any further ones,
  so a hardened value can never re-enter the count under a
  prefix-carrying name.
- The per-connection dropdown's "Auto:" line now renders the derived
  lock label ("Auto: Group X", "Auto: Groups A + B") instead of
  stripping one prefix inline.

## Verification

- svelte-check at the pristine baseline totals (89 errors /
  56 warnings) with no diagnostic referencing the new helpers;
  `bun run build` green.
