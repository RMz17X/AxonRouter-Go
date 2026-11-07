# 995 — Proxy Pools: import targets a group; Groups tab manages members

## Symptom

Bulk-imported proxies always landed ungrouped (the import had no group
option), and the Proxy Groups tab was display-only: summaries with no way
to change membership, while its "New Pool in Group" button opened the
plain create dialog without the group filled in.

## Fix

Frontend only:

- The Bulk Add tab gains an "Import into Group" control — No group / an
  existing group / a new group name — and imported pools are created with
  that group.
- `openCreateModal` takes a group preset; the Groups tab's per-card
  "Add Pool" opens the create dialog with the group already filled.
- New `proxypools/GroupMembersModal.svelte`: a searchable checkbox list
  of every pool (checked = member, pools from other groups marked);
  saving writes only the membership diff (joins set the tag, leaves clear
  it) and reports ok/failed honestly. Each group card gains a
  "Manage Members" action opening it.
- The group card's "View in Table" drops a dead `Default` special case
  (group summaries never contain one).

## Verification

- svelte-check baseline held (89/56, zero new); web build succeeds.
