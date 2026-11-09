### TokenHarbor waves: never report success when no candidate was pickable

- Reviewing the v1.9.60 live-proof logs surfaced a latent hole in the
  new hedged-waves path: if the first wave found zero pickable
  candidates (every connection model-locked at that instant), the wave
  runner returned a nil error, which the caller reads as "request
  served" — the client would get an empty 200 for a request nobody
  attempted. The runner now returns a real 503 upstream error in that
  case, so the caller falls through to the sequential path's standard
  lock/wait handling. With candidates present nothing changes.
