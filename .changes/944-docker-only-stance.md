# 944 — Docker-only stance: tray removed, self-updater gated off in containers, host installers retired

Owner decision 2026-11-05: all official information and usage guidance
for AxonRouter-Go is Docker-only, because the product is being prepared
for cluster deployment. Running outside Docker is unsupported DIY: the
operator provides their own PostgreSQL 17 and Valkey. Executed on
branch `feat/docker-only-stance` in an isolated worktree; not yet
committed at the time of writing (parent verifies and commits).

## The stance

- The compose stacks (`docker-compose.single.yml` for one node,
  `docker-compose.cluster.yml` for a cluster) are the only supported
  way to run the gateway. README's Quick Start already said this; it
  now also carries a short "Running outside Docker (unsupported)" note,
  and `wiki/installation.md` was rewritten to match (Docker first, the
  DIY note last, no host-install instructions).
- **No hard runtime refusal**: the gateway binary still starts anywhere
  it is run. This is deliberate — the stance governs what is supported
  and documented, not what the binary permits.

## Container marker: `AXONROUTER_IN_CONTAINER`

- New file `internal/updater/container.go`: `ContainerMarkerEnv`
  (`AXONROUTER_IN_CONTAINER`), `InContainer()` (accepts `1`/`true`),
  and the sentinel `ErrSelfUpdateDisabledInContainer`. The name follows
  the repo's existing namespaced precedent (`AXONROUTER_PEER_TOKEN`).
- Wiring: the Dockerfile sets `AXONROUTER_IN_CONTAINER=1` via `ENV`,
  and the app service in both compose files sets it in `environment`
  (with a comment, so the contract is visible in the stack files).
  `.env.example` documents it as set-by-image, never by hand.
- When set:
  - `updater.StartBackgroundCheck` returns before starting the cycle
    and logs one boot line: self-update is disabled in a container
    deployment and updates are delivered as new container images.
  - `updater.CheckUpdate` and `updater.PerformSelfUpdate` refuse with
    `ErrSelfUpdateDisabledInContainer` — so the dashboard version
    handlers refuse with the same explanation, and no caller can reach
    the binary swap.
  - The CLI `update` subcommand prints the explanation and exits
    non-zero without checking or touching the binary.
- When unset (DIY bare runs), updater behaviour is byte-identical to
  before: the updater, its channel configuration (`UPDATE_URL` /
  `UPDATE_REPO`, repointed to RMz17X/AxonRouter-Go in 942), and the
  background cycle all stay as they were.

## Tray removal inventory

The desktop tray companion is a host artefact; a Docker-only product
has no desktop to park an icon on. Removed:

- `cmd/axonrouter-tray/` (the companion binary, incl. its systray GUI
  and `tray.ico`) and `internal/tray/` (its library — the tray binary
  was its only consumer).
- The main binary's menu path to it: `startTray`/`trayBinaryName`,
  menu item "2) Run in background with tray icon", and the `"2"`/`"6"`
  choice routing in `cmd/axonrouter-go/menu.go` (plus its now-dead
  imports and the `TestTrayBinaryName` test). The TTY menu now offers
  foreground start and Exit only. Tray attributions in `main.go` and
  `ARCHITECTURE.md` comments were reworded to the background-spawn path
  that remains.
- Build/CI: the Makefile `cross-tray-one` target and its
  `cross-windows-amd64` invocation; the CI "Build tray binary" step and
  the `axonrouter-go-tray-windows-amd64.exe` entries in the CI artifact
  checks and SHA256SUMS assertion; the tray exe in `release.yml`'s
  upload list.
- `docs/specs/tray-companion-architecture.md` (the removed companion's
  design spec) and the tray section of `wiki/cli.md`.
- `go mod tidy` dropped the systray dependency tree
  (`github.com/getlantern/systray` + 6 indirect getlantern modules):
  go.mod −9 lines, go.sum −24 lines.

Remaining "tray" strings in the tree are accounted for: historical
material (`.changes/` fragments, `CHANGELOG.md`) untouched by design;
planning docs about other products or parity history
(`docs/keirouter-port-plan.md`, `docs/PARITY-REMAINING.md`,
`docs/parity/rollout-runbook.md`, `docs/superpowers/…`,
`COMPARISON.md`'s upstream-npm-tray comparison) left as history;
substring hits in unrelated words/code (`portray`, test fixtures).

## Installer removal (stance consequence)

- `install.sh` and `install.ps1` deleted, with their references in
  `wiki/installation.md`. They were the official one-line host-install
  path; a Docker-only stance retires it. (Remaining `install.sh`
  strings in the tree install *other* tools — jcode, Devin CLI,
  Hermes — and are unrelated.)

## Docs stance

- `README.md`: "Running outside Docker (unsupported)" note added after
  Quick Start (operator provides PostgreSQL 17 via `DATABASE_URL`,
  Valkey via `VALKEY_URL` for cluster mode; no installer, no supported
  host path); the Key Features line now scopes self-update to
  bare-binary installs.
- `wiki/installation.md`: rewritten — single-node and cluster compose
  up front, defaults table for the container shape, an Updates section
  (new image + recreate; the in-container refusal), first login, and
  the unsupported-DIY note last. The one-line installers, bare-run
  instructions, and host-persistence guidance are gone.
- `wiki/operations.md`: the Self-update section now opens with the
  container rule and scopes its commands to bare-binary DIY runs.
- `AGENTS.md` was not changed: it states the Docker delivery already
  and its engineering rules are out of this pass's scope.

## What deliberately did NOT change

- No runtime refusal: the gateway boots and serves identically inside
  and outside a container; only the self-updater is gated.
- The updater itself, its channel config, and bare-run update flow.
- Compose service topology (marker added to the app service's
  environment only), the API surface, and the web UI (no UI string
  instructs a host install of the gateway; the install strings in the
  CLI-tool cards concern third-party tools).
