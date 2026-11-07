# 989 — STREAM/JSON mode information on the Usage page, wired from the dispatch layer

## Symptom

The owner asked for the stream-or-JSON information AxonRouter JS shows on
its Usage page: a STREAM/JSON chip on each in-flight card, mode pills
(Stream n / JSON n) on the realtime card, and the mode carried by the
request rows. The Go port had deliberately omitted all of it (recorded
in the 907 parity notes) because no Go usage surface recorded the mode:
the field existed on the recent-request wire type and was populated
nowhere.

## Root cause

The information died at the dispatch boundary. The forwarder knows the
mode (`forwardRequestParams.IsStream`, parsed from the client's
request), but `UsageLogInfo` — the struct every usage surface reads —
carried no such field, the in-flight tracker aggregated its counters
without a mode dimension, and the stored request-detail payloads never
recorded it. Three surfaces, one missing wire.

## Fix

Backend:
- `shared.UsageLogInfo` gains `IsStream`; all three constructions in
  `tryForwardWithConnection` (success, failure, proxy-pool failure) set
  it from the forwarder's own flag.
- In-flight tracker: `TrackPendingRequested` takes the mode and keeps
  two shadow counter families (`pending/modelstream/`,
  `pending/acctstream/`) incremented with the same deltas as the totals.
  `buildPayload` splits each aggregate key into up to two rows — the
  streaming share and the rest — clamped so a leaked counter can never
  report more streaming requests than the key holds; a drained account
  pair deletes its shadow, and a drained model key deletes its own.
  `ActiveRequest.isStream` is emitted without omitempty: on a live row,
  false is information. The `/api/usage/active-requests` shape passes it
  through.
- Completed requests: both `PushRecent` sites (success and failure) set
  `RecentRequest.IsStream` from the log info, and both stored detail
  payloads (success and failure) record `"isStream"`.
- Detail list: `RequestDetailListRow` extracts the flag from the stored
  payload; rows written before it existed read as true — upstream's own
  default for an unrecorded mode (`chatCore/requestDetail.js`:
  `isStream ?? true`). `detailListItem` emits it on every row.

Frontend:
- The in-flight modal card draws upstream's ModeBadge (STREAM with the
  wifi_tethering icon / JSON with the code icon, primary chip) at the
  top right, beside the provider alias prefix fragment 988 added.
- The realtime table and its mobile cards show the same chip beside the
  provider badge; the Details tab's Provider cell carries it too.
- The realtime filter pills gain Stream (n) and JSON (n) in the same
  single-select group as All/Success/Failed — upstream's filterType
  model — counting and filtering on the recorded mode.
- Mechanical test fix: three `TrackPendingRequested` call sites in
  `tracker_test.go` gain the new trailing argument (signature change);
  no expectations changed.

## Verification

- `go build ./...` and `go vet` over the touched packages are clean.
- A temporary in-package probe (run, then deleted) drove the tracker
  through the memory store: two streaming + one JSON request on one
  account key plus one streaming direct request produced exactly the
  split rows {account ×2 STREAM, account ×1 JSON, direct ×1 STREAM},
  and finishing the streaming requests drained only their rows.
- svelte-check (with `--tsconfig ./tsconfig.app.json`) totals identical
  to the pristine baseline (89 errors / 56 warnings, zero new) and the
  web build succeeds.
- Pixel proof rides on the next staging view, like 988: the chips render
  from live traffic and recorded rows, which no environment reachable
  from here can stage on demand.
