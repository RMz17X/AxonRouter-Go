title: Quota Tracker: the accounts page lives in the URL (?page=)
version: v1.10.38

## Added
- Quota Tracker pagination is deep-linkable like the provider filter:
  moving through the accounts pages rewrites ?page= in the address
  bar (replaceState, page 1 removes the parameter), and a ?page=
  present on load seeds the pagination, so a refresh — or a shared
  link — lands on the same page instead of restarting at page 1. An
  out-of-range page is clamped by the server on the first fetch and
  the URL is corrected to match.
