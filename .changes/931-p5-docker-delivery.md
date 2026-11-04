### P5 — Docker delivery: host commands removed, compose stacks, migrate profile

Executes plan §P5 (L7 + L10). Owner defaults: nginx is the cluster LB,
images are built locally (no registry publishing in this phase), Valkey
is the speed-layer flavour.

#### Host-command removal (cmd/9router-go)

- Deleted files: `daemon_cmd.go` (start/stop/restart/status/logs +
  `--background`/`-d` flag), `service_cmd.go` (service
  install/uninstall/start/stop/restart/status), `commands.go` (the
  `mitm` enable/disable/status handlers — they were the last importer
  of the MITM package).
- Deleted package: `internal/mitm/` (cert/dns/server/manager +
  handlers/; its two test files went with the package it tested). The
  dashboard MITM surface was already gone — no route references it.
- `main.go`: the command table is now `version`, `update`, `help` and
  the six feature flags; the default action boots the foreground
  server (menu on a TTY). Proof — before: commands version/update/
  mitm/start/stop/restart/status/logs/service + `--background`; after:
  version/update/help only, `9router-go start|service|mitm` answer
  `unknown command`, `--background` is rejected as an undefined flag.
- `menu.go`: the terminal picker shrank to the foreground world —
  1) start in this terminal, 2) tray companion, 0) exit. The
  daemon-URL helper moved here as `serverURL()`. `parseChoice`,
  `readChoice`, `trayBinaryName` are byte-unchanged, so the
  non-quarantined `menu_test.go` still compiles and its assertions
  still hold (vet green; no test file edited).
- Deliberately KEPT, with reasons: `internal/daemon` and
  `internal/service` packages — they have living consumers outside
  this binary (cmd/9router-tray + internal/tray manage the gateway
  through them; internal/updater strips the background flag via
  daemon constants). What this binary lost is the *operator surface*,
  per plan. The `RegisterPID` block in runServer also stays: it only
  fires for a process spawned through internal/daemon (the tray), and
  removing it would blind the tray to the gateway it started. The
  settings data keys `mitmAlias` / `mitmSudoEncrypted` are stored-data
  keys, not code surface; they belong to P6's text/data sweep, not
  this removal.

#### Images and stacks

- `Dockerfile` (replaced): the proven web (bun) and Go cross-build
  stages are unchanged; the runtime stage is now alpine + su-exec with
  a dedicated `app` user (uid 10001), a HEALTHCHECK on
  `/api/auth/status`, and `docker-entrypoint.sh`, which hands the
  DATA_DIR volume to `app` and drops privileges — the server process
  runs as uid 10001 (proven on agent-x via /proc: `Uid: 10001`).
  Final image size: **81.8 MB**.
- `docker-compose.single.yml` (replaces docker-compose.yml): app +
  postgres:17, named `pgdata` volume, PG healthcheck gating app
  start, `appdata` named volume for the DATA_DIR state root, DATABASE_URL
  assembled from the POSTGRES_* parts, stop_grace_period 30s paired
  with SHUTDOWN_GRACE_SECONDS (P3 drain).
- `docker-compose.cluster.yml`: app replicas (APP_REPLICAS, default
  2) + postgres:17 + valkey/valkey:8-alpine + nginx:1.27-alpine
  (`nginx.cluster.conf`, DNS-variable upstream so replica changes
  re-resolve; SSE-safe proxying). Replicas share PG (truth) and
  Valkey (VALKEY_URL wired; boot banners prove `mode=cluster …
  speedServing=redis`). DATA_DIR volume is shared across replicas.
- `.env.example` rewritten around the compose contract (POSTGRES_*,
  JWT_SECRET, PORT, INITIAL_PASSWORD, APP_REPLICAS, LEGACY_DIR) with
  the bare-binary variables (DATABASE_URL whole, DATA_DIR, VALKEY_URL)
  and the standing app-level entries kept.
- Migration wiring: a `migrate` profile in both stacks builds
  `tools/migrate-db/Dockerfile` (new; the migrator's Go code is
  untouched) and runs it against `${LEGACY_DIR:-./legacy}` + the
  compose PG — the exact profile the P6 README upgrade path will use.

#### One wiring fix found by the acceptance run

- The migrate profile first mounted the legacy dir `:ro` and the
  migrator failed with `unable to open database file (14)`
  (SQLITE_CANTOPEN): it opens the source `mode=ro`, but a WAL-mode
  database needs a writable directory for its -shm/-wal recovery
  sidecars. Proven by re-running with a writable mount: migration
  completed and the source file's md5 was byte-identical before and
  after (the database itself is never written). Both stacks now mount
  the legacy dir writable, with the reason recorded in a comment.

#### Acceptance on agent-x (Docker 29.8.1, compose v5.5.1; projects
p5check/p5checkc; host otherwise untouched, stacks + images + scratch
dir removed afterwards)

- (a) Single stack from a clean tree: `up -d --build` → postgres and
  app both `(healthy)`; GET / → 200; login with INITIAL_PASSWORD →
  `{success:true}`; API key created via /api/keys; /v1/models with the
  key → 200 with the exact `{object:"list", data}` envelope. The app
  container was then force-recreated (new container id): the same key
  still answered 200, and PG itself held the key row
  (`SELECT count(*) FROM apikeys` → 1).
- (b) Migrate profile against the real legacy fixture (the demo-era
  app SQLite, WAL mode): `--profile migrate run` → every table
  source = copied, 0 skipped (apiKeys 4, providerConnections 3,
  usageHistory 30, requestDetails 37, settings 1), the migrator's own
  closing line: `verification passed: every table satisfies target =
  source - skipped`. The app was then booted on the migrated PG and
  served the migrated data (/api/usage/stats reported the fixture's
  37 requests).
- (c) Cluster stack: both app replicas `(healthy)` with distinct node
  ids and `mode=cluster … speedServing=redis` banners; valkey and
  postgres healthy; through the nginx port: GET / 200, login OK, key
  created, /v1/models 200 four times running.

#### Environment notes (agent-x, for the P8 runbook)

- The docker client's buildx state file in ~/.docker is
  permission-blocked on agent-x; builds need
  `BUILDX_CONFIG=<scratch>` (used throughout, inside the scratch dir).
- agent-x is heavily loaded (load ~30, kasm tenants); the web stage
  took ~9 minutes wall on the cold build. Budget P8 timings for it.
- A first build attempt lost its SSH session and left an orphaned
  duplicate `compose up --build` client; it was killed by exact PID.
  Long remote builds should be launched detached with a log (done for
  every subsequent run).
- My first tree copy to agent-x silently dropped `cmd/9router-go/`
  (unanchored rsync exclude `9router-go`, the same trap .dockerignore
  documents). The image build caught it (`stat /app/cmd/9router-go:
  directory not found`); the copy was repaired and the file set
  verified before rebuilding.

#### Deferred to P8 (explicitly)

- The §6 drill itself: cross-node cooldown honouring, per-node
  dashboard aggregation, Valkey killed mid-traffic → PG degradation →
  Valkey resumed, one app node killed → LB serves on, and the load
  probe with its latency/error budget. The cluster stack delivered
  here is the drill's fixture, not its result.
