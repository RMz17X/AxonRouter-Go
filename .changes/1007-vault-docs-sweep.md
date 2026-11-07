# 1007 — Vault removed from the documentation

## Symptom

Fragment 1006 removed the credential-vault system from the code, but the
current-state documentation still described it as a live feature: a
"Credential vault" section in wiki/security.md and wiki/advanced-features.md,
vault routes in wiki/api.md, vault rows in the wiki dashboard/index/parity
tables, and a TECHNICAL_DEBT entry (DB-02) crediting the vault as the
resolution for plaintext credentials at rest.

## Root cause

The 1006 sweep covered code and UI only; the docs were flagged as a follow-up
and the owner ordered them cleaned as well.

## Fix

- wiki/security.md: vault section deleted; vault rotation dropped from the
  destructive-operations table; further-reading now points at
  GuardrailsPanel.svelte.
- wiki/advanced-features.md: the section is "Guardrails — prompt scanning,
  and who may spend what"; the vault subsection (status card, rotation
  instructions, master-key warning) is deleted; steps now open **Guardrails**.
- wiki/api.md: the "Security" route block is "Guardrails"; the two
  `/api/vault/*` routes are gone.
- wiki/dashboard.md, wiki/index.md, wiki/parity.md, wiki/governance.md:
  vault mentions removed; the dashboard table row is the Guardrails page.
- wiki/screenshots.md + docs/keirouter-port-screenshots.md: captions and the
  view description are Guardrails-only; the screenshot asset is renamed
  `security-vault-guardrails.webp` → `security-guardrails.webp`.
- TECHNICAL_DEBT.md DB-02: rewritten to the current truth — credentials are
  plaintext at rest as an accepted posture (owner decision, 2026-11-08); the
  vault that briefly sealed them was removed in v1.9.36.

Deliberately untouched: docs/keirouter-port-plan.md and
docs/POST_RELEASE_DELTA_ANALYSIS.md are frozen point-in-time records of the
port project and still mention the vault historically; CHANGELOG.md and
.changes/ history likewise stand.

## Verification

- `grep -rin vault wiki/ TECHNICAL_DEBT.md docs/keirouter-port-screenshots.md`
  returns only the two removal-context lines (the v1.9.36 notes in
  TECHNICAL_DEBT.md and the port-screenshots catalog).
- The renamed screenshot asset exists under its new name and both referencing
  pages point at it.
