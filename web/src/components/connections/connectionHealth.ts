import type { ModelAvailabilityEntry, ProviderConnection } from '../../api/client'

/**
 * Per-connection health for the provider detail page: one state per
 * connection (active / error / cooldown / locked / disabled) plus the live
 * locks behind it.
 *
 * The state partition mirrors the backend's canonical classification in
 * internal/providers/connection_status.go (EffectiveStatusWithError), in
 * that file's own precedence order — the toggle wins first, then the
 * throttle classification, then the harder error verdicts; a live lock
 * that the status engine folded into a healthy verdict still surfaces as
 * "locked" here so the operator can see which model is parked and until
 * when. The status itself is read from the server-computed
 * `effectiveStatus` field; the client never re-derives it from raw
 * testStatus/lastError except as a fallback for payloads from a build
 * that predates the field (or emitted it malformed).
 */

export type ConnectionHealthState = 'active' | 'error' | 'cooldown' | 'locked' | 'disabled' | 'validation'

export interface ConnectionLockInfo {
  /** Locked model id, or "__all" for an account-wide park. */
  model: string
  /** ISO timestamp the lock lapses. */
  until: string
}

export interface ConnectionHealth {
  state: ConnectionHealthState
  /** The effective status the state was derived from. */
  effectiveStatus: string
  /** Live locks, soonest expiry first. */
  locks: ConnectionLockInfo[]
  lastError: string
  /** Disabled because the upstream disabled the account (ToS / fatal). */
  tosDisabled: boolean
}

const KNOWN_EFFECTIVE_STATUSES = new Set([
  'active',
  'success',
  'unavailable',
  'quota_exhausted',
  'rate_limited',
  'error',
  'expired',
  'disabled',
])

const ERROR_STATUSES = new Set(['error', 'unavailable', 'quota_exhausted', 'expired'])

// Substrings that mark an upstream account disable (mirrors the backend's
// isFatalAccountDisableError list in the dashboard connections handler);
// the ToS subset decides the "disabled · ToS" label.
const TOS_DISABLE_MARKERS = ['terms of service', 'tos_violation', 'account disabled', 'disabled in this account']

// Markers of Google's account-verification demand (mirrors the backend's
// isValidationReq checks): the account is parked until the operator
// verifies it upstream, which is a different, recoverable state from a
// terminal disable — so it gets its own health state.
const VALIDATION_MARKERS = ['validation_required', 'verify your account']

function parseTimeMs(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value
  if (typeof value === 'string' && value.trim() !== '') {
    const t = new Date(value).getTime()
    return Number.isFinite(t) ? t : 0
  }
  return 0
}

/** Live locks carried on the connection payload itself: the flattened
 * modelLock_<model> keys the sanitize step copies through, plus the
 * account-wide park expiries. The locks census SQL prefilter keys on the
 * modelLocks object shape, so a connection whose only lock is a flattened
 * key can be missing from the census; the payload is the same source the
 * row's ⏱ timer already reads. */
export function payloadLocks(conn: ProviderConnection, now: number): ConnectionLockInfo[] {
  const rec = conn as unknown as Record<string, unknown>
  const locks: ConnectionLockInfo[] = []
  for (const [key, value] of Object.entries(rec)) {
    if (!key.startsWith('modelLock_')) continue
    const model = key.slice('modelLock_'.length)
    const untilMs = parseTimeMs(value)
    if (model && untilMs > now) locks.push({ model, until: new Date(untilMs).toISOString() })
  }
  for (const field of ['rateLimitedUntil', 'oauthLockedUntil'] as const) {
    const untilMs = parseTimeMs(rec[field])
    if (untilMs > now) locks.push({ model: '__all', until: new Date(untilMs).toISOString() })
  }
  return locks
}

/** Live locks from the census rows of one connection: "cooldown" rows with
 * an unexpired `until`. Bare "unavailable" rows carry no lock. */
export function censusLocks(entries: ModelAvailabilityEntry[] | undefined, now: number): ConnectionLockInfo[] {
  if (!entries) return []
  const locks: ConnectionLockInfo[] = []
  for (const entry of entries) {
    if (entry.status !== 'cooldown' || !entry.until) continue
    const untilMs = parseTimeMs(entry.until)
    if (untilMs > now) locks.push({ model: entry.model, until: new Date(untilMs).toISOString() })
  }
  return locks
}

function mergeLocks(census: ConnectionLockInfo[], payload: ConnectionLockInfo[]): ConnectionLockInfo[] {
  const byModel = new Map<string, ConnectionLockInfo>()
  for (const lock of payload) byModel.set(lock.model, lock)
  // The census is the canonical lock source; it wins per model.
  for (const lock of census) byModel.set(lock.model, lock)
  return [...byModel.values()].sort((a, b) => a.until.localeCompare(b.until))
}

function normalizeEffectiveStatus(conn: ProviderConnection): string {
  const raw = typeof conn.effectiveStatus === 'string' ? conn.effectiveStatus.trim().toLowerCase() : ''
  if (KNOWN_EFFECTIVE_STATUSES.has(raw)) return raw
  // Fallback for payloads without a usable server verdict (older build, or
  // the "<nil>" the pre-fix decoration emitted for never-probed rows).
  const isActive = conn.isActive === 1 || (conn.isActive as unknown) === true
  if (!isActive) return 'disabled'
  if (conn.isEffectivelyActive === true) return 'active'
  const testStatus = typeof conn.testStatus === 'string' ? conn.testStatus.trim().toLowerCase() : ''
  if (KNOWN_EFFECTIVE_STATUSES.has(testStatus)) return testStatus
  return 'active'
}

export function isTosDisabled(conn: ProviderConnection): boolean {
  const reason = typeof conn.disabledReason === 'string' ? conn.disabledReason.toLowerCase() : ''
  if (reason.includes('tos') || reason.includes('terms')) return true
  const err = typeof conn.lastError === 'string' ? conn.lastError.toLowerCase() : ''
  return TOS_DISABLE_MARKERS.some((marker) => err.includes(marker))
}

export function deriveConnectionHealth(
  conn: ProviderConnection,
  censusEntries: ModelAvailabilityEntry[] | undefined,
  now: number,
): ConnectionHealth {
  const effectiveStatus = normalizeEffectiveStatus(conn)
  const locks = mergeLocks(censusLocks(censusEntries, now), payloadLocks(conn, now))
  const lastError = typeof conn.lastError === 'string' ? conn.lastError : ''

  // The flag is the stored status verdict; the verification URL is
  // never stored (fragment 1061), so it plays no part here. The
  // error-marker clause stays for rows parked before the status field
  // existed.
  const needsVerification =
    (typeof conn.status === 'string' && conn.status.toLowerCase() === 'validation_required') ||
    (effectiveStatus === 'disabled' && VALIDATION_MARKERS.some((marker) => lastError.toLowerCase().includes(marker)))

  // Precedence mirrors EffectiveStatusWithError: the disabled toggle wins,
  // then the throttle verdict, then the error family. A connection whose
  // locks did not change its verdict (a parked model on an otherwise
  // healthy account) reads as locked rather than active. The one
  // deliberate deviation: a validation-flagged connection reads as
  // "validation", not "disabled" — it is parked too, but the operator's
  // next action (verify upstream, then recheck) differs from a terminal
  // disable.
  let state: ConnectionHealthState
  if (needsVerification) state = 'validation'
  else if (effectiveStatus === 'disabled') state = 'disabled'
  else if (effectiveStatus === 'rate_limited') state = 'cooldown'
  else if (ERROR_STATUSES.has(effectiveStatus)) state = 'error'
  else if (locks.length > 0) state = 'locked'
  else state = 'active'

  return { state, effectiveStatus, locks, lastError, tosDisabled: state === 'disabled' && isTosDisabled(conn) }
}

/** Group census rows by connection id (all providers; callers filter). */
export function groupAvailabilityByConnection(
  entries: ModelAvailabilityEntry[],
): Map<string, ModelAvailabilityEntry[]> {
  const grouped = new Map<string, ModelAvailabilityEntry[]>()
  for (const entry of entries) {
    const list = grouped.get(entry.connectionId)
    if (list) list.push(entry)
    else grouped.set(entry.connectionId, [entry])
  }
  return grouped
}

export function healthByConnection(
  connections: ProviderConnection[],
  censusByConnection: Map<string, ModelAvailabilityEntry[]>,
  now: number,
): Map<string, ConnectionHealth> {
  const out = new Map<string, ConnectionHealth>()
  for (const conn of connections) {
    out.set(conn.id, deriveConnectionHealth(conn, censusByConnection.get(conn.id), now))
  }
  return out
}

/** Remaining-time label in the shape of the detail view's ⏱ timer. */
export function formatRemaining(untilMs: number, now: number): string {
  const diff = Math.max(0, Math.floor((untilMs - now) / 1000))
  if (diff < 60) return `${diff}s`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ${diff % 60}s`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`
  return `${Math.floor(diff / 86400)}d ${Math.floor((diff % 86400) / 3600)}h`
}

/** Human label for an effective status ("quota_exhausted" → "quota exhausted"). */
export function statusLabel(effectiveStatus: string): string {
  return effectiveStatus.replace(/_/g, ' ')
}
