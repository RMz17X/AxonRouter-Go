# 993 — Proxy Pools: search box and a Type filter

## Symptom

The Proxy Pools page could not be searched and had no type filter (owner
report, 2026-11-07): with dozens of pools and 10 rows per page, finding one
proxy meant paging by eye. The assignment pickers elsewhere already treat
type as a first-class dimension (`type:<type>` bindings); the pools page
itself did not.

## Fix

Frontend only (`ProxyPoolsView.svelte` toolbar + `proxypools/helpers.ts`):

- A search input filters the list by name, proxy URL, or group
  (case-insensitive substring, applied inside the existing filter chain).
- A Type filter (CountedSelect, counted options, only types present in the
  data) with two families in one list, per the owner's decision: protocols
  derived from the proxy URL's scheme (HTTP / HTTPS / SOCKS4 / SOCKS5 —
  `socks` normalises to SOCKS5), and relay origins taken from the stored
  `type` (Cloudflare / Vercel / Deno / Netlify / Relay), which wins over
  the scheme whenever it names a relay.
- Both join the page-reset effect and the empty-state Reset Filter button.

## Verification

- svelte-check with `--tsconfig ./tsconfig.app.json` stays at the pristine
  baseline (89 errors / 56 warnings, zero new) and the web build succeeds.
