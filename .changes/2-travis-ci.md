### CI moved to Travis CI

- All continuous integration for this repo — the lint, changelog-fragment,
  test, Windows service, race, integration, Docker-verify and cross-compile
  jobs, the project-site build and deploy, and the release pipeline that
  publishes `ghcr.io/rmz17x/axonrouter-go` and the GitHub Release — now runs
  on Travis CI from a single `.travis.yml`. The GitHub Actions workflows are
  removed; there is no Actions fallback in this repo.
- Release behaviour is unchanged for consumers: main pushes publish the
  `latest`, `main` and `sha-<short>` image tags, and a `v*` tag publishes
  the tag image plus a GitHub Release carrying the five binaries and
  `SHA256SUMS.txt`.
