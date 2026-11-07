# 1001 — Proxy bindings: multi-group / multi-selector targets

## Symptom

A proxy binding named exactly one pool, one type, or one group. An
operator who wanted an account or a whole provider to rotate across
two groups (e.g. a primary ISP group plus a fallback group) had to
merge the pools under one tag, losing the grouping, or bind different
accounts to different groups and give up the shared rotation.

## Fix

The binding target now accepts a comma-separated list of selectors,
resolved as their union everywhere a target is consumed:

- `Repo.ActivePoolsByTarget` splits the target
  (`db.SplitProxyTarget`: parts trimmed, blank and `__none__` parts
  dropped) and unions each part's matches, deduplicated by pool ID in
  first-seen order. A single-part target goes through the exact
  pre-change matching code, so existing bindings behave identically.
- `db.NormalizeProxyTarget` is the one stored-value derivation —
  per part: an explicit `group:`/`type:` prefix passes through, a
  bare known type becomes a type selector, any other bare value
  becomes a group selector. The chat resolver
  (`resolveProviderProxyPoolID`) and the dashboard proxy-resolution
  readout both call it, so a stored multi value (`A,B`) reaches the
  matcher as `group:A,group:B` at both surfaces instead of the
  mangled single `group:A,B`.
- The per-connection client/relay resolution treats any target
  containing a comma as a selector target (a pool ID can never
  contain one), so a multi binding stored on a connection reaches
  `ActivePoolsByTarget` intact instead of failing as a missing pool.
- The resolution readout reports the union's candidates and labels
  the binding `group` when every part is a group, `type` when every
  part is a type, `pool` for a lone pool ID, and `multi` when mixed.
- Per-account badge (`proxyBadge.ts`): a multi-selector binding
  counts active pools across the union and reads e.g.
  "Groups: A + B (35)"; a mixed group+type target reads "Multi:".
- Add-connection modal: a "Select multiple groups" mode replaces the
  proxy select with a checkbox list of groups and stores the combined
  selector in the same `proxyPoolId` field; one checked group stores
  exactly today's `group:<name>` value. Types stay single there.
- Provider lock: the header control is now a popover — groups are
  checkboxes (multi-select, applied per toggle), types remain
  single-select and replace the group selection. The writer keeps the
  canonical pair: `proxyPoolId` carries the full selector and
  `proxyGroup` mirrors the bare name(s), comma-joined when the lock
  names several groups.

Stored-value contract: a multi-group binding is
`proxyPoolId = "group:A,group:B"` with
`proxyGroup = "A,B"` (provider lock) or the full selector alone
(per-connection `proxyPoolId`).

## Verification

- Temporary in-package probe (created, run, deleted) against the
  local staging PostgreSQL: single group/type/pool-ID/inactive and
  bare targets returned exactly the pre-change sets; unions,
  first-seen ordering, group+type overlap dedupe, self-dedupe,
  `__none__`-part handling, and the split/normalize derivations all
  matched expectations, including a normalize-then-resolve pass for a
  bare multi proxyGroup.
- `go build ./...` green; `bun run build` green; svelte-check at the
  pristine baseline totals (89 errors / 56 warnings).
