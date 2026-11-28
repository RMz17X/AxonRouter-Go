#!/usr/bin/env bash
# Version guard — BATCHED RELEASES (owner's order, 2026-11-28: the
# AxonGate PR law, AGENTS.md §10, applied to this repo unchanged in
# effect — this script is the AxonGate guard adapted to this repo's
# version carriers).
#
# Versions no longer move per PR. Ordinary PRs (any branch that is not
# release/*) must NOT change VERSION, the latestVersion field of
# version.json, the CurrentVersion in internal/updater/updater.go, or
# CHANGELOG.md. A release PR (branch release/*) is the only place
# versions move: VERSION must rise by exactly one patch over the base,
# version.json and CurrentVersion must carry the same new value, and
# CHANGELOG.md must be updated (the batch entry folded from .changes/).
#
# Contexts without a PR head ref infer the mode from the diff itself: a
# change that moves no version is an ordinary change and passes; one
# that moves a version is validated with the release arithmetic against
# VERSION_BASE_SHA.
set -euo pipefail

BASE="${VERSION_BASE_SHA:-}"
if [ -z "$BASE" ] || [ "${BASE//0/}" = "" ]; then
  echo "version-guard: no comparable base SHA, skipping"
  exit 0
fi

if ! git cat-file -e "$BASE^{commit}" 2>/dev/null; then
  git fetch --no-tags --depth=1 origin "$BASE" >/dev/null 2>&1 || {
    echo "version-guard: cannot fetch base $BASE, skipping"
    exit 0
  }
fi

changed=$(git diff --name-only "$BASE" HEAD --)
if [ -z "$changed" ]; then
  echo "version-guard: no changes, skipping"
  exit 0
fi

touches() { printf '%s\n' "$changed" | grep -qx "$1"; }

file_version() { tr -d '[:space:]' < VERSION 2>/dev/null || echo "0.0.0"; }
file_version_at() { git show "$1:VERSION" 2>/dev/null | tr -d '[:space:]' || echo "0.0.0"; }
json_version() { sed -n 's/.*"latestVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' version.json 2>/dev/null | head -n 1; }
json_version_at() { git show "$1:version.json" 2>/dev/null | sed -n 's/.*"latestVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1; }
go_version() { sed -n 's/.*CurrentVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' internal/updater/updater.go 2>/dev/null | head -n 1; }
go_version_at() { git show "$1:internal/updater/updater.go" 2>/dev/null | sed -n 's/.*CurrentVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1; }

bump_patch() {
  # $1 = X.Y.Z; prints X.Y.(Z+1), or nothing when unparsable.
  local major minor patch
  IFS='.' read -r major minor patch <<< "$1"
  case "$major$minor$patch" in *[!0-9]*|"") return 1 ;; esac
  printf '%s.%s.%s' "$major" "$minor" "$((patch + 1))"
}

cur_v=$(file_version);  prev_v=$(file_version_at "$BASE");  [ -n "$prev_v" ] || prev_v="0.0.0"
cur_j=$(json_version);  prev_j=$(json_version_at "$BASE");  [ -n "$cur_j" ] || cur_j="0.0.0"; [ -n "$prev_j" ] || prev_j="0.0.0"
cur_g=$(go_version);    prev_g=$(go_version_at "$BASE");    [ -n "$cur_g" ] || cur_g="0.0.0"; [ -n "$prev_g" ] || prev_g="0.0.0"

HEAD_REF="${VERSION_HEAD_REF:-}"
case "$HEAD_REF" in
  release/*) MODE="release" ;;
  ?*)        MODE="ordinary" ;;
  *)
    # No head ref: infer from the diff.
    if touches "VERSION" || touches "CHANGELOG.md" || [ "$cur_j" != "$prev_j" ] || [ "$cur_g" != "$prev_g" ]; then
      MODE="release"
    else
      MODE="ordinary"
    fi
    ;;
esac

echo "version-guard: mode=$MODE base=$BASE head_ref=${HEAD_REF:-<none>}"
failed=0

if [ "$MODE" = "ordinary" ]; then
  if touches "VERSION"; then
    echo "::error::Ordinary PRs must not modify VERSION ($prev_v -> $cur_v). Versions move only in a release PR (branch release/*)."
    failed=1
  fi
  if touches "CHANGELOG.md"; then
    echo "::error::Ordinary PRs must not modify CHANGELOG.md (§7.6). Changelog entries are written once per release, in the release PR."
    failed=1
  fi
  if [ "$cur_j" != "$prev_j" ]; then
    echo "::error::Ordinary PRs must not change version.json latestVersion ($prev_j -> $cur_j). Versions move only in a release PR (branch release/*)."
    failed=1
  fi
  if [ "$cur_g" != "$prev_g" ]; then
    echo "::error::Ordinary PRs must not change CurrentVersion in internal/updater/updater.go ($prev_g -> $cur_g). Versions move only in a release PR (branch release/*)."
    failed=1
  fi
  if [ "$failed" = 0 ]; then
    echo "version-guard: ordinary change, no version moved — OK (version $cur_v)"
  fi
  exit "$failed"
fi

# --- release mode -----------------------------------------------------------
expected=$(bump_patch "$prev_v") || {
  echo "::error::Cannot parse base VERSION '$prev_v' as X.Y.Z"
  exit 1
}
if [ "$cur_v" = "$expected" ]; then
  echo "version-guard: VERSION $prev_v -> $cur_v (exactly +1 patch)"
else
  echo "::error::A release must bump VERSION by exactly one patch ($prev_v -> $expected), got $cur_v"
  failed=1
fi
if [ "$cur_j" = "$cur_v" ]; then
  echo "version-guard: version.json latestVersion = $cur_j"
else
  echo "::error::version.json latestVersion ($cur_j) must equal VERSION ($cur_v) in a release"
  failed=1
fi
if [ "$cur_g" = "$cur_v" ]; then
  echo "version-guard: updater CurrentVersion = $cur_g"
else
  echo "::error::CurrentVersion in internal/updater/updater.go ($cur_g) must equal VERSION ($cur_v) in a release"
  failed=1
fi
if touches "CHANGELOG.md"; then
  echo "version-guard: CHANGELOG.md updated"
else
  echo "::error::A release must update CHANGELOG.md with the batch entry for $cur_v"
  failed=1
fi
exit "$failed"
