# 990 — Realtime table: provider folds under the model, new API Key column (key name + client IP)

## Symptom

The owner asked for two changes to the Usage page's realtime table:
merge the Provider column into the Model column with the provider shown
underneath the model, and add a new column carrying the client API key
that was used — its name, and the IP the call came from.

## Root cause

The layout half was a straight table restructure, but the data half did
not exist. The detail payload's `apiKey` field is the UPSTREAM provider
credential the request used (that is what attributed requests to backend
accounts; the caller's key was only ever recorded, masked, on the usage
history row for per-key budgets). The client key's NAME and the client
IP were recorded nowhere in the request details: the name never left the
auth middleware's context value, and the IP was never resolved on the
gateway path at all — the product's resolver (`auth.LoginClientIP`) was
only used by the dashboard login.

## Fix

Backend:
- The three gateway entry points that already stamp the dispatching key
  onto the context (chat completions, messages, responses) now also
  stamp the resolved client IP via a new `withClientIP` /
  `clientIPFromContext` pair (`client_ip.go`), resolved by
  `requestClientIP` through `auth.LoginClientIP` — the one sanctioned
  resolver (edge-stamped real IP with peer proof, TRUSTED_PROXIES /
  TRUST_PROXY chains, Cloudflare when trusted, else the socket address).
- Both detail payload builders (success and failure) merge
  `callerDetailFields(ctx)`: `apiKeyName` and the masked `clientApiKey`
  from the authenticated key in context, plus `clientIp`. Absent pieces
  stay absent — a keyless caller has nothing to record. The existing
  `apiKey` field is untouched.
- The detail list layer extracts the three fields
  (`RequestDetailListRow` + select + scan) and `detailListItem` emits
  them (`apiKeyName`, `clientApiKey`, `clientIp`, all omitempty). Rows
  written before this change read as empty, which the UI renders as "—"
  rather than inventing a caller.

Frontend (`AxonRealtime.svelte` + types):
- The Provider column is gone. The Model cell now stacks: the model
  line, and underneath it the provider badge with the STREAM/JSON chip
  from fragment 989.
- A new API Key column sits after Upstream Account: the key's name on
  the first line (the masked key when the key has no name, "—" when
  there was no key), and the client IP in small mono underneath.
- Mobile cards gain a "Key: <name> · <ip>" line when the row carries
  caller data; the card search now also matches key name, masked key,
  and IP.

## Verification

- `go build ./...` and `go vet` over the touched packages are clean;
  gofmt clean.
- svelte-check (with `--tsconfig ./tsconfig.app.json`) totals identical
  to the pristine baseline (89 errors / 56 warnings, zero new) and the
  web build succeeds.
- The caller chain is the one the codebase already trusts: the
  authenticated key object the auth middleware injects (the same source
  the per-key budget accounting reads), and the login path's IP
  resolver. Live pixel proof rides on the next staging view, like 988
  and 989 — the column populates from real gateway traffic, which no
  environment reachable from here can stage on demand.
