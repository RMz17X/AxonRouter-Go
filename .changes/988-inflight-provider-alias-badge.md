# 988 — In-flight cards draw the provider as upstream's alias prefix, not a grey pill

## Symptom

The owner screenshotted the Usage page's Active In-Flight Requests modal:
each card led with a bare model line and a small grey pill whose text was
a fragment-like word ("Trust", "Wz") — the custom node's name, presented
with no context — above an unlabelled "Account: …" line. He asked for the
streaming view's provider badge to be updated to the AxonRouter JS
standard.

## Root cause

The Go port invented its own card: model line, then a detached pill
rendering `providerDisplayName(provider)`. Upstream's ActiveRequestsModal
never had such a pill — it renders the provider as a coloured alias
prefix on the model line itself (`formatProviderModel`: "alias/model",
the alias in the primary colour and semibold), over a labelled card:
a "MODEL" micro-label above the model line, and an account footer
separated by a hairline carrying an icon, an "ACCOUNT:" micro-label and
the value. The pill was also the only place the provider appeared, so a
short node name read as a stray fragment instead of an identity.

## Fix

Frontend only (`AxonRealtime.svelte` + one helper in analytics/types.ts):

- New `providerAlias()`: the catalog alias when the registry carries one
  (the same lookup chain `providerDisplayName` uses), else the display
  name — so a custom compatible node still reads as itself, never as its
  raw synthetic id.
- The in-flight card now mirrors upstream's structure: "MODEL" label
  with icon; the model line as `alias/` in primary semibold followed by
  the model, with a redundant alias/provider prefix stripped from the
  model the way upstream strips it, and the full display name plus
  combo label in the title; below a hairline, the account footer with
  icon and "ACCOUNT:" label, the ×count and the (conditional) elapsed
  time right-aligned. The grid gains upstream's fourth column at `xl`.
- The grey provider pill is gone from the modal. The completed-requests
  table and mobile cards keep their full-name Provider badge — there the
  name has a labelled column and reads correctly.

Deliberately not drawn: upstream's STREAM/JSON mode chip. Go's tracker
aggregates in-flight requests into counted (model, provider, account)
rows with no stream flag and no per-request start time, so the chip
would be decoration over nothing — the standing honesty rule for this
card, unchanged. Elapsed time still renders when a row happens to carry
a start time, as before.

## Verification

- svelte-check (with `--tsconfig ./tsconfig.app.json`) totals identical
  to the pristine baseline (89 errors / 56 warnings, zero new) and the
  web build succeeds.
- The card markup is a direct port of upstream ActiveRequestsModal.js's
  card structure, diffed field by field during the audit.
- Pixel confirmation rides on the next staging view with live traffic:
  the modal renders only while requests are in flight, which no
  environment reachable from here can stage on demand. The owner's
  screenshot shows the staging gateway serving OpenCode traffic, so the
  first live open of the modal after deploy is the visual check.
