### CI moved to Travis CI — one lean job, PRs and release tags only

- All continuous integration for this repo now runs on Travis CI from a
  single `.travis.yml`: one Linux job that checks the changelog fragment
  (PRs only), builds the web SPA, runs the web tests and the svelte
  ratchet, then `go vet` and `go test`, builds the release binary with
  its PGO assertion, and — on release tags only — publishes
  `ghcr.io/rmz17x/axonrouter-go` and the GitHub Release. The GitHub
  Actions workflows are removed; there is no Actions fallback.
- Builds fire only on pull requests and release tags. Branch pushes
  never build (Travis repo setting), so CI stops costing per commit.
- The `latest` image tag now moves at release tags (previously it moved
  on main pushes); a tag publishes `<tag>`, `sha-<short>` and `latest`,
  and the GitHub Release still carries the five binaries and
  `SHA256SUMS.txt`.
