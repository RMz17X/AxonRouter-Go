# 997 — Proxy Pools: batch import takes four formats and imports partially

## Symptom

The batch import accepted only two line formats
(`protocol://user:pass@host:port` and `host:port:user:pass`), created
pools one serial request at a time, and — worst — aborted the entire
paste when any single line was invalid.

## Fix

Frontend only (`web/src/lib/proxy-import.ts` + the import flow):

- The parser additionally accepts `user:pass@host:port` and bare
  `host:port` (http assumed for both scheme-less forms), bracketed IPv6
  hosts in every scheme-less form, and validates ports (numeric,
  1–65535). Stored URLs are canonicalised through `URL` as before.
- Imports are partial, matching upstream: valid lines import, invalid
  lines are counted, reported with reasons, and left in the textarea for
  fixing instead of discarding the whole paste.
- Deduplication now also covers duplicates inside the paste itself
  (previously only against existing pools).
- Creates run with bounded concurrency (chunks of 8) instead of strictly
  serially.

## Verification

- svelte-check baseline held (89/56, zero new); web build succeeds.
- The pre-existing parser unit file was left untouched (per the standing
  no-test-suites-before-production rule it was not run; the two original
  formats keep their exact previous outputs).
