# 1010 — Persona is visible in the sidebar by default

## Symptom

The Persona tab (id `jailbreak`) did not appear in the sidebar for a
deployment on the Default sidebar preset: `DEFAULT_HIDDEN_NAV_ENTRIES`
listed it next to Plans, so anyone who had never saved a hide list — or who
restored the Default preset — lost the system-prompt page from the menu and
could only reach it by direct URL. The owner hit exactly this while looking
for the page.

## Root cause

The default hide list treated Persona as an advanced-only surface, although
it is the gateway's system-prompt home and part of the owner's daily flow.

## Fix

- `web/src/lib/nav.ts`: `jailbreak` removed from
  `DEFAULT_HIDDEN_NAV_ENTRIES` (Plans remains the only default-hidden
  entry), and the Default preset hint now reads "Plans hidden, everything
  else in place". The Advanced preset ("every entry") is unchanged.
- Deployments with a saved hide list keep it verbatim — this changes only
  the fallback for an absent/unreadable list.
- Mechanical test updates in `web/src/lib/nav.test.ts` (listed, not run):
  the two default-list expectations become `['plans']`; the
  starts-from-defaults case becomes `[]` for unhiding Plans and
  `['quota', 'plans']` for hiding Quota.

## Verification

- Web build via the heavy gate: green.
- svelte-check at the standing baseline (89 errors / 56 warnings).
- The owner's staging instance already stores an explicit hide list without
  `jailbreak` (set 2026-11-08), so its sidebar is unchanged by this release;
  the new default applies to fresh or reset deployments.
