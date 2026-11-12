title: Cap the staging build's CPU with a BUILD_JOBS budget
version: v1.10.46

## Changed
- The Docker build no longer takes every core on the host. The Go
  compiler parallelises across all cores by default and neither the
  Dockerfile nor the compose build set any limit, so each staging
  build saturated all 12 cores of hijilaptop — a laptop that is
  simultaneously the dev box and the agent host — for minutes.
  docker-compose.build.yml now passes BUILD_JOBS=6 as a build arg
  (the one knob, documented in place); the Dockerfile's Go stage
  builds with GOMAXPROCS=6 and GOFLAGS=-p=6, and the web stage caps
  its Go-based bundler tools (esbuild) at GOMAXPROCS=2. Build output
  is unchanged (parallelism only); builds take somewhat longer in
  exchange for a laptop that stays usable while they run.
