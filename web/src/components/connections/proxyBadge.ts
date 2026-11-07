import type { ProviderConnection, ProxyPool } from '../../api/client'
import { parseProxyTargetParts } from '../../lib/proxyBindings'

interface BadgeSelector {
  kind: 'group' | 'type'
  name: string
}

/**
 * Resolve a possibly multi-selector target ("group:A,group:B",
 * "group:A,type:http", bare names mixed in) to the group/type selectors
 * it names. A bare part is a group when pools carry the name as a
 * group, else a type when pools carry it as one, else a group — the
 * same reading the single bare-name branch below applies.
 */
function badgeSelectors(target: string, pools: ProxyPool[]): BadgeSelector[] {
  return parseProxyTargetParts(target).map((part) => {
    if (part.kind !== 'bare') return { kind: part.kind, name: part.name }
    if (pools.some((p) => p.group === part.name)) return { kind: 'group' as const, name: part.name }
    if (pools.some((p) => p.type === part.name)) return { kind: 'type' as const, name: part.name }
    return { kind: 'group' as const, name: part.name }
  })
}

/** Active pools matched by ANY selector, deduplicated by pool id. */
function countActiveUnion(pools: ProxyPool[], selectors: BadgeSelector[]): number {
  const ids = new Set<string>()
  for (const p of pools) {
    if (!p.isActive) continue
    if (selectors.some((s) => (s.kind === 'group' ? p.group === s.name : p.type === s.name))) {
      ids.add(p.id)
    }
  }
  return ids.size
}

function multiLabel(selectors: BadgeSelector[]): { word: string; smart: string; names: string } {
  const names = selectors.map((s) => s.name).join(' + ')
  if (selectors.every((s) => s.kind === 'group')) return { word: 'Groups', smart: 'Smart Groups', names }
  if (selectors.every((s) => s.kind === 'type')) return { word: 'Types', smart: 'Smart Types', names }
  return { word: 'Multi', smart: 'Smart Multi', names }
}

export interface ProxyBadgeInfo {
  /** True when the connection is bound to a pool or a legacy per-connection proxy. */
  hasAnyProxy: boolean
  /** 'success' when bound to an active pool, 'error' for inactive/missing pools and legacy proxies. */
  variant: 'success' | 'error'
  /** "Pool: <name>" / "Pool: <id> (inactive/missing)" / "Legacy: <url>". */
  displayText: string
  /** Endpoint with credentials and path stripped, e.g. "http://proxy.example.com:8080". */
  maskedProxyUrl: string
  noProxyText: string
}

/**
 * Port of upstream ConnectionRow's proxy badge:
 *   const boundProxyPoolId = connection.providerSpecificData?.proxyPoolId
 *   boundProxyPool = pools.find(p => p.id === boundProxyPoolId)
 *   proxyBadgeVariant = success when the pool is active, error when bound to a
 *   missing/inactive pool or a legacy proxy, default otherwise.
 * The endpoint is masked via `new URL()` (protocol + hostname + port only) so
 * proxy credentials never reach the dashboard.
 */
export function proxyBadgeInfo(
  conn: Pick<ProviderConnection, 'providerSpecificData'>,
  pools: ProxyPool[] = [],
  providerProxyGroup?: string
): ProxyBadgeInfo {
  const psd = (conn.providerSpecificData as Record<string, unknown> | undefined) || {}
  const poolId = typeof psd.proxyPoolId === 'string' && psd.proxyPoolId ? psd.proxyPoolId : null

  if (poolId === '__none__' || poolId === 'direct') {
    return {
      hasAnyProxy: false,
      variant: 'success',
      displayText: 'Direct (manual)',
      maskedProxyUrl: 'Direct (No proxy)',
      noProxyText: ''
    }
  }

  if (poolId && poolId.includes(',')) {
    const selectors = badgeSelectors(poolId, pools)
    if (selectors.length > 0) {
      const activeCount = countActiveUnion(pools, selectors)
      const { word, smart, names } = multiLabel(selectors)
      return {
        hasAnyProxy: true,
        variant: activeCount > 0 ? 'success' : 'error',
        displayText: `${word}: ${names}${activeCount > 0 ? ` (${activeCount})` : ' (inactive)'}`,
        maskedProxyUrl: `${smart}: ${names}`,
        noProxyText: ''
      }
    }
  }

  if (poolId) {
    if (poolId.startsWith('group:')) {
      const grp = poolId.slice(6)
      const activeCount = pools.filter((p) => p.group === grp && p.isActive).length
      return {
        hasAnyProxy: true,
        variant: activeCount > 0 ? 'success' : 'error',
        displayText: `Group: ${grp}${activeCount > 0 ? ` (${activeCount})` : ' (inactive)'}`,
        maskedProxyUrl: `Smart Group: ${grp}`,
        noProxyText: ''
      }
    }
    if (poolId.startsWith('type:')) {
      const typ = poolId.slice(5)
      const activeCount = pools.filter((p) => p.type === typ && p.isActive).length
      return {
        hasAnyProxy: true,
        variant: activeCount > 0 ? 'success' : 'error',
        displayText: `Type: ${typ}${activeCount > 0 ? ` (${activeCount})` : ' (inactive)'}`,
        maskedProxyUrl: `Smart Type: ${typ}`,
        noProxyText: ''
      }
    }
    const matchingGroupPools = pools.filter((p) => p.group === poolId)
    if (matchingGroupPools.length > 0) {
      const activeCount = matchingGroupPools.filter((p) => p.isActive).length
      return {
        hasAnyProxy: true,
        variant: activeCount > 0 ? 'success' : 'error',
        displayText: `Group: ${poolId}${activeCount > 0 ? ` (${activeCount})` : ' (inactive)'}`,
        maskedProxyUrl: `Smart Group: ${poolId}`,
        noProxyText: ''
      }
    }
  }

  const pool = poolId ? pools.find((p) => p.id === poolId) || null : null

  const legacyUrl = typeof psd.connectionProxyUrl === 'string' ? psd.connectionProxyUrl : ''
  const hasLegacyProxy = psd.connectionProxyEnabled === true && !!legacyUrl
  const hasAnyProxy = !!poolId || hasLegacyProxy

  // Fallback to provider-level locked proxy group when no custom connection proxy is configured
  if (!hasAnyProxy && providerProxyGroup && providerProxyGroup !== 'none') {
    if (providerProxyGroup.includes(',')) {
      const selectors = badgeSelectors(providerProxyGroup, pools)
      if (selectors.length > 0) {
        const activeCount = countActiveUnion(pools, selectors)
        const { word, names } = multiLabel(selectors)
        return {
          hasAnyProxy: true,
          variant: activeCount > 0 ? 'success' : 'error',
          displayText: `${word}: ${names} (auto)`,
          maskedProxyUrl: `Default ${word}: ${names}`,
          noProxyText: ''
        }
      }
    }
    // Strip REPEATED prefixes: a stored "group:group:X" (a derivation
    // that prefixed an already-prefixed value) must still read as
    // group X — stripping one prefix left "group:X" as the name, which
    // matches no pool and painted every badge red.
    const grp = providerProxyGroup.replace(/^((group|type):)+/, '')
    const activeCount = pools.filter((p) => (p.group === grp || p.type === grp) && p.isActive).length
    return {
      hasAnyProxy: true,
      variant: activeCount > 0 ? 'success' : 'error',
      displayText: `Group: ${grp} (auto)`,
      maskedProxyUrl: `Default Group: ${grp}`,
      noProxyText: ''
    }
  }

  const rawProxyUrl = pool?.proxyUrl || legacyUrl
  let maskedProxyUrl = ''
  if (rawProxyUrl) {
    try {
      const parsed = new URL(rawProxyUrl)
      maskedProxyUrl = `${parsed.protocol}//${parsed.hostname}${parsed.port ? `:${parsed.port}` : ''}`
    } catch {
      maskedProxyUrl = rawProxyUrl
    }
  }

  return {
    hasAnyProxy,
    variant: pool?.isActive === true ? 'success' : 'error',
    displayText: pool
      ? `Pool: ${pool.name}`
      : poolId
        ? `Pool: ${poolId} (inactive/missing)`
        : `Legacy: ${legacyUrl}`,
    maskedProxyUrl,
    noProxyText:
      pool?.noProxy || (typeof psd.connectionNoProxy === 'string' ? psd.connectionNoProxy : '')
  }
}
