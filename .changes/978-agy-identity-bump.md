# 978 — Antigravity identity mirror lifted to agy 1.5.2

## What changed

- `internal/providers/client_identity.go`: `AntigravityCLIVersion`
  "1.5.1" → **"1.5.2"**. `AntigravityCLIChangelist` is UNCHANGED at
  1012564305 (evidence below). Every derived surface follows through the
  two constants' only consumers: the UA builder and the runtime identity
  (`internal/providers/antigravity_identity.go`,
  `internal/providers/antigravity_pin.go`) — User-Agent, loadCodeAssist
  `ideVersion`, fresh pin snapshots.
- `internal/providers/antigravity_identity.go`: header comment updated to
  name the mirrored build (v1.5.2); no code change.

## Evidence for the captured build (no guessing)

- Live binary: `/home/hatch/.local/bin/agy`, 244,044,176 bytes, sha256
  `da1751e7658469fa80bf36eda2c60ee61216d8e42b735a184382ba31d1f6b88e`,
  mtime 2026-11-06 08:23 — bit-identical to the build the teardown pass
  recorded as **v1.5.2** when the live binary self-updated 1.5.1 → 1.5.2
  mid-analysis (`~/workspace/agy-analysis/FINDINGS-WIP.md`, "Self-update
  incident"). The file has not been replaced since, so 1.5.2 is the build
  this VM actually runs today. The binary was NOT executed for this
  fragment (running it is what triggered the self-update last time).
- Changelist: the embedded Go build version string was read out of THIS
  binary (byte scan, the same source the 1.5.1 value came from):
  `go1.28-20261102-RC00 cl/1012564305 +298f53e483` — the only `cl/` build
  string in the file. The 1.5.2 build was compiled with the same
  toolchain snapshot as 1.5.1, so the UA's `cl=` part does not move.
  (`go version -m` cannot read this binary — Google's internal fork
  build — which is why the value is taken from the raw string, exactly
  as in the original teardown's go-evidence.json.)
- UA grammar unchanged in this build: the format pieces `%s/%s/%s (%s)`,
  `aidev_client`, `os_type=`, `auth_method=` are each present in the
  1.5.2 binary; `antigravity/cli/` as a literal is absent, as expected —
  the real CLI assembles it at runtime.

## Pins untouched (permanence by design)

Resolution reads the pin first — `AntigravityIdentityForPSD` returns the
stored snapshot when one exists and only falls back to the runtime
derivation otherwise (verified in code before the bump). Existing
`antigravityIdentity` snapshots keep saying 1.5.1 verbatim for the
account's whole life; only new pins and connection-less surfaces present
1.5.2. The doc example in `antigravity_pin.go` still shows a 1.5.1
snapshot — that is what stored pins genuinely look like, so it was left
as-is deliberately.

## Verification

- `go build ./...` clean; `go vet ./internal/providers/` clean.
- In-process probe (temporary `cmd/probe978`, deleted after the run):
  - runtime UA: `antigravity/cli/1.5.2 (aidev_client; os_type=linux; arch=amd64; cl=1012564305; auth_method=consumer)`, ideVersion 1.5.2, platform 3
  - fresh pin snapshot JSON carries the 1.5.2 UA + ideVersion
  - a stored 1.5.1 pin resolves verbatim (UA and ideVersion both 1.5.1)
