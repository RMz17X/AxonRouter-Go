# 1000 — Usage views: response-mode badge before the provider badge

## Symptom

Wherever the STREAM/JSON mode badge sits beside the provider badge —
the realtime table's Model cell, the realtime mobile cards, and the
Request Details provider cell — the provider came first, so the eye
landed on the provider before the response mode (owner report).

## Fix

Frontend only: swap the render order in those three spots so the
STREAM/JSON badge leads and the provider badge/identity follows. The
in-flight modal is unchanged — its mode badge already stands alone at
the card's top-right, with the provider as an alias prefix on the
model line, so there is no adjacent pair to swap.

## Verification

- svelte-check with `--tsconfig ./tsconfig.app.json` at the pristine
  baseline totals (89 errors / 56 warnings); web build succeeds.
