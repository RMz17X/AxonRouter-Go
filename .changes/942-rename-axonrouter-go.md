# 942 — Full rename: 9router-go → AxonRouter-Go

Owner decision 2026-11-05: the product is renamed in full, including the Go
module path. Executed on branch `rename/axonrouter-go` in an isolated
worktree; not yet committed at the time of writing (parent verifies and
commits). 941 changed paths (932 modified, cmd moves as renames).

## Mapping

| Surface | Before | After |
| --- | --- | --- |
| Go module | `9router/proxy` | `axonrouter/proxy` (all imports, all build tags incl. `integration` and the `sqlite_legacy` quarantine; `tools/migrate-db` header comment — it is a standalone module with no main-module require) |
| Main command | `cmd/9router-go` | `cmd/axonrouter-go` (`git mv`) |
| Tray command | `cmd/9router-tray` | `cmd/axonrouter-tray` (`git mv`; tray display name "AxonRouter Go", CLI name `axonrouter-tray`) |
| Binary / service identity | `9router-go` (systemd unit, launchd label `com.9router.gateway`, Windows task, cron marker, desktop file, OpenRC/runit units, pidfile, `/usr/local/bin/9router-go`) | `axonrouter-go` equivalents (launchd label `com.axonrouter-go.gateway`). Existing installs keep their old units until reinstall — only the data dir has a compat path (below) |
| Dockerfile | builds `./cmd/9router-go`, ships `/usr/local/bin/9router-go`, `CMD ["9router-go"]`, ldflags module path | `axonrouter-go` throughout |
| Compose (single + cluster) | image `9router-go:local`, migrate image `9router-go-migrate:local`, PG defaults `9router` | `axonrouter-go:local`, `axonrouter-go-migrate:local`, PG defaults `axonrouter` (fresh stacks only; `POSTGRES_USER`/`POSTGRES_DB` stay env-overridable). Service keys unchanged — they were already generic (`app`, `postgres`, `valkey`, `nginx`, `migrate`) and `nginx.cluster.conf` upstreams `app:20130`, so no service rename was needed |
| Makefile / build.sh / CI | `BINARY_NAME`, `CROSS_BIN`, ldflags path, cross-build asset checks, docker tag `9router-go:ci` | `axonrouter-go` throughout; release asset names in `release.yml`/`ci.yml` are now `axonrouter-go-*` (the owner's future releases carry the new name) |
| `git remote origin` | `https://github.com/RMz17X/9router-go` | `https://github.com/RMz17X/AxonRouter-Go.git` (set once; no fetch, no push) |
| Installers | `install.sh` / `install.ps1` pull `luqman-v1/9router-go` releases, asset `9router-go-*` | repo default `RMz17X/AxonRouter-Go`, asset `axonrouter-go-*`, install dir/binary `axonrouter-go` |
| Web | `<title>9router-go - …`, PWA manifest names, sidebar/login brand, boot text, backup/console-log/api-keys/providers download filenames, `<your-9router-api-key>` placeholder, `sk-9router-local-token` example key | `AxonRouter-Go` branding; download names `axonrouter-go-*`; placeholder `<your-axonrouter-go-api-key>`; example key `sk-axonrouter-go-local-token` (secrets-scan whitelist prefix updated in lockstep). `package.json` name was already just `web` |
| Docs | README, DATABASE, ARCHITECTURE, AGENTS (product-name mentions only), CLAUDE (two mentions — twin of AGENTS, same treatment), wiki/, `.env.example`, issue/PR templates | product renamed; self-links and badges repointed to `RMz17X/AxonRouter-Go`; PG examples use `axonrouter`; DATA_DIR examples describe the new default + legacy fallback. AGENTS.md `~/.9router.env` test-recipe file renamed to `~/.axonrouter-go.env` (doc convention only — no code reads that filename) |

## Behaviour decision — update channel repointed (not cosmetic)

The updater defaults previously pointed at upstream `luqman-v1/9router-go`;
in the renamed product that channel could cross-install upstream's binary
over AxonRouter-Go (the GitHub-releases fallback supplies digests). Per the
owner's steering:

- `DefaultGitHubRepo` → `RMz17X/AxonRouter-Go`
- `DefaultUpdateURL` → `https://raw.githubusercontent.com/RMz17X/AxonRouter-Go/main/version.json`
- User-Agent → `AxonRouter-Go/<version>`; temp prefix `.axonrouter-go-update-*`
- `scoreArchiveEntry` scores the NEW asset names (`axonrouter-go`,
  `axonrouter_go`); the rest of the scoring is untouched
- `version.json` `downloadUrl`, the dashboard changelog fetches
  (`web/src/api/client.ts`, `internal/handlers/chat/chat.go`), and the
  ChangelogModal/TopBar repo links follow the same repoint
- Until the owner publishes releases there, the check fails harmlessly
  (verified live: `axonrouter-go version` reports manifest 404 + releases
  404 and exits normally). `UPDATE_URL`/`UPDATE_REPO` env overrides and the
  unconfigured-channel refusal logic are unchanged

## DATA_DIR compat (the one data-bearing surface)

`ResolveDataDir` order is unchanged (DATA_DIR env → `.env` value → platform
default), but the platform default is now `~/.axonrouter-go`
(`%APPDATA%/axonrouter-go` on Windows) with a legacy fallback: when the new
dir does not exist and the legacy dir (`~/.9router` / `%APPDATA%/9router`)
does, the legacy dir wins; once the new dir exists it always wins. Proven
with a `go run` harness against `internal/config` under four HOME shapes:
legacy-only → `~/.9router`; empty → `~/.axonrouter-go`; both →
`~/.axonrouter-go`; DATA_DIR set → the env value. No other hardcoding of the
home data path exists outside `internal/config` (remaining `.9router`
mentions are the legacy fallback itself, the legacy SQLite artefact
`~/.9router/db/data.sqlite` named as a migration source, and ignore-file
entries kept for both dirs).

## Kept byte-identical (class b — third-party / wire / state identifiers)

Renaming any of these would break existing installs, client configs, or
wire peers; each stays with its pre-rename value:

- Response headers `x-9router-provider`, `x-9router-combo`, `x-9router-model`,
  `x-9router-attempts`, `x-9router-proxy-pool` — Axon-parity wire contract
  read by clients.
- `urn:9router:sp` — SAML SP entity URN default; changing it breaks existing
  IdP app registrations.
- Client-config content written into third-party tools' files: provider keys
  `"9router"` (Codex `[model_providers.9router]`, OpenCode, Crush/Pi/OMP
  provider tables), the `9Router` display name / `custom:9Router` Droid
  marker inside those configs, the `9router/` OpenCode model-id prefix,
  Nous attribution params `product=9router` / `client=9router` /
  `user=9router`, and the web UI snippets that write the same keys. Detector
  identifiers (`ToolHas9Router`, `has9Router`, …) name these kept markers.
- Provider-facing identities: Cline identity (`User-Agent: 9Router/<v>`,
  `X-CLIENT-TYPE: 9router`), `X-Msh-Platform: 9router`, outbound
  `User-Agent: 9Router` fetches (connection probe, currency, settings),
  `usageUserAgent = "9router-go"`, jailbreak-import fetch UA `9router-go`,
  CommandCode project-slug fallback `"9router-go"`, MiniMax session prefix
  `"9router-go-session"`, Netlify relay UA `"9Router"`.
- Share format: `9router://` link scheme and the `.9router.json` / `.9router`
  file format — links and files already shared must keep parsing.
- Browser storage keys (`9router_auth`, `9router_key`, `9router-theme`,
  `9router-nav-*`, `9router_mask_email`, `9router-oauth`,
  `9router.oauth.*`, `9router-locale`, `9router_pending_*`) and the sitegen
  wiki template's read of `9router-theme` — renaming logs users out and
  drops stored UI state.
- Id-derivation seeds `9router-zcode-billing` and `9router-zcode`
  (`ZCODE_SESSION_SEED` default) — changing them changes deterministically
  derived ids.
- SkillsView skill ids (`9router`, `9router-chat`, …) — folder names in the
  upstream `decolua/9router` skills repo, fetched by raw URL.
- Upstream attribution: all `decolua/9router` references, "Port of 9router"
  comments, `tools/sitegen` upstream defaults and fixtures
  (`luqman-v1/9router-go`, `luqman-v1.github.io/9router-go`,
  `decolua/9router`), the decolua changelog fallback fetch, the TopBar
  Telegram link (`luqman-v1.github.io/9router-go/tg` — no equivalent page
  exists under the owner's Pages), AGENTS.md §7's upstream PR coordinate
  (`--repo luqman-v1/9router-go`) and the author's local upstream paths,
  `release.yml` `REGISTRY_IMAGE: luqmenul/9router-go` and
  `scripts/notify-telegram.sh` / `scripts/compare-logic.sh` (release-pipeline
  coordinates on the maintainer's own accounts/machines — the owner's call
  to repoint), `pages.yml`'s upstream Pages comment.
- Legacy artefact names: `~/.9router/db/data.sqlite` statements, the
  `9router.db` test fixture, `.gitignore`/`.dockerignore` legacy entries
  (kept alongside the new ones), the freebuff `"9router-<uuid>"` cloaking
  guard (comment + test assert the branded id never leaks).
- Arbitrary test fixtures (e.g. `sig_thought_9router_123`,
  `9routerTestBoundary`, example placeholder host `https://9router.local`).

Out of scope and untouched, per the rename brief: API paths, settings keys,
DB schema/migrations, git history, CHANGELOG.md, historical `.changes/*`
fragments, `evidence/`, `docs/` internal plans/specs, `site/` (generated),
`COMPARISON.md` / `TECHNICAL_DEBT.md` / `ROADMAP.md`, and the main checkout's
directory name.

## Verification (worktree, pre-commit)

- `go build ./...`, `go vet ./...`, `go vet -tags integration ./...` green;
  `tools/migrate-db` builds green. Zero `"9router/proxy` strings remain in
  `.go` files; the residual `9router` grep is fully accounted by the kept
  list above, attribution, and historical files.
- `gofmt -l` flags 47 files — byte-identical list at the base commit
  `94abe356` (pre-existing drift); the rename adds zero.
- Boot smoke on a scratch DB (staging PG): renamed binary
  `cmd/axonrouter-go` → banner "AxonRouter-Go Proxy (1.9.16-exp.1)",
  login 200, `GET /api/auth/status` 200, key mint 201, `GET /v1/models`
  with the key 200. Scratch DB dropped, server killed by PID.
- svelte-check not run in the worktree: it has no `node_modules`
  (`bun run check:svelte` cannot run there; installing was out of bounds
  while the main tree was under a suite run). Web edits were string-only.
  The parent should re-run the 77/57 baseline check after the dist rebuild.
