# Review cycle 3 — fix pass (A-1, A-2, A-3, B-1, B-2, C-1, C-2, C-3, E-1)

Fixes for the findings in `~/workspace/9router-review-cycle3.md`,
applied on main @ 9a147b6c. Cycle 3 found no BLOCKING and no MAJOR;
everything below is MINOR/NIT. E-2 (AnalyticsView.svelte file size) is
explicitly not addressed — the review itself calls splitting it churn.
Every fix was verified by direct checks only (build, vet, svelte-check,
a live migrator smoke) — no test suites were run, per the standing
rule.

## A-1 (MINOR) — an errored provider now expires from the topology map

The presence effect in `AnalyticsView.svelte` stamped the sticky
`errorProvider` with "now" on every run, and the effect re-runs on
every 5 s stats poll and SSE merge — so an errored provider's presence
never aged and the owner's five-minute rule never expired it.

- The effect now stamps `errorProvider` only when its value changes,
  tracked by `prevErrorProvider` — a plain (untracked) `let`, so the
  bookkeeping adds no reactive dependency and no loop. A different
  provider erroring stamps the new one; the previous one's stamp ages
  out on the normal rule, as does the error node itself.
- Accepted trade, forced by the wire shape: the stats/SSE payload
  carries the provider string and no error timestamp, so an unbroken
  error streak from one provider is indistinguishable from a single
  error — presence runs five minutes from the streak's first error
  (the review's prescribed fix direction; occurrence-time stamping is
  not available client-side).
- Reviewer re-read of the final diff: if `errorProvider` ever cleared
  to `''`, the change is detected, `stamp('')` no-ops, and the old
  stamp ages out — no stuck state either way.

## A-2 (MINOR) — loadStats results are sequenced

`loadStats` had no in-flight sequencing: a slow full read resolving
after a fresher totals poll overwrote the poll's totals on the
wholesale path and could resurrect just-finished active requests.

- Design chosen: a monotonic sequence counter (`loadStatsSeq`). Each
  call captures its sequence; only the latest call applies its result,
  its error, or the `isFetching` reset. The alternative the review
  offered — skipping poll ticks while a full read is in flight — was
  not taken: it needs a separate full-read flag and changes the poll's
  cadence, while the counter covers all three callers (section/period
  effect, 5 s poll, 30 s full read) uniformly, and also drops stale
  reads across a period change.
- A superseded full read's breakdown maps stay stale until the next
  30 s read — the bounded cost; the window was previously unbounded in
  the other direction (fresher data overwritten by older).

## A-3 (NIT) — SSE reconnect backs off

The usage stream's fixed `setTimeout(connectStream, 3000)` retry ran
at full rate against a dead gateway forever, and every open dashboard
reconnected in lockstep after an outage. The retry now doubles from
3 s to a 30 s cap with up to 1 s of jitter, and the delay resets when
a connect actually establishes (reader acquired). Cleanup semantics
(abort + clearTimeout on unmount/section change) are unchanged.

## B-1 (MINOR) — the breaker sentences match the mechanism again

`ARCHITECTURE.md` and `DATABASE.md` §6 still described the speed-layer
breaker as "3 consecutive failures trip it" — stale since cycle 1's
m3 replaced the consecutive counter with a sliding window. Both now
read "3 failures inside a 10 s sliding window trip it", matching
`internal/speedlayer/failover.go` (`failThreshold = 3`,
`failWindow = 10s`).

## B-2 (NIT) — usageWritesDropped is documented where operators look

DATABASE.md §6 (the degradation model) gains a paragraph on the same
`GET /api/system/memory` payload: `usageWritesDropped` is the count of
usage-ledger rows lost since boot across every loss path (queue-full
drops and rows the per-row fallback could not land), zero is the only
healthy value, nonzero means investigate starting with the `[usage]`
log lines.

## C-1 (MINOR) — invalid UTF-8 in a legacy text value is a skip, not an abort

`convertValue` (`tools/migrate-db/copy.go`) validated malformed JSON,
unparseable numbers and wrong types into per-row skips, but never
UTF-8 — and PostgreSQL enforces the database encoding during COPY, so
one legacy row with non-UTF-8 bytes passed conversion and aborted the
whole run at its 5,000-row chunk's flush.

- The jsonb path now checks `utf8.Valid(raw)` before `json.Valid`,
  and the text/character path checks `utf8.ValidString` /
  `utf8.Valid` for string and []byte values. Failures return
  `notUTF8Error` ("value for column %s is not valid UTF-8") and flow
  through the existing skip-and-report path.
- bytea is deliberately untouched: arbitrary bytes are a faithful
  representation there.

## C-2 (MINOR) — integer/smallint narrowing range-checks before it narrows

The `integer`/`smallint` case converted through `int32(n)`
unconditionally; Go's narrowing wraps modulo the target width, so an
out-of-range legacy value (a millisecond epoch in an integer column)
was silently mangled. The case now errors — and therefore skips —
outside `math.MinInt32/MaxInt32` (integer) or `math.MinInt16/MaxInt16`
(smallint), with the value and column named. The returned Go type is
unchanged (int32): only the check was missing.

## C-3 (NIT) — the WAL sidecar failure names its own fix

When the source open/ping fails with a source-recovery class, the
error now appends: "a WAL-mode source needs a writable directory for
its -shm/-wal recovery sidecars; mount the legacy directory writable".

- One deliberate widening of the review's letter, on live evidence:
  the review named the can't-open class (SQLITE_CANTOPEN, 14 — the
  shape P5 hit on a read-only Docker mount). Staging the same failure
  faithfully here (WAL database, non-writable directory, unprivileged
  user) produced SQLITE_READONLY_RECOVERY (1544) instead — the file
  opens, WAL recovery cannot write. The matcher
  (`isSourceRecoveryError`, via `errors.As` on the modernc
  `*sqlite.Error`; the driver import is now named instead of blank)
  accepts both codes, and both were exercised live (below). Any other
  open error keeps its previous bare form.

## E-1 (NIT) — the deleted MITM feature's settings plumbing is gone

`internal/handlers/dashboard/settings.go` round-tripped the
`mitmAlias` kv scope through the database export/import payload on
every backup/restore, for a feature deleted in P5. Removed: the
`databaseExport.MitmAlias` field and its initialization, the
`loadKVScopes` read case, the `writeKVPayload("mitmAlias", …)` call,
and `'mitmAlias'` in the import wipe list. Consequences, all intended:
backups taken here no longer carry a `mitmAlias` section; a legacy or
Axon backup's section is ignored on import; pre-existing `mitmAlias`
kv rows are no longer deleted by an import — they stay inert and
unread, which is the recorded posture for that stored data (P6).
No other code referenced the scope (repo-wide grep is now empty).

- **Decision: `mitmSudoEncrypted` STAYS in `protectedSettingKeys`.**
  That list is a guard, not feature plumbing: it strips the key from
  dashboard-client writes (Axon parity — Next's
  PROTECTED_SETTING_KEYS carries it too), it costs one string
  comparison on the settings-write path, and a legacy settings row
  can still carry the value — removing the entry would let a generic
  settings update touch a stored credential-shaped value for no
  benefit. The reasoning is recorded in the code comment as well.

## Verification (direct checks only)

- **Static gates:** `go build ./...` and `go vet ./...` green on the
  main module; `go build ./...` and `go vet ./...` green in
  `tools/migrate-db`; gofmt clean on every touched Go file. (The one
  gofmt drift in the tree, `internal/handlers/dashboard/
  apikeys_policy.go`, is pre-existing at HEAD and untouched.)
- **Web:** `bun run build` succeeds (pre-compressed assets 1669 KB →
  313 KB brotli). `bun run check:svelte`: **77 errors / 57 warnings,
  30 files with problems** — byte-for-byte the baseline totals, proven
  by an A/B run against pristine HEAD (stash, re-check, pop): the one
  error inside `AnalyticsView.svelte` (`'tokens' does not exist on
  type 'RecentRequestItem'`) is the same pre-existing error in both
  runs, relocated from line 334 to 374 by this pass's insertions. No
  new errors, none removed.
- **Migrator smoke:** a crafted legacy source (python3 stdlib
  sqlite3) — `kv` with one clean row and one row whose `value` is
  `CAST(x'6c6174696e2d31a9' AS TEXT)` (invalid UTF-8 in a TEXT value),
  `providerConnections` with one clean row and one with
  `priority = 1700000000000` — migrated into a scratch database on the
  local staging PostgreSQL 17. The run **completed (exit 0)**:
  - `providerConnections`: source 2, copied 1, skipped 1 — "value
    1700000000000 overflows integer column priority"
  - `kv`: source 2, copied 1, skipped 1 — "value for column value is
    not valid UTF-8"
  - "verification passed: every table satisfies target = source -
    skipped"
  - Direct read-back of the target: `kv` holds only
    ('settings', 'theme', 'dark'); `providerConnections` holds only
    ('c1', 5). Re-run with the final binary (post-C-3 rebuild) via
    `--truncate` reproduced identical results. Scratch database
    dropped, fixtures deleted.
- **C-3 live:** WAL-mode source in a non-writable directory, migrator
  run as `nobody` via setpriv: `open source …: attempt to write a
  readonly database (1544) — a WAL-mode source needs a writable
  directory for its -shm/-wal recovery sidecars; mount the legacy
  directory writable`. Missing-file open: `unable to open database
  file (14) — ` with the same hint appended.
- **Frontend fixes are code-verified** (no browser rig this pass, per
  the brief): the final AnalyticsView diff was re-read as a reviewer —
  `prevErrorProvider` and `loadStatsSeq` are plain untracked `let`s
  (no new reactive dependencies, no effect loops), the presence
  effect's tracked reads are unchanged, the SSE effect's cleanup
  contract is untouched, and the `isFetching` reset cannot be stranded
  (the latest call always clears it; an older call finishing last
  skips a clear that already happened).

## Scoped down, plainly

- C-3's matcher is wider than the review's literal "can't-open class"
  (adds 1544), on the live evidence above — the narrower version
  would not have fired in the faithful staging of the very failure it
  exists to explain.
- Nothing else was scoped down. E-2 (file size) untouched per the
  brief; cycles 1–2's verified-sound list untouched.
