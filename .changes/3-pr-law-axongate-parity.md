### Pull-request & release law (AxonGate parity)

- Adopted the AxonGate PR law for this repo (AGENTS.md §10, owner's order 2026-11-28): all work lands through PRs from `pr/<slug>` branches in `.worktree/` worktrees and is merged by its author; ordinary PRs never move a version; releases are batched into `release/vX.Y.Z` PRs that bump exactly one patch and fold the pending fragments into one CHANGELOG entry.
- Added `scripts/check-version-bump.sh`, wired into the Travis PR build: an ordinary PR that moves VERSION, version.json's latestVersion, the updater's CurrentVersion, or CHANGELOG.md now fails CI.
- GitHub enforcement matches AxonGate exactly: `pr-and-checks` and `no-force-no-delete` rulesets on main, plus auto-delete of merged branches.
