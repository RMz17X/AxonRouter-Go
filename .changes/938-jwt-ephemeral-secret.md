# JWT ephemeral secret — memoize per process so sessions validate when JWT_SECRET is unset

Micro-fix on main @ f04e1ad1, found by the council round-1 fix pass
(.changes/937) as an out-of-scope observation and confirmed by a
parent code read. No test suites run; verification is direct checks
only: build, vet, gofmt, and live probes on a scratch database.

## The defect

`loadJWTSecret` (`internal/config/config.go`) generated a FRESH random
ephemeral secret on every call when `JWT_SECRET` was unset — only the
warning was once-guarded (`jwtEphemeralWarnOnce`), not the secret.
`LoadConfig()` runs on request paths, so the secret that signed a
login differed from the secret that verified the next request:
`/api/auth/session` answered
`{"authenticated":false,"reason":"invalid_signature"}` and dashboard
sessions could NEVER validate in the unset shape. The documented
intent (P4, the warning text itself, README.md:184,
ARCHITECTURE.md:139) is an ephemeral PER-BOOT secret; the cycle-1
review's "verified clean" note for JWT repeated the per-boot claim —
implementation and claim disagreed in the unset branch.

## The fix (one file: internal/config/config.go, +31/−6)

- New package-level memo beside `jwtEphemeralWarnOnce`:
  `jwtEphemeralSecret string` guarded by `jwtEphemeralMu sync.Mutex`.
- `loadJWTSecret`'s unset branch now returns `ephemeralJWTSecret()`:
  first call generates 32 crypto/rand bytes (hex), stores the value,
  and every later call returns the same value — one secret per
  process, i.e. per boot, as documented.
- Failure honesty preserved: if `rand.Read` fails, the Error log
  fires, `""` is returned (callers fail closed), and the failure is
  NOT memoized — a later call retries generation. No empty/static
  fallback is ever cached.
- The warning still fires exactly once per process.
- The `JWT_SECRET`-set path is untouched — env value returned
  verbatim, byte-identical behaviour. `LoadConfig` itself was not
  restructured; no other config behaviour changed.

## Docs

Grepped README.md / ARCHITECTURE.md / DATABASE.md for JWT/ephemeral
phrasing before touching anything. Both statements found
(README.md:184, ARCHITECTURE.md:139) say the unset shape gets an
ephemeral per-boot secret whose sessions survive neither a restart
nor a second node — which this fix makes TRUE. Neither claims
sessions survive in the unset shape, so no doc edit was needed.

## Verification (direct checks)

- `go build ./...` and `go vet ./...` green via `~/bin/heavy`
  (default tags); `gofmt` clean on the touched file.
- Probe, scratch DB `router9_jwtfix` on the local staging PG
  (dropped afterwards; gateway killed by exact PID both boots):
  - **JWT_SECRET unset** (explicitly removed from the boot env):
    boot log shows the ephemeral warning exactly once, including
    after several requests. `POST /api/auth/login` →
    `{"mustChangePassword":false,"success":true}`.
    `GET /api/auth/session` with the login cookie, twice (two
    separate verify-path LoadConfig calls) → both
    `{"authenticated":true, ... "reason":"valid_paseto", payload
    with exp/iat}`. Pre-fix shape would have been
    `authenticated:false / invalid_signature` on the first verify.
  - **JWT_SECRET set**: no ephemeral warning in the boot log;
    login → `{"mustChangePassword":false,"success":true}`;
    session → `{"authenticated":true, ... "reason":"valid_paseto"}`.
    The set-shape flow works exactly as before.

Nothing unexpected. Diff is confined to
`internal/config/config.go` plus this fragment; not committed (the
parent verifies and commits).
