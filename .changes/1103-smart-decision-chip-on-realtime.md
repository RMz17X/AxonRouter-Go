---
type: fix
---

The smart-router decision chip now also renders on the Realtime requests card (desktop table and mobile cards), not only in the Request Details tab — the Realtime card polls the same request-details list, whose rows already carry the recorded decision. The chip's label/icon/class/title helpers moved into a shared `smartDecision.ts` module used by both components, so the chip reads identically everywhere.
