import { api, type ProviderConnection } from '../../api/client'

/**
 * The media provider views' own source of connection data.
 *
 * The dashboard stopped loading the full connection list on boot (170k rows
 * for a fleet with several thousand accounts) and reads per-provider
 * aggregates from /api/providers/stats instead. The global `connections`
 * array in App.svelte is therefore deliberately empty.
 *
 * The media views had not been migrated: every one of them still filtered
 * that empty array, so a provider with hundreds of live accounts rendered
 * "No connections" — and the per-provider toggle silently did nothing,
 * because it fanned out over the same empty list.
 *
 * This module is the migration target: it fetches one provider's roster on
 * demand, page by page, and caches it. Counts that only need a total come
 * from the aggregate endpoint instead, so a list of 1,245 accounts never
 * crosses the wire to render a badge.
 */

/** One provider's roster, plus the server-reported total for its pager. */
export interface MediaProviderRoster {
  connections: ProviderConnection[]
  total: number | null
  loading: boolean
  error: string | null
}

const EMPTY_ROSTER: MediaProviderRoster = {
  connections: [],
  total: null,
  loading: false,
  error: null,
}

/**
 * Roster state per provider id. A plain object in a $state rune: replacing
 * the whole map on every load would invalidate every reader, and Svelte 5
 * forbids exporting a reassigned $state binding, so the readers below are
 * functions.
 */
const rosters = $state<Record<string, MediaProviderRoster>>({})

/** Page size for the detail-page list. One page of 25 is a screenful. */
export const MEDIA_ROSTER_PAGE_SIZE = 25

function rosterFor(providerId: string): MediaProviderRoster {
  return rosters[providerId] ?? EMPTY_ROSTER
}

/** The cached roster for a provider, or an empty roster before it loads. */
export function mediaRoster(providerId: string): MediaProviderRoster {
  return rosterFor(providerId)
}

/**
 * Loads one page of a provider's roster and caches it.
 *
 * Returns the fresh roster so a caller can read the result directly without
 * waiting for the next reactive tick.
 */
export async function loadMediaRoster(
  providerId: string,
  page = 1,
  pageSize = MEDIA_ROSTER_PAGE_SIZE,
  search?: string
): Promise<MediaProviderRoster> {
  if (!providerId) return EMPTY_ROSTER
  rosters[providerId] = { ...rosterFor(providerId), loading: true, error: null }
  try {
    const data = await api.getConnectionsPage(providerId, page, pageSize, search || undefined)
    const next: MediaProviderRoster = {
      connections: data.connections,
      total: data.total,
      loading: false,
      error: null,
    }
    rosters[providerId] = next
    return next
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err)
    const next: MediaProviderRoster = { ...rosterFor(providerId), loading: false, error: message }
    rosters[providerId] = next
    return next
  }
}

/** Drops a provider's cached roster, so the next read refetches it. */
export function invalidateMediaRoster(providerId: string): void {
  if (rosters[providerId]) {
    rosters[providerId] = { ...rosters[providerId], connections: [], total: null, error: null }
  }
}

/**
 * Flips every connection of one provider to active or inactive.
 *
 * The browser-side fan-out (one PUT per connection) was the real cost: a
 * thousand-account provider turned one click into a thousand sequential
 * requests. The server route does it in one statement, so this is a single
 * request regardless of pool size.
 */
export async function setProviderConnectionsActive(
  providerId: string,
  active: boolean
): Promise<{ ok: boolean; count: number }> {
  const res = await api.bulkActive({ provider: providerId, action: active ? 'activate' : 'deactivate' })
  invalidateMediaRoster(providerId)
  return { ok: Boolean(res?.ok), count: Number(res?.count ?? 0) }
}
