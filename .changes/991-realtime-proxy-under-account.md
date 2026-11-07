# 991 — Realtime table: the Proxy column folds under Upstream Account

## Symptom

The owner asked for the Proxy column to be merged into the Upstream
Account column, with the proxy shown underneath the account — the same
treatment fragment 990 gave Provider under Model.

## Fix

Frontend only (`AxonRealtime.svelte`):

- The Proxy column is gone. The Upstream Account cell now stacks: the
  account name, and underneath it the same proxy chip the column used
  to carry (the pool chip in primary when the request left through a
  pool, the muted "direct" chip otherwise).
- The table's minimum width drops 760px → 700px with one column fewer.
- Mobile cards, which never showed the proxy, gain the chip in the
  meta row right after the account, so the information exists at phone
  width too.

No data changes: the rows already carried the egress label
(`detailListItem.proxy`, rendered by the existing `proxyLabel`).

## Verification

- svelte-check (with `--tsconfig ./tsconfig.app.json`) totals identical
  to the pristine baseline (89 errors / 56 warnings, zero new) and the
  web build succeeds.
- Pixel proof rides on the next staging view, like 988–990.
