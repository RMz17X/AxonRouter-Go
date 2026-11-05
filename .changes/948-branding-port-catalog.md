### feat(branding,ops): AxonRouter identity completed, port 3177, GHCR publishing

- The official AxonRouter logo, favicon and PWA icons replace the legacy
  9router artwork everywhere; the CLI-tools wire field `has9Router` is
  renamed `hasAxonRouter` to match.
- The default gateway port moves 20130 → 3177, consistently across
  config defaults, the Dockerfile (ENV/EXPOSE/healthcheck), both compose
  stacks and the cluster nginx upstream.
- A GitHub Actions workflow builds and publishes the Docker image to
  GHCR whenever a `v*` tag is pushed.
- Grok model catalog updated (4.7, 4.6, 4, 3, code-fast) with tuned
  live-catalog timeouts; minimax-code-global choices translation fixed;
  Tencent/CodeBuddy `displayMsg`/`msg` error text is extracted for
  fallback decisions; dashboard numbers auto-compact to K/M/B; the
  topology card's blocking "No providers connected" overlay is gone.
