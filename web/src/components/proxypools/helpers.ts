import type { ProxyPool } from '../../api/client'

export function formatDateTime(value: string | null | undefined): string {
  if (!value) return 'Never'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'Never'
  return date.toLocaleString()
}

export type PoolStatusFilter = 'all' | 'active' | 'passed' | 'failed'
export type PoolSortOption = 'default' | 'fastest' | 'recently_tested' | 'name'

export function getStatusVariant(status: string | null | undefined): 'success' | 'error' | 'default' {
  if (status === 'active' || status === 'passed') return 'success'
  if (status === 'error' || status === 'failed') return 'error'
  return 'default'
}

export function getLatencyBadge(
  latency?: number | null
): { variant: 'success' | 'warning' | 'error'; text: string } | null {
  if (!latency || latency <= 0) return null
  const ms = Math.round(latency)
  if (ms < 300) {
    return { variant: 'success', text: `⚡ ${ms}ms` }
  }
  if (ms <= 800) {
    return { variant: 'warning', text: `⏳ ${ms}ms` }
  }
  return { variant: 'error', text: `🐢 ${ms}ms` }
}

/**
 * A pool counts as failed when its last probe reported a hard failure. The
 * gateway writes `failed`, upstream `decolua/9router` writes `error`, and rows
 * imported from an upstream database can still carry either.
 */
export function isFailedStatus(pool: ProxyPool): boolean {
  return pool.testStatus === 'failed' || pool.testStatus === 'error'
}

/**
 * Latency is only meaningful for a pool that answered. A failed pool stores 0
 * and an untested one has no measurement, so both must stay unranked.
 * `active` is accepted for the upstream vocabulary, `passed` for ours.
 */
export function hasMeasuredLatency(pool: ProxyPool): boolean {
  return pool.testStatus === 'passed' || pool.testStatus === 'active'
}

export function filterProxyPools(pools: ProxyPool[], filter: PoolStatusFilter): ProxyPool[] {
  if (filter === 'active') return pools.filter((p) => p.isActive === true)
  if (filter === 'passed') return pools.filter((p) => p.testStatus === 'passed')
  if (filter === 'failed') return pools.filter(isFailedStatus)
  return pools
}

// Every comparator falls back to the same tie-break so equal keys keep a
// stable, predictable row order instead of whatever sort happened to produce.
const tieBreakByName = (a: ProxyPool, b: ProxyPool) => (a.name || '').localeCompare(b.name || '')

const rankByLatency = (pool: ProxyPool) =>
  hasMeasuredLatency(pool) && typeof pool.latency === 'number' && pool.latency > 0
    ? pool.latency
    : Infinity

const COMPARATORS: Record<
  Exclude<PoolSortOption, 'default'>,
  (a: ProxyPool, b: ProxyPool) => number
> = {
  fastest: (a, b) => {
    const diff = rankByLatency(a) - rankByLatency(b)
    // Infinity - Infinity is NaN, so only descend into subtraction when the
    // keys actually differ; equal keys fall through to the tie-break.
    return Number.isNaN(diff) || diff === 0 ? tieBreakByName(a, b) : diff
  },
  recently_tested: (a, b) => {
    const at = a.lastTestedAt ? Date.parse(a.lastTestedAt) || 0 : 0
    const bt = b.lastTestedAt ? Date.parse(b.lastTestedAt) || 0 : 0
    return at === bt ? tieBreakByName(a, b) : bt - at
  },
  name: tieBreakByName,
}

export function sortProxyPools(pools: ProxyPool[], option: PoolSortOption): ProxyPool[] {
  if (option === 'default') return pools
  return [...pools].sort(COMPARATORS[option])
}

export function poolStatusCounts(pools: ProxyPool[]): {
  active: number
  passed: number
  failed: number
} {
  return {
    active: pools.filter((p) => p.isActive === true).length,
    passed: pools.filter((p) => p.testStatus === 'passed').length,
    failed: pools.filter(isFailedStatus).length,
  }
}

/**
 * The Type filter has two families in one list (owner decision, 2026-11-07):
 * protocols, derived from the proxy URL's scheme, and relay origins, taken
 * from the stored `type` the deploy flows stamp. A relay pool's URL scheme
 * says nothing about what it is, so the stored type wins whenever it names
 * a relay; everything else is classified by scheme.
 */
const RELAY_TYPES = new Set(['vercel', 'cloudflare', 'deno', 'netlify', 'relay'])

export function poolTypeKey(pool: ProxyPool): string {
  const stored = (pool.type || '').trim().toLowerCase()
  if (RELAY_TYPES.has(stored)) return stored
  try {
    const scheme = new URL(pool.proxyUrl || '').protocol.replace(/:$/, '').toLowerCase()
    if (scheme === 'socks') return 'socks5'
    if (scheme) return scheme
  } catch {
    // An unparseable URL falls through to the stored type / default below.
  }
  return stored || 'http'
}

const TYPE_LABELS: Record<string, string> = {
  http: 'HTTP',
  https: 'HTTPS',
  socks4: 'SOCKS4',
  socks5: 'SOCKS5',
  cloudflare: 'Cloudflare',
  vercel: 'Vercel',
  deno: 'Deno',
  netlify: 'Netlify',
  relay: 'Relay',
}

export function poolTypeLabel(key: string): string {
  return TYPE_LABELS[key] || key.toUpperCase()
}

const TYPE_ORDER = [
  'http',
  'https',
  'socks4',
  'socks5',
  'cloudflare',
  'vercel',
  'deno',
  'netlify',
  'relay',
]

export function poolTypeOptions(
  pools: ProxyPool[]
): { value: string; label: string; count: number }[] {
  const counts = new Map<string, number>()
  for (const pool of pools) {
    const key = poolTypeKey(pool)
    counts.set(key, (counts.get(key) || 0) + 1)
  }
  return Array.from(counts.entries())
    .sort(([a], [b]) => {
      const ia = TYPE_ORDER.indexOf(a)
      const ib = TYPE_ORDER.indexOf(b)
      if (ia !== -1 || ib !== -1) return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib)
      return a.localeCompare(b)
    })
    .map(([value, count]) => ({ value, label: poolTypeLabel(value), count }))
}

export function poolMatchesSearch(pool: ProxyPool, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  return [pool.name, pool.proxyUrl, pool.group]
    .some((field) => (field || '').toLowerCase().includes(q))
}

export interface GroupSummary {
  name: string
  pools: ProxyPool[]
  totalCount: number
  activeCount: number
  inCooldownCount: number
  avgFitness: number
  avgLatency: number
  strictCount: number
}
