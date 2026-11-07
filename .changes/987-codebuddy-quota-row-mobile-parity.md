# 987 — Quota rows stack on mobile like upstream; CodeBuddy/WorkBuddy price fields forwarded

## Symptom

The owner asked for the CodeBuddy/WorkBuddy UI to be checked against
AxonRouter JS so it looks as good there. The audit found the family
surfaces already at parity — the Total Credit card, per-pack rows with
the recurring/"expires in" wording, the hide-row action with persisted
visibility, the Provider Detail "Credits left: X / Y (resets …)" line
(verbatim with JS ConnectionRow), and the combo credit ordering — except
for the quota row layout itself at phone width, plus one dropped field
pair in the normalization.

## Root cause

Upstream's QuotaTable row stacks on mobile: the row is a column (name
row, with the reset time riding on it, then the progress block owning a
full row) and only becomes a horizontal row at `sm:` and up. Upstream's
own comment records why: at 360 px the fixed 144 px name column plus the
reset column left the progress column 0–27 px wide and cut the
used/total text to two glyphs. The Go port kept a single horizontal
flex row at every width — the fixed `w-36` name column and the reset
column always present — reproducing exactly that crush for every
provider's rows, CodeBuddy/WorkBuddy packs included. Separately,
upstream's parseQuotaData forwards `price`/`priceNote` for the
codebuddy-cn / codebuddy-intl / workbuddy branches and the shared row
renders a price tagline under the pack name when a price is present;
the Go normalization dropped both fields and the Go row had no tagline
slot. (No CodeBuddy billing response on either side currently sets a
price — upstream's only producer is the FreeBuff usage service — so this
half is plumbing parity: the fields now survive end to end and the
tagline renders the moment a quota carries one.)

## Fix

Frontend only, in the shared quota table (the same component upstream
renders these providers with):

- Row container is `relative flex flex-col gap-1.5 sm:flex-row
  sm:items-center sm:gap-2`; the name cell drops its fixed width below
  `sm:` (`sm:w-36 sm:shrink-0`) and gains a mobile-only reset span
  (`sm:hidden`) showing the same countdown/"expires in" label; the
  progress cell gains `sm:min-w-[7rem]`; the desktop reset cell is
  `hidden sm:block`.
- The hide-row button follows upstream's placement: absolute at the
  row's top-right with a 44 px touch target on mobile, static 32 px at
  `sm:` and up (the row is `relative` to anchor it).
- The name cell gains the price tagline line
  (`formatFreebucksPrice(price)` plus ` · priceNote`, title = priceNote),
  rendered only when the quota carries a price; `formatFreebucksPrice`
  is ported into quota/types.ts, `price`/`priceNote` join
  NormalizedQuota, and the codebuddy-cn / codebuddy-intl / workbuddy
  parse branch forwards both fields.
- Untouched by design: Go's credit-balance row treatment (an amount, no
  bar) and the gray "unknown" color state, both deliberate Go-side
  choices upstream lacks; the Total Credit card; color thresholds
  (>70 green, ≥30 yellow, else red — verified identical to upstream).

## Verification

- svelte-check totals identical to the pristine baseline (89 errors /
  56 warnings, zero new), and the web build succeeds.
- The layout classes are a direct port of upstream QuotaTable.js's row
  markup, diffed line by line during the audit; every other family
  surface named above was read in both trees before concluding parity.
- A pixel-level check of populated CodeBuddy rows remains open by
  environment: no CodeBuddy/WorkBuddy connection exists in any database
  reachable from here, and the rows render only from a live billing
  read, so the first staging view of a real account is the visual
  confirmation. The change is class-level markup in one shared
  component, with no logic altered.
