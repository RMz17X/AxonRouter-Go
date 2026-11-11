title: Provider Detail: account search over the provider's whole list
version: v1.10.39

## Added
- Provider Detail gains the Quota Tracker's account search: a search
  form above the account list (submit on Enter or the button, clear
  button while applied) that finds accounts by name, email,
  connection id, or the display identity (displayName, username,
  githubLogin) — the same field set the Quota Tracker search covers.
- GET /api/connections accepts a search parameter and filters the
  provider's whole list before pagination, so a match on any page is
  findable and X-Total-Count reports the filtered total; the pager
  and the "Showing … of N accounts" line follow the filtered count
  while a search is applied, and an empty result says so instead of
  falling back to the unfiltered list.
