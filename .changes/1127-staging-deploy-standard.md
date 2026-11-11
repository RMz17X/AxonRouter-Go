title: Staging deploy standard: build compose only, documented
version: v1.10.37

## Changed
- docker-compose.build.yml: the bundled postgres is gated behind the
  local-pg profile and the app no longer depends on it — staging uses
  the shared global-postgres — and the app environment carries
  TZ: Asia/Jakarta, matching the production stack.
- AGENTS.md §9 records the staging deploy standard: build with the
  build compose, then a bare up -d; never the production single-file
  stack, never retagged images.
