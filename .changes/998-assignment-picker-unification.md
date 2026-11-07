# 998 — Proxy assignment pickers: one derivation, one vocabulary

## Symptom

Every surface that assigns a proxy to a connection had grown its own
copy of the group/type derivation and its own words for the same
states: section titles came in three variants ("Proxy Groups",
"Proxy Groups (Smart Fitness)", "Proxy Groups (Smart Fitness &
Balancing)"), and in AddConnectionModal the "None" option's `__none__`
sentinel was converted to null on save — silently turning an
explicit-direct choice into inherit-the-provider-default, the opposite
of what the same sentinel means in ProviderDetailView.

## Fix

Frontend only:

- New `web/src/lib/proxyBindings.ts`: `deriveProxyGroups` /
  `deriveProxyTypes` (distinct trimmed values with total + active
  counts, zero-active entries kept). AddConnectionModal,
  ProviderDetailView (lock select, per-connection dropdown, Apply
  Proxy modal) and NoAuthProxyCard all consume it; the three local
  copies are deleted.
- Section titles are "Proxy Groups" / "Proxy Types" /
  "Individual Pools" everywhere.
- AddConnectionModal offers both zero states distinctly: "Provider
  default" (stores null) and "Direct (no proxy)" (stores `__none__`
  unchanged), round-tripping the same states ProviderDetailView's
  dropdown reads back. Inactive pools there are now marked
  "(inactive)" like everywhere else.
- NoAuthProxyCard's direct chip is relabelled "Direct (no proxy)".

Deliberately unchanged: the per-connection dropdown's lock-aware
labels (they encode lock state, not vocabulary drift), the Proxy Lock
"None (direct)" and Apply "None (unbind all)" labels (they describe
removing a lock/assignment), and NoAuthProxyCard's raw settings fetch
(its `cache: 'no-store'` merge and any-response-is-success semantics
are not behaviour-identical to the api client, so a swap would be a
behaviour change wearing a refactor's clothes).

## Verification

- svelte-check with `--tsconfig ./tsconfig.app.json`: 89 errors /
  56 warnings before and after (pristine worktree vs working tree);
  ProviderDetailView's 9 pre-existing errors are byte-identical as a
  message set, all in untouched regions.
