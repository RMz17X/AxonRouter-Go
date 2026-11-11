title: Playground: one settings button, and a cached model inventory
version: v1.10.37

## Fixed
- Playground toolbar: the prompt chip ("No prompt") and the params chip
  ("Temp 1.0") both opened the same Session settings modal — merged
  into a single settings chip ("No prompt · Temp 1.0") that opens it.
- GET /api/playground/models rebuilt the full model inventory on every
  request — a walk over every connection (tens of thousands of rows on
  farm fleets) plus per-provider catalog work, measured at 8–32 s. The
  inventory is now cached for five minutes and served stale with a
  background refresh past expiry, so the picker opens instantly; the
  per-provider contribution cap is also checked before the cooldown
  parse it makes unnecessary.
