import type { ProviderConnection, ProxyPool } from '../../api/client'

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
    const grp = providerProxyGroup.replace(/^(group|type):/, '')
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
