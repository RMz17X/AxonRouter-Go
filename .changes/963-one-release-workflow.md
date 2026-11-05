## One release workflow; the Telegram notify job is gone

Owner order (2026-11-06): a tag push used to fan out into two separate
Actions runs — Publish Docker Image (docker-publish.yml) and Release
(release.yml) — paying the workflow overhead twice, and the Release
run ended with a Telegram announcement the owner does not want.

- docker-publish.yml is deleted; its job moves into release.yml as
  the `docker` job, unchanged in behaviour (same GHCR login, metadata
  tags, buildx cache, linux/amd64): it runs on every main push and
  every version tag.
- The `channel` and `binaries` jobs keep their tag-push behaviour and
  are gated to tags (or manual dispatch), so a main push runs only
  the image job.
- The `notify` job is removed, along with the release-notes artifact
  step whose only consumer it was. scripts/notify-telegram.sh stays
  in the tree, unused by CI.
- A release is now a single Actions run: image + binaries together,
  which is also the run dewacloud's auto-pull follows.

This supersedes the arrangement recorded in fragment 960 (which had
left image publishing in its own workflow file).
