title: Playground inventory: build each provider's unpinned catalog once
version: v1.10.38

## Fixed
- The Playground inventory's first build after a restart still took
  minutes: every unpinned connection (up to the per-provider cap)
  resolved its provider's live catalog per credential, so a farm of
  interchangeable keys fanned out to one upstream fetch per key. The
  catalog an unpinned connection publishes is its provider's, so it
  is now built once per provider — the same first-connection stance
  /v1/models already takes — while pinned connections keep their own
  per-account builds. Cold inventory builds drop from minutes to
  seconds; the five-minute cache from v1.10.37 covers repeat opens.
