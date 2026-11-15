<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import {
    api,
    normalizeLastError,
    type ConnectionUsageResponse,
    type CreateConnectionPayload,
    type FreebuffSessionStatusResponse,
    type ModelAvailabilityEntry,
    type ProviderConnection,
    type ProviderNode,
    type ProxyPool,
    type Settings
  } from '../../api/client'
  import { PROVIDER_CATALOG, type ProviderCatalogItem } from '../../lib/providers'
  import { PROVIDER_ID_TO_ALIAS } from '../../lib/models'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import { proxyBadgeInfo } from './proxyBadge'
  import {
    formatRemaining,
    groupAvailabilityByConnection,
    healthByConnection as buildHealthByConnection,
    statusLabel,
    type ConnectionHealth
  } from './connectionHealth'
  import {
    badgeFor,
    canProbeRow,
    probeOutcomeFrom,
    probeOutcomeFromError,
    testingStatus,
    type ProbeStatus
  } from './connectionProbe'
  import { getProviderStats } from './types'
  import Menu from '../../lib/ui/Menu.svelte'
  import MenuItem from '../../lib/ui/MenuItem.svelte'
  import EditConnectionModal, { type ConnectionUpdate } from './EditConnectionModal.svelte'
  import AddConnectionModal from './AddConnectionModal.svelte'
  import BulkImportJwtModal from './BulkImportJwtModal.svelte'
  import { supportsJwtBulkImport } from '../../lib/bulk-add'
  import {
    deriveProxyGroups,
    deriveProxyTypes,
    parseProxyTargetParts,
    splitProxyTarget
  } from '../../lib/proxyBindings'
  import ProviderHeaderOverridesModal from './ProviderHeaderOverridesModal.svelte'
  import ExportKeysModal from './ExportKeysModal.svelte'
  import ShareProviderModal from './ShareProviderModal.svelte'
  import ProxyResolutionNote from '../media/ProxyResolutionNote.svelte'

  /**
   * The Connections card of a provider detail page, extracted from
   * ProviderDetailView so the media provider detail page renders the
   * exact same roster UI (toolbar, proxy lock, one-by-one probe, rows,
   * and every modal the card opens) instead of its own reduced card.
   *
   * The host page still reads the roster this card renders — the header
   * status strip, the all-connections toggle and the models section —
   * so providerConnections / paginatedConnections / connectionHealthById
   * / totalConnectionsCount are bindable: the card keeps the internal
   * deriveds as the single source and syncs them out through effects.
   *
   * Strategy and modal state that the host page also writes (its
   * loadData seeds the strategy fields; its compatible-node card opens
   * the Add / Export / Share modals) is bindable for the same reason.
   */
  interface Props {
    providerId: string
    /** Catalog entry for this provider (name, auth modes, alias). */
    provider?: ProviderCatalogItem
    /** The custom node behind this id, when the page is a compatible node. */
    providerNode?: ProviderNode
    connections?: ProviderConnection[]
    providerStats?: Record<string, Record<string, any>>
    /** All custom nodes, for the Share modal's provider list. */
    providerNodes?: ProviderNode[]
    proxyPools?: ProxyPool[]
    settings?: Settings | null
    isRoundRobin?: boolean
    stickyLimit?: string
    lockedProxyGroup?: string
    lockedProxyPoolId?: string
    showAddKeyModal?: boolean
    showExportKeysModal?: boolean
    showShareProviderModal?: boolean
    addConnectionError?: string
    providerConnections?: ProviderConnection[]
    paginatedConnections?: ProviderConnection[]
    connectionHealthById?: Map<string, ConnectionHealth>
    totalConnectionsCount?: number
    freebuffSessions?: Record<string, FreebuffSessionStatusResponse>
    isAuthorizingFreebuff?: boolean
    onRefresh: () => void
    /** Host-provided Add flow (ProviderDetailView routes it into its
     *  OAuth machinery). Without one, Add opens the API-key modal. */
    onAddConnection?: () => void
    onStartFreebuff?: () => void
    onSelectFreebuffAccount?: (connId: string) => void
  }

  let {
    providerId,
    provider,
    providerNode,
    connections = [],
    providerStats = {},
    providerNodes = [],
    proxyPools = [],
    settings = $bindable(null),
    isRoundRobin = $bindable(false),
    stickyLimit = $bindable('1'),
    lockedProxyGroup = $bindable(''),
    lockedProxyPoolId = $bindable(''),
    showAddKeyModal = $bindable(false),
    showExportKeysModal = $bindable(false),
    showShareProviderModal = $bindable(false),
    addConnectionError = $bindable(''),
    providerConnections: providerConnectionsOut = $bindable([]),
    paginatedConnections: paginatedConnectionsOut = $bindable([]),
    connectionHealthById: connectionHealthByIdOut = $bindable(new Map()),
    totalConnectionsCount: totalConnectionsCountOut = $bindable(0),
    freebuffSessions = {},
    isAuthorizingFreebuff = false,
    onRefresh,
    onAddConnection,
    onStartFreebuff,
    onSelectFreebuffAccount
  }: Props = $props()

  // Catalog & node resolution, mirroring the host page's derivations
  // from the same inputs so the card renders identically wherever it
  // mounts.
  let providerName = $derived(providerNode?.name || provider?.name || providerId)
  let storageAlias = $derived(
    providerNode?.id || provider?.alias || PROVIDER_ID_TO_ALIAS[providerId] || providerId
  )
  // Cached stats from providerStats (O(1) lookup)
  let cachedStats = $derived(
    getProviderStats([], providerId, undefined, providerStats)
  )
  let isOAuth = $derived(provider?.category === 'oauth')
  // Upstream parity: providers with authModes ["apikey","oauth"] offer both an
  // OAuth login and a manual API Key button (data-driven, category-independent
  // so freeTier kimchi is covered too — mirrors upstream hasDualAuthModes).
  let authModes = $derived(provider?.authModes || [])
  let hasDualAuthModes = $derived(
    !providerNode && authModes.includes('oauth') && authModes.includes('apikey')
  )
  // Upstream per-provider button labels ([id]/page.js).
  let oauthButtonLabel = $derived(
    providerId === 'xai' ? 'Grok Build OAuth' : providerId === 'kimi' ? 'Kimi Coding OAuth' : 'OAuth'
  )
  let apiKeyButtonLabel = $derived(
    providerId === 'xai'
      ? 'xAI API Key'
      : providerId === 'kimi'
        ? 'Kimi API Key'
        : providerId === 'qoder'
          ? 'PAT'
          : 'API Key'
  )
  let isFreebuff = $derived(
    providerId === 'freebuff' || storageAlias === 'fb' || storageAlias === 'freebuff'
  )
  let isCompatibleNode = $derived(
    !!providerNode &&
      (providerNode.type === 'openai-compatible' || providerNode.type === 'anthropic-compatible')
  )

  // The card's Add buttons dispatch through the host page when it
  // provides a flow (ProviderDetailView routes them into its OAuth
  // machinery, including the risk-notice gate); without one they open
  // the API-key modal directly.
  function handleAddConnectionClick() {
    if (onAddConnection) {
      onAddConnection()
    } else {
      openAddKeyModal()
    }
  }

  function startFreebuffFlow() {
    onStartFreebuff?.()
  }

  function selectFreebuffAccount(connId: string) {
    onSelectFreebuffAccount?.(connId)
  }

  // Quota & Cooldown tracking state
  let connectionQuotas = $state<Record<string, ConnectionUsageResponse>>({})
  let currentTime = $state(Date.now())

  // Account health: the locks census (GET /api/models/availability) joined
  // with each connection's server-computed effectiveStatus. The census is
  // refetched whenever the parent hands this view a fresh connections list
  // — the same cycle the connections themselves refresh on (provider open
  // and every post-mutation reload) — and the 1s currentTime tick keeps
  // lock remaining times live between fetches. No separate polling.
  let availabilityEntries = $state<ModelAvailabilityEntry[]>([])

  async function loadAvailability() {
    try {
      const res = await api.getModelAvailability()
      availabilityEntries = res.models
    } catch {
      availabilityEntries = []
    }
  }

  $effect(() => {
    void connections
    untrack(() => {
      void loadAvailability()
    })
  })

  let availabilityByConnection = $derived(groupAvailabilityByConnection(availabilityEntries))
  let connectionHealthById = $derived.by((): Map<string, ConnectionHealth> => {
    // Reading currentTime re-derives health as locks lapse, so a lock that
    // expires while the page is open drops out of the counts and badges.
    void currentTime
    return buildHealthByConnection(paginatedConnections, availabilityByConnection, currentTime)
  })
  // The paged slice is declared before the derivations that read it:
  // $derived bodies evaluate lazily, so the old order ran, but it read
  // as a use-before-declaration and the checker rightly flagged it.
  let pagedConnections = $state<ProviderConnection[]>([])
  let isLoadingConnections = $state<boolean>(false)

  // Filtered connections for this provider, sorted by priority ASC
  let providerConnections = $derived(
    connections.length > 0
      ? connections
          .filter((c) => c.provider === providerId)
          .sort((a, b) => (a.priority ?? 999999) - (b.priority ?? 999999))
      : pagedConnections
  )

  // Connections pagination
  let connPageSize = $state<number>(10)
  let connPage = $state<number>(1)

  // Account search, mirroring the Quota Tracker's: submit on Enter or
  // the search button (not per keystroke), a clear button while a
  // search is applied, and the server filters the provider's whole
  // list so a match on any page is findable.
  let connSearchQuery = $state('')
  let appliedConnSearch = $state('')
  let pagedTotal = $state<number | null>(null)

  // While an account search is applied, the count is the filtered
  // total the server reports for the search (X-Total-Count), not the
  // provider's whole roster.
  //
  // Declared after the state it reads: a $derived that names a binding
  // declared further down hits the temporal dead zone when it is
  // evaluated, which is a runtime ReferenceError rather than a type
  // complaint. `?? 0` keeps the count a plain number, so the page
  // arithmetic below does not have to narrow it at every use.
  let totalConnectionsCount = $derived(
    appliedConnSearch && pagedTotal !== null
      ? pagedTotal
      : (cachedStats.total ?? 0) > 0
        ? (cachedStats.total ?? 0)
        : pagedConnections.length > 0
          ? pagedConnections.length
          : providerConnections.length
  )

  function handleConnSearch(): void {
    appliedConnSearch = connSearchQuery.trim()
    connPage = 1
  }

  function handleClearConnSearch(): void {
    connSearchQuery = ''
    if (appliedConnSearch) {
      appliedConnSearch = ''
      connPage = 1
    }
  }

  let connTotalPages = $derived(
    Math.max(1, Math.ceil(totalConnectionsCount / connPageSize))
  )

  async function loadPagedConnections(pId: string, page: number, size: number) {
    if (!pId) return
    isLoadingConnections = true
    try {
      const data = await api.getConnectionsPage(pId, page, size, appliedConnSearch || undefined)
      pagedConnections = data.connections
      pagedTotal = data.total
    } catch (err) {
      console.error('Failed to load paged connections', err)
      pagedConnections = []
      pagedTotal = null
    } finally {
      isLoadingConnections = false
    }
  }

  $effect(() => {
    void providerId
    connPage = 1
    connSearchQuery = ''
    appliedConnSearch = ''
  })

  $effect(() => {
    void appliedConnSearch
    if (providerId) {
      loadPagedConnections(providerId, connPage, connPageSize)
    }
  })

  let paginatedConnections = $derived(
    pagedConnections.length > 0 || appliedConnSearch
      ? pagedConnections
      : providerConnections.slice((connPage - 1) * connPageSize, connPage * connPageSize)
  )

  $effect(() => {
    if (connPage > connTotalPages) {
      connPage = connTotalPages
    }
  })

  function loadPageUsage(conns: ProviderConnection[]) {
    for (const c of conns) {
      if (!connectionQuotas[c.id]) {
        api.getConnectionUsage(c.id).then((usage) => {
          if (usage && !usage.error) {
            connectionQuotas[c.id] = usage
          }
        }).catch(() => {})
      }
    }
  }

  // Quotas are NOT auto-fetched when the page opens (operator report:
  // opening a provider with many connections burst-fired one live usage
  // read per connection and made the page crawl). The operator loads
  // them explicitly with the button above the list; /api/usage responses
  // are also cached server-side for 55s, so repeat loads stay cheap.
  let pageQuotasPending = $derived(
    paginatedConnections.filter((c) => !connectionQuotas[c.id]).length,
  )
  let proxyLockOpen = $state(false)
  let proxyLockPos = $state({ top: 0, right: 0 })
  // The lock's display label and group selection, derived from the
  // stored selector: a single group/type keeps its "Group: X" /
  // "Type: X" label; several groups read "Groups: A + B".
  let lockedProxyParts = $derived(parseProxyTargetParts(lockedProxyGroup))
  let lockedGroupNames = $derived(
    splitProxyTarget(lockedProxyGroup)
      .filter((part) => part.startsWith('group:'))
      .map((part) => part.slice(6))
  )
  let lockedProxyLabel = $derived.by(() => {
    if (lockedProxyPoolId) {
      const name = proxyPools.find((p) => p.id === lockedProxyPoolId)?.name
      return name ? `Pool ${name}` : 'Pool (missing)'
    }
    if (!lockedProxyGroup) return 'None (direct)'
    const names = lockedProxyParts.map((p) => p.name).join(' + ')
    if (lockedProxyParts.length > 1) {
      if (lockedProxyParts.every((p) => p.kind === 'group')) return `Groups ${names}`
      if (lockedProxyParts.every((p) => p.kind === 'type')) return `Types ${names}`
      return `Multi ${names}`
    }
    return lockedProxyParts[0]?.kind === 'type' ? `Type ${names}` : `Group ${names}`
  })
  let activeProxyPools = $derived(proxyPools.filter((p) => p.isActive))

  let availableProxyGroups = $derived(deriveProxyGroups(proxyPools))

  let availableProxyTypes = $derived(deriveProxyTypes(proxyPools))

  // Selection state
  let selectedConnIds = $state<string[]>([])
  let isAllSelected = $derived(
    providerConnections.length > 0 && selectedConnIds.length === providerConnections.length
  )

  // One-by-One Health Check state
  // ONE_BY_ONE_DELAY_MS mirrors upstream: a pause between connections so a
  // provider is not hit with back-to-back probes.
  const ONE_BY_ONE_DELAY_MS = 1000
  let isTestingOneByOne = $state(false)
  let isStoppingOneByOne = $state(false)
  let isStopTesting = $state(false)
  let oneByOneCurrentId = $state<string | null>(null)
  let oneByOneSummary = $state<
    { total: number; completed: number; passed: number; failed: number; stopped: boolean } | null
  >(null)
  // Written by both the sweep and the per-row refresh (PR #196); the shared
  // ProbeStatus keeps those two writers from drifting apart.
  let oneByOneStatuses = $state<Record<string, ProbeStatus>>({})

  // Credits (upstream ConnectionRow isCreditAware): CodeBuddy CN/Intl and
  // WorkBuddy expose a Tencent billing endpoint with a per-package
  // remaining-credit balance, surfaced on demand per connection so the
  // operator can see account-level headroom before deciding rotation order.
  interface CreditInfo {
    remaining: number
    total: number
    resetAtMs: number | null
    plan?: string
    message?: string
  }
  let creditInfoByConn = $state<Record<string, CreditInfo>>({})
  let creditLoadingByConn = $state<Record<string, boolean>>({})
  const CREDIT_AWARE_PROVIDERS = new Set(['codebuddy-cn', 'codebuddy-intl', 'workbuddy'])
  function isCreditAwareConn(conn: ProviderConnection): boolean {
    return CREDIT_AWARE_PROVIDERS.has(conn.provider)
  }
  async function fetchConnectionCredits(conn: ProviderConnection) {
    if (creditLoadingByConn[conn.id]) return
    creditLoadingByConn = { ...creditLoadingByConn, [conn.id]: true }
    try {
      const data = (await api.getConnectionUsage(conn.id, true)) as ConnectionUsageResponse & {
        message?: string
      }
      if (data?.quotas && typeof data.quotas === 'object' && !Array.isArray(data.quotas)) {
        let remaining = 0
        let total = 0
        let resetAtMs: number | null = null
        for (const q of Object.values(data.quotas)) {
          if (q.unlimited) continue
          const t = Number(q.total)
          const u = Number(q.used)
          if (Number.isFinite(t) && Number.isFinite(u)) {
            remaining += Math.max(0, t - u)
            total += t
          }
          const r = q.resetAt ? new Date(q.resetAt).getTime() : NaN
          if (Number.isFinite(r) && (resetAtMs === null || r < resetAtMs)) resetAtMs = r
        }
        creditInfoByConn = {
          ...creditInfoByConn,
          [conn.id]: { remaining, total, resetAtMs, plan: data.plan },
        }
      } else if (data?.message) {
        creditInfoByConn = { ...creditInfoByConn, [conn.id]: { remaining: 0, total: 0, resetAtMs: null, message: data.message } }
      } else {
        creditInfoByConn = {
          ...creditInfoByConn,
          [conn.id]: { remaining: 0, total: 0, resetAtMs: null, message: data?.error || 'No quota data' },
        }
      }
    } catch (e) {
      creditInfoByConn = {
        ...creditInfoByConn,
        [conn.id]: {
          remaining: 0,
          total: 0,
          resetAtMs: null,
          message: e instanceof Error ? e.message : 'Failed to load quota',
        },
      }
    } finally {
      creditLoadingByConn = { ...creditLoadingByConn, [conn.id]: false }
    }
  }
  // Row UI state
  let activeProxyDropdownId = $state<string | null>(null)
  let proxySearch = $state('')
  let dropdownPos = $state<{ top: number; right: number }>({ top: 0, right: 0 })
  let updatingProxyConnId = $state<string | null>(null)
  // Fresh verification URLs minted by a row's Verifikasi test, keyed by
  // connection id (fragment 1061): shown in the row's error panel until
  // the next test replaces them or the account releases.
  let freshVerifyUrls = $state<Record<string, string>>({})

  // "Or the account releases" includes releases this page did not start:
  // when refreshed row data stops reading "validation" for a connection
  // (e.g. the usage auto-check enabled it server-side), the URL an
  // earlier test minted for it is stale and the panel must drop it on
  // the refresh itself, not wait for another Verifikasi press. Entries
  // for connections outside the current page slice are left alone —
  // absence from the slice is not a verdict.
  $effect(() => {
    const health = connectionHealthById
    untrack(() => {
      const stale = Object.keys(freshVerifyUrls).filter((id) => {
        const h = health.get(id)
        return h !== undefined && h.state !== 'validation'
      })
      if (stale.length === 0) return
      const next = { ...freshVerifyUrls }
      for (const id of stale) delete next[id]
      freshVerifyUrls = next
    })
  })
  let showApplyProxyModal = $state(false)
  let isApplyingProxy = $state(false)
  let isResettingCooldown = $state(false)
  // Non-null while a reorder request is in flight; the chevrons disable on it
  // so a second click cannot fire a swap computed from a stale list.
  let reorderingConnId = $state<string | null>(null)
  // Bulk-apply progress + abort flag for the Apply Proxy modal.
  let applyProxyDone = $state(0)
  let applyProxyAbort = $state(false)
  let proxyPoolSearch = $state('')

  let filteredProxyPools = $derived.by(() => {
    const q = proxyPoolSearch.trim().toLowerCase()
    if (!q) return proxyPools
    return proxyPools.filter(
      (p) => p.name.toLowerCase().includes(q) || (p.proxyUrl ?? '').toLowerCase().includes(q)
    )
  })

  // The connection whose Edit Connection modal is open; null when closed. The
  // modal owns its own fields and payload (issue #158).
  let editingConnection = $state<ProviderConnection | null>(null)
  let showBulkJwtModal = $state(false)
  // CodeBuddy-family providers take a Bulk Add (JWT) entry in the
  // connections toolbar (upstream ConnectionsSection gating).
  let isJwtBulkProvider = $derived(supportsJwtBulkImport(providerId))
  // Everything the Share modal can offer in a multi-provider set: the custom
  // nodes (which carry their own endpoint) plus the registered providers this
  // instance actually holds a connection for. A provider with no credential is
  // not shareable — the receiver would get a row that cannot answer.
  let shareableProviders = $derived.by(() => {
    const withConnections = new Set(providerConnections.filter((c) => c.isActive !== 0).map((c) => c.provider))
    const out: Array<{ id: string; name: string; isCompatible: boolean }> = []
    for (const n of providerNodes) out.push({ id: n.id, name: n.name || n.id, isCompatible: true })
    for (const item of PROVIDER_CATALOG) {
      if (withConnections.has(item.id) || withConnections.has(item.alias || '')) {
        out.push({ id: item.id, name: item.name || item.id, isCompatible: false })
      }
    }
    return out
  })
  let showHeaderOverrides = $state(false)
  // DeepSeek's credential is renewed by a browser, not by the client: the
  // site's userToken is only issued by a real sign-in, and the WAF's cookie
  // is only renewed by the browser that solves its challenge. So the renewal
  // opens a browser and waits for a human.
  let isDeepSeekWeb = $derived(providerId === 'deepseek-web')
  let renewingConnId = $state<string | null>(null)
  function getCooldownInfo(conn: ProviderConnection): { label: string; title: string; isExhausted: boolean; isLock?: boolean } | null {
    // 1. Check modelLock_* and rateLimitedUntil across conn, conn.data, and providerSpecificData (matching upstream ⏱ {timeLeft})
    const dataObj = conn.providerSpecificData as Record<string, unknown> | undefined
    const rawData = (conn as unknown as { data?: Record<string, unknown> }).data
    const allProps = {
      ...(typeof rawData === 'object' && rawData ? rawData : {}),
      ...(typeof dataObj === 'object' && dataObj ? dataObj : {}),
      ...conn
    }
    const locks = Object.entries(allProps).filter(
      ([k, v]) => (k.startsWith('modelLock_') || k === 'rateLimitedUntil') && v && new Date(v as string).getTime() > currentTime
    )
    if (locks.length > 0) {
      let maxLock = 0
      for (const [_, v] of locks) {
        const t = new Date(v as string).getTime()
        if (t > maxLock) maxLock = t
      }
      const diff = Math.max(0, Math.floor((maxLock - currentTime) / 1000))
      const timeLeft =
        diff < 60
          ? `${diff}s`
          : diff < 3600
            ? `${Math.floor(diff / 60)}m ${diff % 60}s`
            : `${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`
      return {
        label: `⏱ ${timeLeft}`,
        title: `Model rate limit lock active until ${new Date(maxLock).toLocaleTimeString()}`,
        isExhausted: false,
        isLock: true
      }
    }

    // 2. Check live quota data for exhausted models (remaining <= 0) and resetAt.
    //    A pool with total <= 0 is not an exhausted pool — it means the account
    //    has no allocation at all (Qoder credits report total 0, and its
    //    "expiresAt" is a 9999-12-31 sentinel), so counting it produced
    //    "Quota Exhausted (0%): Resets in 2912172d 1h" on a perfectly healthy
    //    connection. Only a pool that was actually granted something can run
    //    out of it.
    const usage = connectionQuotas[conn.id]
    if (usage?.quotas) {
      const quotaEntries = Object.entries(usage.quotas)
      const exhausted = quotaEntries.filter(([_, q]) => {
        const total = Number(q.total)
        if (!Number.isFinite(total) || total <= 0) return false
        return (
          (q.remainingPercentage !== undefined && Number(q.remainingPercentage) <= 0) ||
          (q.remaining !== undefined && Number(q.remaining) <= 0)
        )
      })
      if (exhausted.length > 0) {
        let earliestReset = 0
        let resetModel = ''
        for (const [m, q] of exhausted) {
          if (q.resetAt) {
            const t = new Date(q.resetAt).getTime()
            if (t > currentTime && (earliestReset === 0 || t < earliestReset)) {
              earliestReset = t
              resetModel = q.displayName || m
            }
          }
        }
        if (earliestReset > currentTime) {
          const diff = earliestReset - currentTime
          const hours = Math.floor(diff / (1000 * 60 * 60))
          const mins = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60))
          const secs = Math.floor((diff % (1000 * 60)) / 1000)
          const timeStr =
            hours >= 24
              ? `${Math.floor(hours / 24)}d ${hours % 24}h`
              : hours > 0
                ? `${hours}h ${mins}m`
                : `${mins}m ${secs}s`
          return {
            label: `Quota Exhausted (0%): Resets in ${timeStr}`,
            title: `Quota exhausted for ${resetModel}. Reset at ${new Date(earliestReset).toLocaleString()}`,
            isExhausted: true
          }
        }
      }
    }
    // 4. Check errorCode 429 or lastError indicating rate limit / quota
    const errCode = (conn as unknown as { errorCode?: number }).errorCode
    const lastErr = normalizeLastError(conn.lastError) || ''
    if (
      errCode === 429 ||
      lastErr.includes('429') ||
      lastErr.toLowerCase().includes('quota') ||
      lastErr.toLowerCase().includes('exhausted')
    ) {
      const match = lastErr.match(/Resets in ([^.]+)/i)
      if (match) {
        return {
          label: `Quota Exhausted (429): Resets in ${match[1].trim()}`,
          title: lastErr,
          isExhausted: true
        }
      }
      return {
        label: 'Quota Exhausted (429)',
        title: lastErr || 'HTTP 429 Rate Limit / Quota Exhausted',
        isExhausted: true
      }
    }

    return null
  }
  async function saveProviderStrategy(fallback: 'round-robin' | null, sticky: string) {
    try {
      const current = settings?.providerStrategies || {}
      const updated = { ...current }
      if (fallback === 'round-robin') {
        // Upstream saves `Number(sticky) || 3`; the entry is merged (not
        // replaced) so the proxy/rotate keys saved by the free-provider card
        // survive a round-robin toggle.
        updated[providerId] = {
          ...(updated[providerId] || {}),
          fallbackStrategy: 'round-robin',
          stickyRoundRobinLimit: Number(sticky) || 3
        }
      } else {
        delete updated[providerId]
      }
      if (settings) settings.providerStrategies = updated
      await api.updateSettings({ providerStrategies: updated })
    } catch (err) {
      console.error('Error saving provider strategy:', err)
    }
  }

  async function toggleRoundRobin() {
    isRoundRobin = !isRoundRobin
    if (isRoundRobin && !stickyLimit) {
      stickyLimit = '1'
    }
    await saveProviderStrategy(isRoundRobin ? 'round-robin' : null, stickyLimit)
  }

  async function handleStickyLimitChange() {
    if (isRoundRobin) {
      await saveProviderStrategy('round-robin', stickyLimit)
    }
  }
  // Selection handlers
  function toggleSelectAll() {
    if (isAllSelected) {
      selectedConnIds = []
    } else {
      selectedConnIds = providerConnections.map((c) => c.id)
    }
  }

  function toggleSelect(id: string) {
    if (selectedConnIds.includes(id)) {
      selectedConnIds = selectedConnIds.filter((x) => x !== id)
    } else {
      selectedConnIds = [...selectedConnIds, id]
    }
  }

  async function handleDeleteSelected() {
    if (selectedConnIds.length === 0) return
    if (!confirm(`Delete ${selectedConnIds.length} selected connection(s)? This cannot be undone.`)) return
    let failed = 0
    for (const id of selectedConnIds) {
      try {
        await api.deleteConnection(id)
      } catch (e) {
        console.error('Error deleting connection:', e)
        failed++
      }
    }
    selectedConnIds = []
    onRefresh()
    if (failed > 0) {
      alert(`Deleted with ${failed} failed request(s).`)
    }
  }

  // One-by-One Health Test (upstream handleRunOneByOneTest): sequential probes
  // with a pause between them, a live summary, and a cooperative stop.
  async function runOneByOneTest() {
    if (isTestingOneByOne || providerConnections.length === 0) return

    const queued: Record<string, { state: 'queued' | 'testing' | 'success' | 'failed'; error?: string | null }> = {}
    for (const c of providerConnections) {
      queued[c.id] = { state: 'queued', error: null }
    }

    isStopTesting = false
    isStoppingOneByOne = false
    oneByOneCurrentId = null
    oneByOneStatuses = queued
    oneByOneSummary = {
      total: providerConnections.length,
      completed: 0,
      passed: 0,
      failed: 0,
      stopped: false
    }
    isTestingOneByOne = true

    let passed = 0
    let failed = 0

    try {
      for (let index = 0; index < providerConnections.length; index += 1) {
        if (isStopTesting) {
          oneByOneSummary = {
            total: providerConnections.length,
            completed: index,
            passed,
            failed,
            stopped: true
          }
          break
        }

        const conn = providerConnections[index]
        oneByOneCurrentId = conn.id
        oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: testingStatus() }

        try {
          const res = await api.testConnection(conn.id)
          if (res?.valid) {
            passed += 1
          } else {
            failed += 1
          }
          oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFrom(res) }
        } catch (err) {
          failed += 1
          oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFromError(err) }
        }

        oneByOneSummary = {
          total: providerConnections.length,
          completed: index + 1,
          passed,
          failed,
          stopped: false
        }

        if (index < providerConnections.length - 1) {
          await new Promise((r) => setTimeout(r, ONE_BY_ONE_DELAY_MS))
        }
      }
    } finally {
      oneByOneCurrentId = null
      isTestingOneByOne = false
      isStoppingOneByOne = false
      isStopTesting = false
      onRefresh()
    }
  }

  // Verifikasi: test-call the chat API for one validation-flagged
  // account. If the gate is gone, the backend enables the account in
  // the same call; if it is still gated, the response carries the
  // verification URL that test just minted — kept here (never stored
  // server-side) and shown in the row's error panel for an immediate
  // click. Either way the refreshed row state carries the rest of the
  // verdict.
  let recheckingId = $state<string | null>(null)

  async function recheckVerification(conn: ProviderConnection) {
    recheckingId = conn.id
    try {
      const res = await api.testConnection(conn.id)
      if (res?.verificationUrl) {
        freshVerifyUrls = { ...freshVerifyUrls, [conn.id]: res.verificationUrl }
      } else if (res?.valid) {
        const next = { ...freshVerifyUrls }
        delete next[conn.id]
        freshVerifyUrls = next
      }
    } catch {
      // The refreshed row state carries the outcome.
    } finally {
      recheckingId = null
      onRefresh()
    }
  }
  function stopOneByOneTest() {
    if (!isTestingOneByOne) return
    isStopTesting = true
    isStoppingOneByOne = true
  }

  // Per-row refresh: probe one account straight from its row instead of
  // opening the edit modal. Reuses the one-by-one badge state so the row
  // shows the live testing/success/failed indicator for free.
  async function refreshOneConnection(conn: ProviderConnection) {
    if (!canProbeRow(isTestingOneByOne, oneByOneStatuses[conn.id])) return
    oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: testingStatus() }
    try {
      const res = await api.testConnection(conn.id)
      oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFrom(res) }
    } catch (err) {
      oneByOneStatuses = { ...oneByOneStatuses, [conn.id]: probeOutcomeFromError(err) }
    } finally {
      onRefresh()
    }
  }

  /**
   * Renew one DeepSeek Web connection's credential.
   *
   * A browser opens and waits for the sign-in, so the operator has to be
   * there for it. The credential that comes back is written to the row, so
   * there is nothing to paste.
   */
  async function renewDeepSeekCredential(conn: ProviderConnection) {
    if (renewingConnId) return
    renewingConnId = conn.id
    try {
      await api.renewDeepSeekCredential(conn.id)
      alert('The credential was renewed.')
      onRefresh()
    } catch (err) {
      alert(`The renewal failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      renewingConnId = null
    }
  }

  // Priority reordering.
  // Single server-side transactional call instead of two independent PUTs:
  // a partial failure between those two writes left two rows sharing a
  // priority, and a stable sort over tied priorities made every later click a
  // literal no-op — the pair became permanently un-reorderable through the UI.
  // The in-flight flag also stops a second click from firing a swap computed
  // from the same stale list (last-write-wins, so the list appeared frozen).
  async function swapPriority(idx: number, delta: -1 | 1) {
    const conn = providerConnections[idx]
    if (!conn || reorderingConnId) return
    const target = providerConnections[idx + delta]
    if (!target) return

    reorderingConnId = conn.id
    try {
      await api.reorderConnection(conn.id, delta < 0 ? 'up' : 'down')
      onRefresh()
    } catch (err) {
      alert(`Reorder failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      reorderingConnId = null
    }
  }

  // Single connection toggling & deleting
  async function toggleConnectionActive(conn: ProviderConnection) {
    try {
      await api.updateConnection(conn.id, { isActive: conn.isActive === 1 ? 0 : 1 })
      onRefresh()
    } catch (err) {
      alert(`Toggle failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleDeleteConnection(conn: ProviderConnection) {
    if (!confirm(`Delete connection "${formatEmailLabel(conn.name || conn.id, $emailPrivacy)}"? This cannot be undone.`)) return
    try {
      await api.deleteConnection(conn.id)
      onRefresh()
    } catch (err) {
      alert(`Delete failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleResetConnectionStatus(conn: ProviderConnection) {
    try {
      const res = await api.resetConnectionStatus(conn.id)
      if (res.ok) {
        onRefresh()
      } else {
        alert('Failed to reset connection status')
      }
    } catch (err) {
      alert(`Reset failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleBulkResetStatus() {
    const isSelected = selectedConnIds.length > 0
    isResettingCooldown = true
    try {
      const res = await api.bulkResetStatus({
        provider: providerId,
        ids: isSelected ? selectedConnIds : undefined,
        action: isSelected ? undefined : 'cooldown',
      })
      if (res.ok) {
        onRefresh()
      } else {
        alert('Failed to bulk reset status')
      }
    } catch (err) {
      alert(`Bulk reset failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isResettingCooldown = false
    }
  }

  // Per-row proxy badge (upstream ConnectionRow) — see proxyBadge.ts.
  function proxyBadgeFor(conn: ProviderConnection) {
    return proxyBadgeInfo(conn, proxyPools, lockedProxyGroup, lockedProxyPoolId)
  }

  async function applyProxyGroupLock(val: string) {
    lockedProxyGroup = val
    // A lock write owns the strategy's proxyPoolId, so any pool binding
    // it replaces is gone from the UI state too.
    lockedProxyPoolId = ''
    try {
      const existing = settings?.providerStrategies || {}
      const currentStrat = { ...(existing[providerId] || {}) }
      // The lock owns BOTH strategy proxy fields, in the canonical
      // selector forms the resolver honours. Writing proxyGroup alone
      // (the old behaviour) broke twice: a bare TYPE name stored there
      // was re-read by the resolver as a group selector, so a type lock
      // never resolved; and any stored proxyPoolId — which the resolver
      // prefers — silently kept control while the UI showed the lock
      // changed or removed.
      if (!val) {
        delete currentStrat.proxyPoolId
        delete currentStrat.proxyGroup
      } else if (val.startsWith('type:') && !val.includes(',')) {
        currentStrat.proxyPoolId = val
        delete currentStrat.proxyGroup
      } else {
        // A group selector, single ("group:A") or multi
        // ("group:A,group:B"): proxyPoolId carries the selector and
        // proxyGroup mirrors the bare name(s), comma-joined.
        currentStrat.proxyPoolId = val
        currentStrat.proxyGroup = val
          .split(',')
          .map((part) => part.replace(/^group:/, ''))
          .join(',')
      }
      const updated = { ...existing, [providerId]: currentStrat }
      if (settings) settings.providerStrategies = updated
      await api.updateSettings({ providerStrategies: updated })
    } catch (err) {
      console.error('Failed to lock proxy group:', err)
    }
  }

  // Provider-level binding to ONE pool: the strategy's proxyPoolId holds
  // the pool id directly (the resolver returns it verbatim and the
  // serving path resolves that single pool), with no proxyGroup mirror —
  // a mirror would re-read as a group selector. One settings write binds
  // the whole provider; accounts keep only their own custom marks.
  async function applyProviderProxyPool(poolId: string) {
    lockedProxyGroup = ''
    lockedProxyPoolId = poolId
    try {
      const existing = settings?.providerStrategies || {}
      const currentStrat = { ...(existing[providerId] || {}) }
      currentStrat.proxyPoolId = poolId
      delete currentStrat.proxyGroup
      const updated = { ...existing, [providerId]: currentStrat }
      if (settings) settings.providerStrategies = updated
      await api.updateSettings({ providerStrategies: updated })
    } catch (err) {
      console.error('Failed to lock proxy pool:', err)
    }
  }

  // Multi-group lock: toggling a group rewrites the selector from the
  // selected names (in picker order, so the stored value is stable) and
  // keeps the popover open for the next toggle. A type choice stays
  // single — picking one replaces any group selection, and vice versa.
  function toggleLockGroup(name: string) {
    const selected = new Set(lockedGroupNames)
    if (selected.has(name)) selected.delete(name)
    else selected.add(name)
    const names = availableProxyGroups.map((g) => g.name).filter((n) => selected.has(n))
    applyProxyGroupLock(names.map((n) => `group:${n}`).join(','))
  }

  function toggleProxyLockMenu(e: MouseEvent) {
    if (proxyLockOpen) {
      proxyLockOpen = false
      return
    }
    const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
    proxyLockPos = {
      top: rect.bottom + 4,
      right: Math.max(8, window.innerWidth - rect.right)
    }
    proxyLockOpen = true
  }

  // Proxy assignment
  // The payload carries the binding in both places: upstream stores it under
  // providerSpecificData.proxyPoolId, while this backend's proxy resolver reads
  // the top-level field.
  function proxyAssignmentPayload(poolId: string | null) {
    return {
      proxyPoolId: poolId,
      providerSpecificData: { proxyPoolId: poolId }
    }
  }

  async function assignProxyPool(conn: ProviderConnection, poolId: string | null) {
    activeProxyDropdownId = null
    updatingProxyConnId = conn.id
    try {
      await api.updateConnection(conn.id, proxyAssignmentPayload(poolId))
      onRefresh()
    } catch (err) {
      alert(`Failed to update proxy: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      updatingProxyConnId = null
    }
  }

  // Only the one-to-one rotation below still stamps per connection:
  // distributing accounts across pools IS a per-account marking. Uniform
  // group / type / pool choices go through the provider strategy instead
  // (handleApplyProxyPool), as a single settings write.
  //
  // Cancel sets the abort flag and the loop stops on the next iteration, so
  // closing the modal no longer leaves requests firing in the background. The
  // running counter is shown in the modal: with 100+ connections a sequential
  // loop is slow enough that the previous label-less "Applying..." read as a
  // hang.
  async function applyProxyAssignments(assignments: Array<{ id: string; poolId: string | null }>) {
    isApplyingProxy = true
    applyProxyAbort = false
    applyProxyDone = 0
    let failed = 0
    try {
      for (const assignment of assignments) {
        if (applyProxyAbort) break
        try {
          await api.updateConnection(assignment.id, proxyAssignmentPayload(assignment.poolId))
        } catch {
          failed++
        }
        applyProxyDone++
      }
      onRefresh()
      showApplyProxyModal = false
      if (applyProxyAbort) {
        alert(`Cancelled after ${applyProxyDone} of ${assignments.length} connection(s).`)
      } else if (failed > 0) {
        alert(`Updated with ${failed} failed request(s).`)
      }
    } finally {
      isApplyingProxy = false
      applyProxyAbort = false
    }
  }

  function cancelApplyProxy() {
    if (isApplyingProxy) {
      applyProxyAbort = true
      return
    }
    showApplyProxyModal = false
  }

  // A uniform choice for the whole provider is ONE provider-level
  // binding (the strategy), never a stamp on every connection: accounts
  // inherit it dynamically per request, and the only per-account marks
  // that remain are the custom ones set on a single row. The upstream
  // port this replaces looped one update per connection — at 31k
  // connections that ground for minutes and froze every account to that
  // day's choice, immune to later provider-level changes.
  async function handleApplyProxyPool(poolId: string | null) {
    showApplyProxyModal = false
    if (poolId === null) {
      await applyProxyGroupLock('')
    } else if (poolId.startsWith('group:') || poolId.startsWith('type:')) {
      await applyProxyGroupLock(poolId)
    } else {
      await applyProviderProxyPool(poolId)
    }
    onRefresh()
  }

  async function handleApplyProxyRotate() {
    if (activeProxyPools.length === 0) {
      alert('No active proxy pools available.')
      return
    }
    // One-to-one (rotate): connection i gets active pool i, wrapping around.
    await applyProxyAssignments(
      providerConnections.map((c, i) => ({
        id: c.id,
        poolId: activeProxyPools[i % activeProxyPools.length].id
      }))
    )
  }
  // Edit connection modal. Every field, the probe and the payload live in
  // EditConnectionModal; this only decides when it opens and who refreshes.
  function openEditConnection(conn: ProviderConnection) {
    editingConnection = conn
  }

  async function saveEditingConnection(payload: ConnectionUpdate) {
    if (!editingConnection) return
    await api.updateConnection(editingConnection.id, payload)
    editingConnection = null
    onRefresh()
  }
  async function handleAddKeyConnection(payload: CreateConnectionPayload) {
    addConnectionError = ''
    try {
      await api.createConnection({
        ...payload,
        provider: payload.provider || providerId,
        name: payload.name || `${providerName} Key`
      })
      showAddKeyModal = false
      onRefresh()
    } catch (err) {
      // Upstream keeps the modal open and shows the error inline.
      addConnectionError = err instanceof Error ? err.message : String(err)
    }
  }

  function openAddKeyModal() {
    addConnectionError = ''
    showAddKeyModal = true
  }
  // Port of upstream ConnectionRow's secondaryDisplayName: the email when the
  // stored name differs from it, otherwise the displayName when that differs.
  // The Qoder device flow stores name === email, so the displayName ("Luqmanul
  // Hakim") is what upstream shows underneath — the same reason our card used to
  // show the email twice over.
  function secondaryConnLabel(c: {
    name?: string | null
    email?: string | null
    displayName?: string | null
  }): string {
    const name = c.name?.trim() ?? ''
    const email = c.email?.trim() ?? ''
    const display = c.displayName?.trim() ?? ''
    if (name && email && name !== email) return email
    if (name && display && name !== display) return display
    return ''
  }

  onMount(() => {
    const timer = setInterval(() => {
      currentTime = Date.now()
    }, 1000)
    return () => clearInterval(timer)
  })

  // Sync the roster out to the host page (see the Props note): the
  // internal deriveds stay the single source, the bindables are their
  // published copies.
  $effect(() => {
    providerConnectionsOut = providerConnections
  })
  $effect(() => {
    paginatedConnectionsOut = paginatedConnections
  })
  $effect(() => {
    connectionHealthByIdOut = connectionHealthById
  })
  $effect(() => {
    totalConnectionsCountOut = totalConnectionsCount
  })
</script>


  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] p-6">
    <!-- Header -->
    <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <h2 class="text-lg font-semibold">Connections</h2>
      <div class="flex flex-1 flex-wrap items-center gap-2">
        {#if selectedConnIds.length > 0}
          <button
            type="button"
            onclick={handleDeleteSelected}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-red-500/10 hover:bg-red-500/20 text-red-600 dark:text-red-400 border border-red-500/30 h-8 px-3 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[16px]">delete</span>
            Delete Selected ({selectedConnIds.length})
          </button>
        {/if}

        {#if providerConnections.length > 0 && proxyPools.length > 0}
          <button
            type="button"
            onclick={() => (showApplyProxyModal = true)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-3 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[16px]">lan</span>
            Apply Proxy
          </button>
        {/if}

        {#if providerConnections.length > 0}
          <button
            type="button"
            onclick={handleBulkResetStatus}
            disabled={isResettingCooldown}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-amber-500/10 hover:bg-amber-500/20 text-amber-600 dark:text-amber-400 border border-amber-500/30 h-8 px-3 text-xs rounded-[8px]"
            title={selectedConnIds.length > 0 ? 'Reset cooldown & exhausted status for selected accounts' : 'Reset cooldown & exhausted status for all active accounts in this provider'}
          >
            <span class="material-symbols-outlined text-[16px] {isResettingCooldown ? 'animate-spin' : ''}">restart_alt</span>
            {selectedConnIds.length > 0 ? `Reset Cooldown (${selectedConnIds.length})` : 'Reset Cooldown / Exhausted'}
          </button>
        {/if}

        {#if isJwtBulkProvider}
          <button
            type="button"
            onclick={() => (showBulkJwtModal = true)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-3 text-xs rounded-[8px]"
            title="Bulk import JWT / token accounts"
          >
            <span class="material-symbols-outlined text-[16px]">playlist_add</span>
            Bulk Add
          </button>
        {/if}

        <button
          type="button"
          onclick={runOneByOneTest}
          disabled={isTestingOneByOne || providerConnections.length === 0}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-3 text-xs rounded-[8px]"
        >
          <span class="material-symbols-outlined text-[16px] {isTestingOneByOne ? 'animate-spin text-primary' : ''}">sync</span>
          {isTestingOneByOne ? 'Testing Connection One-by-One...' : 'Test Connection One-by-One'}
        </button>

        {#if isTestingOneByOne}
          <button
            type="button"
            onclick={stopOneByOneTest}
            disabled={isStoppingOneByOne}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed bg-transparent hover:bg-surface-2 text-text-muted hover:text-text-main h-8 px-3 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[16px]">stop</span>
            {isStoppingOneByOne ? 'Stopping...' : 'Stop'}
          </button>
        {/if}

        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5 sm:ml-auto rounded-lg border border-border bg-surface-2/60 px-2.5 py-1">
          <span class="text-xs text-text-muted font-medium">Round Robin</span>
          <button
            type="button"
            role="switch"
            aria-label="Toggle Round Robin"
            aria-checked={isRoundRobin}
            onclick={toggleRoundRobin}
            class="relative inline-flex shrink-0 cursor-pointer rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-brand-500/30 {isRoundRobin ? 'bg-brand-500' : 'bg-surface-3'} w-11 h-6"
          >
            <span
              class="pointer-events-none inline-block rounded-full bg-white shadow-sm transform transition duration-200 ease-in-out {isRoundRobin ? 'translate-x-5' : 'translate-x-0.5'} size-5 mt-0.5"
            ></span>
          </button>
          {#if isRoundRobin}
            <div class="flex items-center gap-1.5">
              <span class="text-xs text-text-muted">Sticky:</span>
              <input
                type="number"
                min="1"
                bind:value={stickyLimit}
                onchange={handleStickyLimitChange}
                placeholder="1"
                class="w-14 px-2 py-1 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary"
              />
            </div>
          {/if}

          <button
            type="button"
            onclick={() => (showHeaderOverrides = true)}
            class="inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-xs font-medium text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main"
          >
            <span class="material-symbols-outlined text-[16px]" aria-hidden="true">tune</span>
            Custom headers
          </button>

          <!-- Provider Proxy Lock -->
          <div class="flex items-center gap-1.5 border-l border-border/70 pl-3">
            <span class="text-xs text-text-muted font-medium flex items-center gap-1" title="Lock provider to a proxy group. All accounts automatically route through it unless set to a custom proxy.">
              <span class="material-symbols-outlined text-[16px] {lockedProxyGroup || lockedProxyPoolId ? 'text-primary' : 'text-text-muted'}">lock</span>
              Proxy Lock:
            </span>
            <div class="relative">
              <button
                type="button"
                onclick={toggleProxyLockMenu}
                class="flex items-center gap-1 bg-surface-2 border border-border rounded-lg px-2 py-1 text-xs text-text-main focus:outline-none focus:border-primary cursor-pointer"
              >
                <span class="max-w-[220px] truncate">{lockedProxyLabel}</span>
                <span class="material-symbols-outlined text-[16px] text-text-muted">expand_more</span>
              </button>
              {#if proxyLockOpen}
                <!-- Backdrop -->
                <div
                  class="fixed inset-0 z-[80]"
                  onclick={() => (proxyLockOpen = false)}
                  role="presentation"
                ></div>
                <div
                  class="fixed z-[85] w-[270px] max-w-[85vw] max-h-[340px] overflow-y-auto rounded-xl border border-border bg-surface shadow-2xl py-1"
                  style="top: {proxyLockPos.top}px; right: {proxyLockPos.right}px;"
                >
                  <div class="p-1">
                    <button
                      type="button"
                      onclick={() => { proxyLockOpen = false; applyProxyGroupLock('') }}
                      class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {!lockedProxyGroup && !lockedProxyPoolId ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                    >
                      <span class="truncate">None (direct)</span>
                      {#if !lockedProxyGroup && !lockedProxyPoolId}
                        <span class="material-symbols-outlined text-[16px] text-primary shrink-0">check</span>
                      {/if}
                    </button>
                  </div>
                  {#if availableProxyGroups.length > 0}
                    <div class="border-t border-border/60 px-3 pt-2 pb-1 text-[10px] font-medium uppercase tracking-wide text-text-muted">
                      Proxy Groups — select one or more
                    </div>
                    <div class="p-1">
                      {#each availableProxyGroups as grp}
                        <label class="flex items-center gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {lockedGroupNames.includes(grp.name) ? 'text-primary font-medium' : 'text-text-main'}">
                          <input
                            type="checkbox"
                            checked={lockedGroupNames.includes(grp.name)}
                            onchange={() => toggleLockGroup(grp.name)}
                            class="h-3.5 w-3.5 shrink-0 accent-[var(--color-primary)]"
                          />
                          <span class="flex-1 truncate">Group: {grp.name}</span>
                          <span class="text-text-muted font-normal">({grp.active} active)</span>
                        </label>
                      {/each}
                    </div>
                  {/if}
                  {#if availableProxyTypes.length > 0}
                    <div class="border-t border-border/60 px-3 pt-2 pb-1 text-[10px] font-medium uppercase tracking-wide text-text-muted">
                      Proxy Types — single selection
                    </div>
                    <div class="p-1">
                      {#each availableProxyTypes as typ}
                        <button
                          type="button"
                          onclick={() => { proxyLockOpen = false; applyProxyGroupLock(`type:${typ.type}`) }}
                          class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {lockedProxyGroup === `type:${typ.type}` ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                        >
                          <span class="truncate">Type: {typ.type} ({typ.active} active)</span>
                          {#if lockedProxyGroup === `type:${typ.type}`}
                            <span class="material-symbols-outlined text-[16px] text-primary shrink-0">check</span>
                          {/if}
                        </button>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        </div>
      </div>
    </div>

    {#if lockedProxyGroup}
      <div class="mb-4">
        <ProxyResolutionNote {providerId} refreshKey={lockedProxyGroup} />
      </div>
    {/if}

    <!-- One-by-One summary (upstream parity) -->
    {#if oneByOneSummary}
      <div class="mb-4 rounded-lg border border-black/10 bg-black/[0.02] px-3 py-2 text-xs text-text-muted dark:border-white/10 dark:bg-white/[0.03]">
        <div class="flex flex-wrap items-center gap-3">
          <span>Total: {oneByOneSummary.total}</span>
          <span>Completed: {oneByOneSummary.completed}</span>
          <span>Passed: {oneByOneSummary.passed}</span>
          <span>Failed: {oneByOneSummary.failed}</span>
          {#if oneByOneSummary.stopped}
            <span class="text-amber-600 dark:text-amber-400">Stopped</span>
          {/if}
          {#if isTestingOneByOne && oneByOneCurrentId}
            <span>
              Running: {formatEmailLabel(providerConnections.find((conn) => conn.id === oneByOneCurrentId)?.name || oneByOneCurrentId, $emailPrivacy)}
            </span>
          {/if}
        </div>
      </div>
    {/if}

    <!-- Select All Checkbox & Sensor Email Toggle -->
    {#if totalConnectionsCount > 0 || providerConnections.length > 0}
      <div class="mb-3 flex items-center justify-between gap-2 border-b border-black/[0.03] pb-2 dark:border-white/[0.03]">
        <label class="flex cursor-pointer items-center gap-1.5 text-xs text-text-muted hover:text-primary">
          <input
            type="checkbox"
            checked={isAllSelected}
            onchange={toggleSelectAll}
            class="h-3.5 w-3.5 rounded border-gray-300 text-primary focus:ring-primary cursor-pointer"
          />
          Select All
        </label>

        <div class="flex items-center gap-2">
          {#if selectedConnIds.length > 0}
            <button
              type="button"
              onclick={handleBulkResetStatus}
              class="flex items-center gap-1.5 rounded-lg border border-warning/40 bg-warning/10 px-2.5 py-1 text-xs font-medium text-warning hover:bg-warning/20 transition-colors cursor-pointer"
              title="Reset status and clear cooldowns for selected connections"
            >
              <span class="material-symbols-outlined text-[15px]">restart_alt</span>
              <span>Reset Cooldown ({selectedConnIds.length})</span>
            </button>
          {:else}
            <button
              type="button"
              onclick={handleBulkResetStatus}
              class="flex items-center gap-1.5 rounded-lg border border-border-subtle bg-surface px-2.5 py-1 text-xs text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
              title="Reset status and clear all cooldowns/exhausted for this provider"
            >
              <span class="material-symbols-outlined text-[15px]">restart_alt</span>
              <span>Reset All Cooldowns</span>
            </button>
          {/if}

          <!-- Sensor Email Toggle Button -->
          <button
            type="button"
            onclick={() => emailPrivacy.toggle()}
            class="flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs transition-colors cursor-pointer {$emailPrivacy ? 'border-brand-500/40 bg-brand-500/10 text-brand-500 font-medium' : 'border-border-subtle bg-surface text-text-muted hover:text-text-main hover:bg-surface-2'}"
            title={$emailPrivacy ? 'Tampilkan email lengkap' : 'Sensor / sembunyikan email'}
          >
            <span class="material-symbols-outlined text-[15px]">
              {$emailPrivacy ? 'visibility_off' : 'visibility'}
            </span>
            <span>{$emailPrivacy ? 'Email Disensor' : 'Sensor Email'}</span>
          </button>
        </div>
      </div>
    {/if}

    <!-- Connections List (the account status strip lives in the page
         header, beside the connection count) -->
      {#if isLoadingConnections && paginatedConnections.length === 0}
        <div class="flex items-center justify-center p-8 text-text-muted gap-2 text-sm">
          <span class="material-symbols-outlined animate-spin text-[20px]">progress_activity</span>
          <span>Loading connections...</span>
        </div>
      {:else if totalConnectionsCount === 0 && paginatedConnections.length === 0}
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex items-center gap-3">
          <div class="inline-flex items-center justify-center w-9 h-9 rounded-full bg-primary/10 text-primary shrink-0">
            <span class="material-symbols-outlined text-[18px]">{isOAuth ? 'lock' : 'key'}</span>
          </div>
          <div class="min-w-0">
            <p class="text-sm text-text-muted">No connections yet</p>
            {#if hasDualAuthModes}
              <p class="text-xs text-text-muted">
                Choose {oauthButtonLabel} or {apiKeyButtonLabel}.
              </p>
            {/if}
          </div>
        </div>
        <div class="flex gap-2">
        {#if providerId === 'freebuff'}
          <button
            type="button"
            onclick={startFreebuffFlow}
            disabled={isAuthorizingFreebuff}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">vpn_key</span>
            {isAuthorizingFreebuff ? 'Polling Authorization...' : 'Authorize Freebuff CLI'}
          </button>
        {:else if providerId === 'antigravity'}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">login</span>
            Connect Google Account
          </button>
        {:else if providerId === 'kiro'}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">login</span>
            Connect Kiro
          </button>
        {:else if hasDualAuthModes}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">lock</span>
            {oauthButtonLabel}
          </button>
          <button
            type="button"
            onclick={openAddKeyModal}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">key</span>
            {apiKeyButtonLabel}
          </button>
        {:else}
          <button
            type="button"
            onclick={handleAddConnectionClick}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-8 px-4 text-xs rounded-[8px]"
          >
            <span class="material-symbols-outlined text-[18px]">add</span>
            Add Connection
          </button>
        {/if}
        </div>
      </div>
    {:else}
      {#if pageQuotasPending > 0}
        <div class="flex items-center justify-end px-1 py-2">
          <button
            type="button"
            onclick={() => loadPageUsage(paginatedConnections)}
            class="inline-flex h-8 cursor-pointer items-center justify-center gap-2 rounded-[8px] border border-border bg-surface-2 px-3 text-xs font-medium text-text-main transition-colors hover:bg-surface-3"
          >
            <span class="material-symbols-outlined text-[18px]">cloud_download</span>
            Load quotas ({pageQuotasPending})
          </button>
        </div>
      {/if}
      <!-- Account search (Quota Tracker parity): the server filters the
           provider's whole list, so this finds accounts on any page. -->
      <form
        onsubmit={(e) => {
          e.preventDefault()
          handleConnSearch()
        }}
        class="flex items-center gap-1 px-1 py-2"
      >
        <input
          bind:value={connSearchQuery}
          type="search"
          placeholder="Search name or email"
          aria-label="Search accounts by name or email"
          class="h-8 w-44 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors placeholder:text-text-muted"
        />
        <button
          type="submit"
          aria-label="Search"
          title="Search"
          class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg border border-border-subtle bg-surface-2 text-text-muted transition-colors hover:bg-surface-3 hover:text-text-main"
        >
          <span class="material-symbols-outlined text-[18px]">search</span>
        </button>
        {#if appliedConnSearch}
          <button
            type="button"
            onclick={handleClearConnSearch}
            aria-label="Clear search"
            title="Clear search"
            class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-text-muted transition-colors hover:bg-surface-3 hover:text-text-main"
          >
            <span class="material-symbols-outlined text-[18px]">close</span>
          </button>
          <span class="text-xs text-text-muted">{totalConnectionsCount} matching accounts</span>
        {/if}
      </form>
      {#if appliedConnSearch && paginatedConnections.length === 0 && !isLoadingConnections}
        <p class="px-1 pb-2 text-xs text-text-muted">
          No accounts match "{appliedConnSearch}".
        </p>
      {/if}
      <!-- relative z-50: the open row proxy dropdown paints a fixed inset-0
           click-away backdrop over the whole viewport, which otherwise swallows
           the first click on any priority chevron. -->
      <div class="relative z-50 flex min-w-0 flex-col divide-y divide-black/[0.03] dark:divide-white/[0.03] max-h-[500px] overflow-y-auto pr-1">
        {#each paginatedConnections as conn, localIdx (conn.id)}
          {@const globalIdx = (connPage - 1) * connPageSize + localIdx}
          {@const isFirst = globalIdx === 0}
          {@const isLast = globalIdx === totalConnectionsCount - 1}
          {@const isSelected = selectedConnIds.includes(conn.id)}
          {@const status = oneByOneStatuses[conn.id]}
          {@const specificData = conn.providerSpecificData as Record<string, unknown> | undefined}
          {@const assignedPoolId = (typeof specificData?.proxyPoolId === 'string' ? specificData.proxyPoolId : null)}
          {@const proxyBadge = proxyBadgeFor(conn)}
          {@const lastErr = normalizeLastError(conn.lastError || status?.error) || ''}
          {@const isFatalDisabledErr =
            lastErr.toLowerCase().includes('disabled in this account') ||
            lastErr.toLowerCase().includes('terms of service') ||
            lastErr.toLowerCase().includes('tos_violation') ||
            lastErr.toLowerCase().includes('account disabled') ||
            lastErr.toLowerCase().includes('validation_required') ||
            lastErr.toLowerCase().includes('verify your account') ||
            lastErr.toLowerCase().includes('invalid_grant') ||
            lastErr.toLowerCase().includes('token refresh rejected')}
          {@const priorityNum = conn.priority ?? globalIdx + 1}
          {@const isConnActive = ((conn.isActive as number | boolean) === 1 || (conn.isActive as number | boolean) === true) && conn.testStatus !== 'disabled' && !isFatalDisabledErr}
          {@const cooldownInfo = getCooldownInfo(conn)}
          {@const health = connectionHealthById.get(conn.id)}
          <!-- badgeFor owns the precedence between the live probe and the
               connection's persisted testStatus; the markup below only renders
               the verdict it returns. -->
          {@const connBadge = badgeFor(status, conn.testStatus, lastErr)}
          <div class="flex min-w-0 items-stretch">
            <!-- Multi-select checkbox -->
            <div class="flex shrink-0 items-center pl-1 sm:pl-2">
              <input
                type="checkbox"
                checked={isSelected}
                onchange={() => toggleSelect(conn.id)}
                class="h-4 w-4 rounded border-gray-300 text-primary focus:ring-primary cursor-pointer"
              />
            </div>

            <!-- Connection Row Content -->
            <div class="flex-1 min-w-0">
              <div class="group flex min-w-0 flex-col gap-3 rounded-lg p-2 transition-colors hover:bg-black/[0.02] dark:hover:bg-white/[0.02] sm:flex-row sm:items-center sm:justify-between">
                <!-- Left info -->
                <div class="flex min-w-0 flex-1 items-start gap-2 sm:items-center sm:gap-3">
                  <!-- Reorder buttons. z-[60] sits above the row proxy
                       dropdown's fixed inset-0 click-away backdrop (z-40): a
                       positioned backdrop paints above unpositioned content, so
                       without this the first chevron click after opening a
                       dropdown is always swallowed. -->
                  <div class="relative z-[60] flex shrink-0 flex-col">
                    <button
                      type="button"
                      disabled={isFirst || !!reorderingConnId}
                      title={isFirst ? 'Already first' : 'Move up'}
                      onclick={() => swapPriority(globalIdx, -1)}
                      class="p-1 rounded disabled:opacity-40 {isFirst || reorderingConnId ? 'text-text-muted/30 cursor-not-allowed' : 'hover:bg-sidebar text-text-muted hover:text-primary cursor-pointer'}"
                    >
                      <span class="material-symbols-outlined text-sm">keyboard_arrow_up</span>
                    </button>
                    <button
                      type="button"
                      disabled={isLast || !!reorderingConnId}
                      title={isLast ? 'Already last' : 'Move down'}
                      onclick={() => swapPriority(globalIdx, 1)}
                      class="p-1 rounded disabled:opacity-40 {isLast || reorderingConnId ? 'text-text-muted/30 cursor-not-allowed' : 'hover:bg-sidebar text-text-muted hover:text-primary cursor-pointer'}"
                    >
                      <span class="material-symbols-outlined text-sm">keyboard_arrow_down</span>
                    </button>
                  </div>

                  <!-- Lock icon -->
                  <span class="material-symbols-outlined shrink-0 text-base text-text-muted">lock</span>

                  <!-- Title & badges -->
                  <div class="flex-1 min-w-0">
                    <p class="text-sm font-medium truncate">
                      {formatEmailLabel(conn.name || conn.email || (conn.authType === 'oauth' ? 'OAuth Account' : 'API Key Slot'), $emailPrivacy)}
                    </p>
                    <!-- Secondary label: upstream ConnectionRow shows the email
                         under a name, or the displayName when the stored name is
                         the email (which is what the Qoder device flow writes). -->
                    {#if secondaryConnLabel(conn)}
                      <p class="truncate text-xs text-text-muted">{formatEmailLabel(secondaryConnLabel(conn), $emailPrivacy)}</p>
                    {/if}
                    <div class="mt-1 flex min-w-0 flex-wrap items-center gap-1.5 sm:gap-2">
                      {#if health?.state === 'validation'}
                        <!-- Verification-required (first-use account).
                             The owner's spec (2026-11-09, fragment 1061):
                             the Verifikasi button test-calls the chat API
                             for this account. If the gate is gone, the
                             account is enabled in the same call; if it is
                             still gated, the response carries the
                             verification URL that test just minted and
                             the error panel shows it ("Buka Verifikasi")
                             for an immediate click. The URL is never
                             stored: minted per refusal, it only resolves
                             while a live sign-in session for the account
                             exists in the browser. What verifies is the
                             scan step in Google's own page — no
                             sign-in-based action anywhere. -->
                        <span
                          class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] border bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/30"
                          title={lastErr || 'Google requires account verification before this account can serve again'}
                        >
                          <span class="material-symbols-outlined text-[12px]">gpp_maybe</span>
                          needs verification
                        </span>
                        <button
                          type="button"
                          onclick={() => recheckVerification(conn)}
                          disabled={recheckingId === conn.id}
                          class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-primary/10 text-primary hover:bg-primary/20 transition-colors disabled:opacity-50 cursor-pointer"
                          title="Test-call the chat API for this account now: if the verification gate is gone, the account is enabled; if it is still gated, the error panel shows the fresh verification URL from this test"
                        >
                          {#if recheckingId === conn.id}
                            <span class="material-symbols-outlined text-[11px] animate-spin">progress_activity</span>
                          {:else}
                            <span class="material-symbols-outlined text-[11px]">fact_check</span>
                          {/if}
                          verifikasi
                        </button>
                      {:else if !isConnActive || conn.testStatus === 'disabled' || isFatalDisabledErr}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-surface-3 text-text-muted px-2 py-0.5 text-[10px]"
                          title={conn.disabledReason ? `disabled: ${conn.disabledReason}` : lastErr ? `disabled: ${lastErr}` : 'Connection is disabled'}
                        >
                          <span class="size-1.5 rounded-full bg-text-muted/60"></span>
                          {health?.tosDisabled ? 'disabled · ToS' : 'disabled'}
                        </span>
                      {:else if connBadge === 'queued'}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-surface-2 text-text-muted px-2 py-0.5 text-[10px]"
                          title="Waiting for its turn"
                        >
                          <span class="size-1.5 rounded-full bg-text-muted/50"></span>
                          queued
                        </span>
                      {:else if connBadge === 'testing'}
                        <span class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-blue-500/10 text-blue-600 dark:text-blue-400 px-2 py-0.5 text-[10px]">
                          <span class="material-symbols-outlined text-[10px] animate-spin">progress_activity</span>
                          testing
                        </span>
                      {:else if connBadge === 'error'}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-red-500/10 text-red-600 dark:text-red-400 px-2 py-0.5 text-[10px]"
                          title={status?.state === 'failed' && status.error ? `failed: ${status.error}` : undefined}
                        >
                          <span class="size-1.5 rounded-full bg-red-500"></span>
                          error
                        </span>
                      {:else}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-green-500/10 text-green-600 dark:text-green-400 px-2 py-0.5 text-[10px]"
                          title={status?.state === 'success' ? 'last probe passed' : undefined}
                        >
                          <span class="size-1.5 rounded-full bg-green-500"></span>
                          active
                        </span>
                      {/if}
                      <!-- Cooldown & Exhausted Quota badge with live timer.
                           The plain lock timer yields to the health lock
                           badge below when it fires: that one names the
                           locked model, the timer alone does not. -->
                      {#if cooldownInfo && !(cooldownInfo.isLock && health && health.locks.length > 0)}
                        {#if cooldownInfo.isLock}
                          <span class="text-xs text-orange-500 font-mono" title={cooldownInfo.title}>
                            {cooldownInfo.label}
                          </span>
                        {:else}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] border {cooldownInfo.isExhausted
                              ? 'bg-rose-500/10 text-rose-600 dark:text-rose-400 border-rose-500/30'
                              : 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/30'}"
                            title={cooldownInfo.title}
                          >
                            <span class="material-symbols-outlined text-[12px] animate-pulse">hourglass_top</span>
                            {cooldownInfo.label}
                          </span>
                        {/if}
                      {/if}

                      <!-- Account health badges (server effectiveStatus +
                           locks census; see connectionHealth.ts): live
                           locks name the model and its expiry, a bare
                           rate-limit verdict with no recorded reset gets
                           its cooldown pill here, and an error verdict
                           the probe badge called "active" is surfaced
                           instead of hidden. -->
                      {#if health && health.locks.length > 0}
                        {@const firstLock = health.locks[0]}
                        <span
                          class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] border bg-orange-500/10 text-orange-600 dark:text-orange-400 border-orange-500/30"
                          title={health.locks
                            .map((l) => `${l.model === '__all' ? 'All models' : l.model} locked until ${new Date(l.until).toLocaleString()}`)
                            .join('\n')}
                        >
                          <span class="material-symbols-outlined text-[12px]">lock</span>
                          locked: {firstLock.model === '__all' ? 'all models' : firstLock.model}{health.locks.length > 1
                            ? ` +${health.locks.length - 1}`
                            : ''} · {formatRemaining(new Date(firstLock.until).getTime(), currentTime)}
                        </span>
                      {/if}
                      {#if health?.state === 'cooldown' && !cooldownInfo}
                        <span
                          class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] border bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/30"
                          title="Rate limited by the upstream; no reset time recorded"
                        >
                          <span class="material-symbols-outlined text-[12px]">hourglass_top</span>
                          cooldown
                        </span>
                      {/if}
                      {#if health?.state === 'error' && connBadge === 'active' && isConnActive}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-red-500/10 text-red-600 dark:text-red-400 px-2 py-0.5 text-[10px]"
                          title={lastErr || undefined}
                        >
                          <span class="size-1.5 rounded-full bg-red-500"></span>
                          {statusLabel(health.effectiveStatus)}
                        </span>
                      {/if}

                      <!-- Auth type badge -->
                      <span class="inline-flex items-center gap-1.5 rounded-full font-semibold bg-surface-2 text-text-muted px-2 py-0.5 text-[10px]">
                        {conn.authType === 'oauth' ? 'OAuth' : 'API Key'}
                      </span>
                      <!-- Freebuff Session status badge -->
                      {#if isFreebuff}
                        {@const fbSess = freebuffSessions[conn.id]}
                        {@const boundModel = fbSess?.currentModel || (specificData?.freebuffModel as string) || (specificData?.assignedModel as string)}
                        {#if fbSess?.status === 'active' || boundModel}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20"
                            title="Active session bound to {boundModel}"
                          >
                            <span>🔒</span>
                            <span class="font-mono">{boundModel}</span>
                          </span>
                        {:else if fbSess?.status === 'queued'}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/20"
                            title="Waiting in queue"
                          >
                            <span>⏳</span> queued
                          </span>
                        {:else if fbSess?.status === 'banned'}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-semibold px-2 py-0.5 text-[10px] bg-red-500/10 text-red-600 dark:text-red-400 border border-red-500/20"
                            title="Account banned"
                          >
                            <span>🚫</span> banned
                          </span>
                        {:else if fbSess}
                          <span
                            class="inline-flex items-center gap-1 rounded-full font-normal px-2 py-0.5 text-[10px] bg-surface-2 text-text-muted border border-border/50"
                            title="No active session held"
                          >
                            no session
                          </span>
                        {/if}
                      {/if}
                      <!-- Proxy badge (upstream: green when the bound pool is active,
                           red when bound to a missing/inactive pool or a legacy proxy) -->
                      {#if proxyBadge.hasAnyProxy}
                        <span
                          class="inline-flex items-center gap-1.5 rounded-full font-semibold px-2 py-0.5 text-[10px] {proxyBadge.variant === 'success'
                            ? 'bg-green-500/10 text-green-600 dark:text-green-400'
                            : 'bg-red-500/10 text-red-600 dark:text-red-400'}"
                          title={proxyBadge.displayText}
                        >
                          <span class="size-1.5 rounded-full {proxyBadge.variant === 'success' ? 'bg-green-500' : 'bg-red-500'}"></span>
                          Proxy
                        </span>
                      {/if}
                      <!-- Last error tooltip -->
                      {#if lastErr && lastErr !== 'Provider test not supported'}
                        {@const isModelScoped = Boolean(conn.lastErrorModel)}
                        {@const isHealthy = connBadge !== 'error'}
                        <div
                          class="flex items-center gap-1 min-w-0 max-w-full sm:max-w-[360px]"
                          title={isModelScoped ? `[${conn.lastErrorModel}] ${lastErr}` : lastErr}
                        >
                          {#if isModelScoped}
                            <span class="font-mono text-[11px] text-text-muted shrink-0">[{conn.lastErrorModel}]</span>
                          {/if}
                          <span class="truncate text-xs {isHealthy ? 'text-text-muted' : 'text-red-500'}">
                            {lastErr.length > 50 ? lastErr.slice(0, 50) + '...' : lastErr}
                          </span>
                        </div>
                      {/if}

                      <!-- Priority tag -->
                      <span class="text-xs text-text-muted">#{priorityNum}</span>
                      <!-- Credits left, fetched on demand via the Actions
                           menu (upstream ConnectionRow isCreditAware). -->
                      {#if isCreditAwareConn(conn) && creditInfoByConn[conn.id]}
                        {@const creditInfo = creditInfoByConn[conn.id]}
                        <span
                          class="max-w-full truncate text-xs text-text-muted"
                          title={creditInfo.message ? creditInfo.message : `Plan: ${creditInfo.plan || '?'}`}
                        >
                          {creditInfo.message
                            ? `Credits: ${creditInfo.message}`
                            : `Credits left: ${creditInfo.remaining.toFixed(2)} / ${creditInfo.total.toFixed(2)}${creditInfo.resetAtMs ? ` (resets ${new Date(creditInfo.resetAtMs).toLocaleDateString()})` : ''}`}
                        </span>
                      {/if}
                    </div>
                    <!-- Error message block -->
                    {#if lastErr && lastErr !== 'Provider test not supported' && (status?.state === 'failed' || conn.testStatus === 'failed' || conn.testStatus === 'error' || conn.testStatus === 'unavailable' || !isConnActive || connBadge === 'error' || lastErr.toLowerCase().includes('terms of service') || lastErr.toLowerCase().includes('disabled in this account') || lastErr.toLowerCase().includes('account disabled') || lastErr.toLowerCase().includes('tos_violation') || lastErr.toLowerCase().includes('validation_required') || lastErr.toLowerCase().includes('verify your account') || Boolean(freshVerifyUrls[conn.id]))}
                      <div class="mt-1.5 flex flex-col gap-1.5 text-xs text-red-500 bg-red-500/10 px-2.5 py-1.5 rounded-md border border-red-500/20 max-w-full">
                        <div class="flex items-start gap-1.5">
                          <span class="material-symbols-outlined text-sm shrink-0 mt-0.5">error</span>
                          <span class="break-words font-medium leading-relaxed">
                            {#if conn.lastErrorModel}
                              <span class="font-mono text-[11px] mr-1">[{conn.lastErrorModel}]</span>
                            {/if}
                            {lastErr}
                          </span>
                        </div>
                        {#if lastErr.toLowerCase().includes('terms of service') || lastErr.toLowerCase().includes('disabled in this account') || lastErr.toLowerCase().includes('account disabled') || lastErr.toLowerCase().includes('tos_violation')}
                          <div class="flex flex-wrap items-center justify-between gap-2 pt-1.5 mt-0.5 border-t border-red-500/20 text-xs">
                            <span class="text-amber-400 font-medium">Akun dinonaktifkan (Pelanggaran ToS) — Appeal: https://forms.gle/hGzM9MEUv2azZsrb9</span>
                            <a
                              href="https://forms.gle/hGzM9MEUv2azZsrb9"
                              target="_blank"
                              rel="noopener noreferrer"
                              class="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-amber-500 hover:bg-amber-400 text-black font-semibold shadow-sm transition-colors"
                            >
                              <span>Kunjungi Form Appeal</span>
                              <span class="material-symbols-outlined text-xs">open_in_new</span>
                            </a>
                          </div>
                        {/if}
                        {#if freshVerifyUrls[conn.id]}
                          <div class="flex flex-wrap items-center justify-between gap-2 pt-1.5 mt-0.5 border-t border-red-500/20 text-xs">
                            <span class="text-amber-400 font-medium">Verifikasi akun Google/Antigravity diperlukan:</span>
                            <a
                              href={freshVerifyUrls[conn.id]}
                              target="_blank"
                              rel="noreferrer"
                              class="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-amber-600 hover:bg-amber-500 text-white font-semibold shadow-sm transition-colors cursor-pointer"
                            >
                              <span>Buka Verifikasi</span>
                              <span class="material-symbols-outlined text-xs">open_in_new</span>
                            </a>
                          </div>
                        {/if}
                      </div>
                    {/if}
                    <!-- Proxy detail line: pool/legacy label, masked endpoint, no_proxy -->
                    {#if proxyBadge.hasAnyProxy}
                      <div class="mt-1 flex min-w-0 flex-wrap items-center gap-2">
                        <span
                          class="max-w-full truncate text-[11px] text-text-muted sm:max-w-[420px]"
                          title={proxyBadge.displayText}
                        >
                          {proxyBadge.displayText}
                        </span>
                        {#if proxyBadge.maskedProxyUrl}
                          <code
                            class="max-w-full truncate rounded bg-black/5 px-1 py-0.5 font-mono text-[10px] text-text-muted dark:bg-white/5 sm:max-w-[260px]"
                          >
                            {proxyBadge.maskedProxyUrl}
                          </code>
                        {/if}
                        {#if proxyBadge.noProxyText}
                          <span
                            class="max-w-full truncate text-[11px] text-text-muted sm:max-w-[320px]"
                            title={proxyBadge.noProxyText}
                          >
                            no_proxy: {proxyBadge.noProxyText}
                          </span>
                        {/if}
                      </div>
                    {/if}
                  </div>
                </div>

                <!-- Right actions -->
                <div class="flex w-full items-center justify-between gap-2 sm:w-auto sm:justify-end">
                  <!-- The proxy picker stays a control of its own: it is a
                       two-tap choice between pools, not a verb. Everything that
                       runs and is done moved into one menu, because four to five
                       labelled buttons in a row left the connection name almost
                       no width (issue #224). -->
                  <div class="grid flex-1 grid-flow-col auto-cols-fr gap-1 sm:flex sm:flex-none">
                    <!-- Proxy dropdown -->
                    <div class="relative">
                      <button
                        type="button"
                        onclick={(e) => {
                          e.stopPropagation()
                          if (activeProxyDropdownId === conn.id) {
                            activeProxyDropdownId = null
                            proxySearch = ''
                          } else {
                            activeProxyDropdownId = conn.id
                            proxySearch = ''
                            const rect = e.currentTarget.getBoundingClientRect()
                            dropdownPos = {
                              top: rect.bottom + 4,
                              right: Math.max(8, window.innerWidth - rect.right)
                            }
                          }
                        }}
                        disabled={updatingProxyConnId === conn.id}
                        class="flex w-full flex-col items-center rounded px-2 py-1 transition-colors hover:bg-black/5 dark:hover:bg-white/5 disabled:opacity-60 {proxyBadge.hasAnyProxy ? 'text-primary' : 'text-text-muted hover:text-primary'} cursor-pointer"
                        title={proxyBadge.displayText}
                      >
                        <span class="material-symbols-outlined text-[18px] {updatingProxyConnId === conn.id ? 'animate-spin' : ''}">
                          {updatingProxyConnId === conn.id ? 'progress_activity' : (proxyBadge.hasAnyProxy ? 'vpn_lock' : 'lan')}
                        </span>
                        <span class="text-[10px] leading-tight">Proxy</span>
                      </button>

                      {#if activeProxyDropdownId === conn.id}
                        <!-- Backdrop -->
                        <div
                          class="fixed inset-0 z-[80]"
                          onclick={() => { activeProxyDropdownId = null; proxySearch = ''; }}
                          role="presentation"
                        ></div>
                        <div
                          class="fixed z-[85] max-w-[85vw] w-[290px] max-h-[380px] flex flex-col rounded-xl border border-border bg-surface shadow-2xl overflow-hidden"
                          style="top: {dropdownPos.top}px; right: {dropdownPos.right}px;"
                        >
                          <!-- Search Header -->
                          <div class="p-2 border-b border-border bg-surface-2/40 shrink-0">
                            <div class="relative">
                              <span class="material-symbols-outlined absolute left-2 top-1.5 text-[16px] text-text-muted">search</span>
                              <input
                                type="text"
                                bind:value={proxySearch}
                                placeholder="Search proxy or group..."
                                class="w-full pl-7 pr-2 py-1 text-xs rounded-lg border border-border bg-surface text-text-main placeholder:text-text-muted/60 focus:outline-none focus:border-primary"
                              />
                            </div>
                          </div>

                          <div class="overflow-y-auto flex-1 divide-y divide-border/40 py-1">
                            <!-- Default / Direct Options -->
                            <div class="p-1 space-y-0.5">
                              {#if lockedProxyGroup}
                                <button
                                  type="button"
                                  onclick={() => { assignProxyPool(conn, null); proxySearch = ''; }}
                                  class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {!assignedPoolId ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                                >
                                  <div class="flex items-center gap-2 truncate">
                                    <span class="material-symbols-outlined text-[16px] text-primary">lock</span>
                                    <div class="flex flex-col text-left truncate">
                                      <span class="truncate font-medium">Auto: {lockedProxyLabel}</span>
                                      <span class="text-[10px] text-text-muted font-normal">Inherit provider lock (Default)</span>
                                    </div>
                                  </div>
                                  {#if !assignedPoolId}
                                    <span class="material-symbols-outlined text-[16px] text-primary shrink-0">check</span>
                                  {/if}
                                </button>
                                <button
                                  type="button"
                                  onclick={() => { assignProxyPool(conn, '__none__'); proxySearch = ''; }}
                                  class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {assignedPoolId === '__none__' || assignedPoolId === 'direct' ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                                >
                                  <div class="flex items-center gap-2 truncate">
                                    <span class="material-symbols-outlined text-[16px] text-text-muted">block</span>
                                    <div class="flex flex-col text-left truncate">
                                      <span class="truncate">Direct (Bypass lock)</span>
                                      <span class="text-[10px] text-text-muted font-normal">No proxy for this account</span>
                                    </div>
                                  </div>
                                  {#if assignedPoolId === '__none__' || assignedPoolId === 'direct'}
                                    <span class="material-symbols-outlined text-[16px] text-primary shrink-0">check</span>
                                  {/if}
                                </button>
                              {:else}
                                <button
                                  type="button"
                                  onclick={() => { assignProxyPool(conn, null); proxySearch = ''; }}
                                  class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {!assignedPoolId ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                                >
                                  <div class="flex items-center gap-2 truncate">
                                    <span class="material-symbols-outlined text-[16px] text-text-muted">public_off</span>
                                    <span>Direct (No proxy)</span>
                                  </div>
                                  {#if !assignedPoolId}
                                    <span class="material-symbols-outlined text-[16px] text-primary shrink-0">check</span>
                                  {/if}
                                </button>
                              {/if}
                            </div>

                            {#if proxyPools.length === 0}
                              <div class="px-3 py-2 text-xs text-text-muted">
                                <p>No proxy pools available.</p>
                                <a
                                  href="/dashboard/proxypools"
                                  class="mt-1.5 inline-flex items-center gap-1 font-medium text-primary hover:underline"
                                >
                                  <span class="material-symbols-outlined text-sm">add</span>
                                  Create proxy pool
                                </a>
                              </div>
                            {:else}
                              <!-- Filtered Groups -->
                              {@const q = proxySearch.trim().toLowerCase()}
                              {@const filteredGroups = availableProxyGroups.filter((g) => !q || g.name.toLowerCase().includes(q))}
                              {#if filteredGroups.length > 0}
                                <div class="p-1">
                                  <div class="px-2 py-1 text-[10px] font-semibold text-text-muted uppercase tracking-wider">
                                    Proxy Groups
                                  </div>
                                  {#each filteredGroups as grp}
                                    {@const isSelected = assignedPoolId === `group:${grp.name}` || assignedPoolId === grp.name}
                                    <button
                                      type="button"
                                      onclick={() => { assignProxyPool(conn, `group:${grp.name}`); proxySearch = ''; }}
                                      class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {isSelected ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                                    >
                                      <div class="flex items-center gap-2 truncate">
                                        <span class="material-symbols-outlined text-[16px] text-primary">folder</span>
                                        <span class="truncate">{grp.name}</span>
                                      </div>
                                      <div class="flex items-center gap-1.5 shrink-0">
                                        <span class="text-[10px] text-text-muted">({grp.active})</span>
                                        {#if isSelected}
                                          <span class="material-symbols-outlined text-[16px] text-primary">check</span>
                                        {/if}
                                      </div>
                                    </button>
                                  {/each}
                                </div>
                              {/if}

                              <!-- Filtered Types -->
                              {@const filteredTypes = availableProxyTypes.filter((t) => !q || t.type.toLowerCase().includes(q))}
                              {#if filteredTypes.length > 0}
                                <div class="p-1">
                                  <div class="px-2 py-1 text-[10px] font-semibold text-text-muted uppercase tracking-wider">
                                    Proxy Types
                                  </div>
                                  {#each filteredTypes as typ}
                                    {@const isSelected = assignedPoolId === `type:${typ.type}`}
                                    <button
                                      type="button"
                                      onclick={() => { assignProxyPool(conn, `type:${typ.type}`); proxySearch = ''; }}
                                      class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {isSelected ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                                    >
                                      <div class="flex items-center gap-2 truncate">
                                        <span class="material-symbols-outlined text-[16px]">category</span>
                                        <span class="capitalize truncate">{typ.type}</span>
                                      </div>
                                      <div class="flex items-center gap-1.5 shrink-0">
                                        <span class="text-[10px] text-text-muted">({typ.active})</span>
                                        {#if isSelected}
                                          <span class="material-symbols-outlined text-[16px] text-primary">check</span>
                                        {/if}
                                      </div>
                                    </button>
                                  {/each}
                                </div>
                              {/if}

                              <!-- Filtered Individual Pools -->
                              {@const filteredPools = proxyPools.filter((p) => !q || p.name.toLowerCase().includes(q) || (p.group && p.group.toLowerCase().includes(q)))}
                              {#if filteredPools.length > 0}
                                <div class="p-1">
                                  <div class="px-2 py-1 text-[10px] font-semibold text-text-muted uppercase tracking-wider">
                                    Individual Pools
                                  </div>
                                  {#each filteredPools as pool}
                                    {@const isSelected = assignedPoolId === pool.id}
                                    <button
                                      type="button"
                                      onclick={() => { assignProxyPool(conn, pool.id); proxySearch = ''; }}
                                      class="flex w-full items-center justify-between gap-2 px-2.5 py-1.5 text-xs rounded-lg hover:bg-surface-2 transition-colors cursor-pointer {isSelected ? 'bg-primary/10 text-primary font-medium' : 'text-text-main'}"
                                    >
                                      <div class="flex items-center gap-2 truncate">
                                        <span class="truncate">{pool.name}{pool.group ? ` [${pool.group}]` : ''}</span>
                                        {#if !pool.isActive}
                                          <span class="text-[10px] text-text-muted shrink-0">(inactive)</span>
                                        {/if}
                                      </div>
                                      {#if isSelected}
                                        <span class="material-symbols-outlined text-[16px] text-primary shrink-0">check</span>
                                      {/if}
                                    </button>
                                  {/each}
                                </div>
                              {/if}
                            {/if}
                          </div>
                        </div>
                      {/if}
                    </div>
                    <Menu
                      label="Actions for this connection"
                      triggerIcon="more_horiz"
                      hideLabel
                      minWidth="13rem"
                    >
                      {#if isFreebuff}
                        <MenuItem
                          label="Manage session"
                          icon="lock_clock"
                          onSelect={() => selectFreebuffAccount(conn.id)}
                        />
                        <div class="my-1 border-t border-border-subtle" role="separator"></div>
                      {/if}

                      <MenuItem
                        label="Refresh"
                        icon="sync"
                        disabled={isTestingOneByOne || status?.state === 'testing'}
                        onSelect={() => refreshOneConnection(conn)}
                      />
                      {#if isCreditAwareConn(conn)}
                        <MenuItem
                          label={creditLoadingByConn[conn.id]
                            ? 'Loading credits...'
                            : creditInfoByConn[conn.id]
                              ? 'Refresh credits'
                              : 'Show credits'}
                          icon="credit_card"
                          disabled={!!creditLoadingByConn[conn.id]}
                          onSelect={() => fetchConnectionCredits(conn)}
                        />
                      {/if}
                      <MenuItem
                        label="Reset Cooldown / Status"
                        icon="restart_alt"
                        onSelect={() => handleResetConnectionStatus(conn)}
                      />
                      {#if isDeepSeekWeb}
                        <MenuItem
                          label="Renew credential"
                          icon="lock_reset"
                          disabled={renewingConnId === conn.id}
                          onSelect={() => renewDeepSeekCredential(conn)}
                        />
                      {/if}
                      <MenuItem label="Edit" icon="edit" onSelect={() => openEditConnection(conn)} />

                      <div class="my-1 border-t border-border-subtle" role="separator"></div>

                      <MenuItem label="Delete" icon="delete" danger onSelect={() => handleDeleteConnection(conn)} />
                    </Menu>
                  </div>

                  <!-- Active toggle switch -->
                  <div class="flex items-center gap-3">
                    <button
                      type="button"
                      role="switch"
                      aria-label="Toggle connection active"
                      aria-checked={isConnActive}
                      onclick={() => toggleConnectionActive(conn)}
                      class="relative inline-flex shrink-0 cursor-pointer rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-brand-500/30 {isConnActive ? 'bg-brand-500' : 'bg-surface-3'} w-8 h-4"
                    >
                      <span
                        class="pointer-events-none inline-block rounded-full bg-white shadow-sm transform transition duration-200 ease-in-out {isConnActive ? 'translate-x-4' : 'translate-x-0.5'} size-3 mt-0.5"
                      ></span>
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </div>
        {/each}
      </div>

      <!-- Pagination Controls -->
      {#if connTotalPages > 1}
        <div class="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3 text-xs text-text-muted">
          <div class="flex items-center gap-2">
            <span>Showing {(connPage - 1) * connPageSize + 1} to {Math.min(connPage * connPageSize, totalConnectionsCount)} of {totalConnectionsCount} accounts</span>
            <select
              value={connPageSize}
              onchange={(e) => {
                connPageSize = Number((e.target as HTMLSelectElement).value)
                connPage = 1
              }}
              class="rounded border border-border bg-surface-1 px-1.5 py-0.5 text-xs text-text-main"
            >
              <option value={10}>10 / page</option>
              <option value={20}>20 / page</option>
              <option value={50}>50 / page</option>
              <option value={100}>100 / page</option>
            </select>
          </div>
          <div class="flex items-center gap-1">
            <button
              type="button"
              disabled={connPage <= 1}
              onclick={() => connPage--}
              class="inline-flex items-center justify-center rounded border border-border bg-surface-1 px-2.5 py-1 text-xs font-medium text-text-main hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-40"
            >
              Previous
            </button>
            <span class="px-2 py-1 font-medium text-text-main">
              {connPage} / {connTotalPages}
            </span>
            <button
              type="button"
              disabled={connPage >= connTotalPages}
              onclick={() => connPage++}
              class="inline-flex items-center justify-center rounded border border-border bg-surface-1 px-2.5 py-1 text-xs font-medium text-text-main hover:bg-surface-2 disabled:cursor-not-allowed disabled:opacity-40"
            >
              Next
            </button>
          </div>
        </div>
      {/if}
    {/if}

    <!-- Bottom Add Button (upstream: compatible nodes add keys only via the details card) -->
    {#if !isCompatibleNode}
    <div class="mt-4 grid grid-cols-1 gap-2 sm:flex">
      {#if providerId === 'freebuff'}
        <button
          type="button"
          onclick={startFreebuffFlow}
          disabled={isAuthorizingFreebuff}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">vpn_key</span>
          {isAuthorizingFreebuff ? 'Polling Authorization...' : 'Authorize Freebuff CLI'}
        </button>
      {:else if providerId === 'kiro'}
        <button
          type="button"
          onclick={handleAddConnectionClick}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">login</span>
          Connect Kiro
        </button>
      {:else if hasDualAuthModes}
        <button
          type="button"
          onclick={handleAddConnectionClick}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">lock</span>
          {oauthButtonLabel}
        </button>
        <button
          type="button"
          onclick={openAddKeyModal}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">key</span>
          {apiKeyButtonLabel}
        </button>
      {:else}
        <button
          type="button"
          onclick={handleAddConnectionClick}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">add</span>
          Add
        </button>
      {/if}
      {#if providerConnections.length > 0}
        <button
          type="button"
          onclick={() => (showExportKeysModal = true)}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">download</span>
          Export Keys
        </button>
        <button
          type="button"
          onclick={() => (showShareProviderModal = true)}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
        >
          <span class="material-symbols-outlined text-[18px]">link</span>
          Share
        </button>
      {/if}
    </div>
    {/if}
  </div>
<!-- 3. Apply Proxy Modal -->
{#if showApplyProxyModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 fade-in"
      onclick={cancelApplyProxy}
      role="presentation"
    ></div>
    <div class="relative flex max-h-[85vh] w-full flex-col bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-elev)] fade-in max-w-lg">
      <div class="flex shrink-0 items-center justify-between px-6 pt-5 pb-3 border-b border-border-subtle">
        <h2 class="text-lg font-semibold text-text-main">
          Apply Proxy (provider-wide)
        </h2>
        <button
          type="button"
          onclick={cancelApplyProxy}
          disabled={isApplyingProxy}
          aria-label="Close"
          class="p-1 rounded text-text-muted hover:text-text-main disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>

      <!-- The pool list scrolls on its own. With 100+ imported pools an
           unbounded list grew this centred panel past the viewport, which
           clipped the title and every action above the fold. -->
      <div class="min-h-0 flex-1 space-y-2 overflow-y-auto custom-scrollbar px-6 py-4">
        <button
          type="button"
          onclick={handleApplyProxyRotate}
          disabled={isApplyingProxy || activeProxyPools.length === 0}
          class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors disabled:opacity-50 cursor-pointer"
        >
          <span class="material-symbols-outlined text-primary text-lg">sync_alt</span>
          <div>
            <div class="text-xs font-medium text-text-main">One-to-one (rotate)</div>
            <div class="text-[11px] text-text-muted">Distribute active proxy pools round-robin — one custom mark per account</div>
          </div>
        </button>

        <button
          type="button"
          onclick={() => handleApplyProxyPool(null)}
          disabled={isApplyingProxy}
          class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors cursor-pointer"
        >
          <span class="material-symbols-outlined text-red-500 text-lg">link_off</span>
          <div>
            <div class="text-xs font-medium text-text-main">None (clear provider binding)</div>
            <div class="text-[11px] text-text-muted">Accounts without a custom proxy go direct</div>
          </div>
        </button>

        {#if availableProxyGroups.length > 0}
          <div class="pt-2 pb-1 text-[11px] font-semibold text-text-muted uppercase tracking-wider">
            Proxy Groups
          </div>
          {#each availableProxyGroups as grp}
            {@const count = grp.active}
            <button
              type="button"
              onclick={() => handleApplyProxyPool(`group:${grp.name}`)}
              disabled={isApplyingProxy || count === 0}
              class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-primary/30 bg-primary/5 hover:bg-primary/10 transition-colors disabled:opacity-50 cursor-pointer"
            >
              <span class="material-symbols-outlined text-primary text-lg">folder_special</span>
              <div class="min-w-0 flex-1">
                <div class="text-xs font-medium text-text-main flex items-center justify-between gap-2">
                  <span>Group: {grp.name}</span>
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-primary/20 text-primary font-medium">{count} active pools</span>
                </div>
                <div class="text-[11px] text-text-muted">Dynamic smart scoring + failover within group</div>
              </div>
            </button>
          {/each}
        {/if}

        {#if availableProxyTypes.length > 0}
          <div class="pt-2 pb-1 text-[11px] font-semibold text-text-muted uppercase tracking-wider">
            Proxy Types
          </div>
          {#each availableProxyTypes as typ}
            {@const count = typ.active}
            <button
              type="button"
              onclick={() => handleApplyProxyPool(`type:${typ.type}`)}
              disabled={isApplyingProxy || count === 0}
              class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors disabled:opacity-50 cursor-pointer"
            >
              <span class="material-symbols-outlined text-text-muted text-lg">category</span>
              <div class="min-w-0 flex-1">
                <div class="text-xs font-medium text-text-main flex items-center justify-between gap-2">
                  <span class="capitalize">Type: {typ.type}</span>
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-surface-3 text-text-muted">{count} active pools</span>
                </div>
                <div class="text-[11px] text-text-muted">Dynamic smart scoring across all {typ.type} proxies</div>
              </div>
            </button>
          {/each}
        {/if}

        <div class="pt-2 pb-1 text-[11px] font-semibold text-text-muted uppercase tracking-wider">
          Individual Pools
        </div>

        <input
          type="search"
          bind:value={proxyPoolSearch}
          placeholder="Filter {proxyPools.length} pools by name or URL"
          disabled={isApplyingProxy}
          class="w-full rounded-lg border border-border bg-surface-2 px-3 py-2 text-xs text-text-main placeholder:text-text-muted focus:border-primary focus:outline-none disabled:opacity-50"
        />

        {#each filteredProxyPools as pool}
          <button
            type="button"
            onclick={() => handleApplyProxyPool(pool.id)}
            disabled={isApplyingProxy || !pool.isActive}
            class="flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left border border-border hover:bg-surface-2 transition-colors disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
          >
            <span class="material-symbols-outlined text-text-muted text-lg">lan</span>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <div class="text-xs font-medium text-text-main truncate">{pool.name}</div>
                {#if !pool.isActive}
                  <span class="text-[10px] text-text-muted">(inactive)</span>
                {/if}
              </div>
              <div class="text-[11px] text-text-muted truncate">{pool.proxyUrl}</div>
            </div>
          </button>
        {:else}
          <p class="py-6 text-center text-xs text-text-muted">No proxy pool matches “{proxyPoolSearch}”.</p>
        {/each}
      </div>

      <div class="flex shrink-0 items-center justify-between gap-3 px-6 py-4 border-t border-border-subtle">
        <p class="text-xs text-text-muted" aria-live="polite">
          {#if isApplyingProxy}
            Applying {applyProxyDone} / {providerConnections.length}…
          {:else}
            {filteredProxyPools.length} of {proxyPools.length} pools
          {/if}
        </p>
        <button
          type="button"
          onclick={cancelApplyProxy}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          {isApplyingProxy ? 'Stop' : 'Cancel'}
        </button>
      </div>
    </div>
  </div>
{/if}
<!-- 4. Edit Connection Modal -->
{#if editingConnection}
  <EditConnectionModal
    connection={editingConnection}
    onClose={() => (editingConnection = null)}
    onSave={saveEditingConnection}
    onSaveError={(message) => alert(message)}
  />
{/if}
<!-- 5a. Per-provider header overrides (upstream CustomConfigCard parity) -->
<ProviderHeaderOverridesModal
  isOpen={showHeaderOverrides}
  providerId={providerId}
  onClose={() => (showHeaderOverrides = false)}
/>

<!-- 6. Add Key Connection Modal (for non-oauth providers) -->
<AddConnectionModal
  isOpen={showAddKeyModal}
  providerId={providerId}
  providerName={providerName}
  isCompatible={!!providerNode}
  isAnthropic={providerNode?.apiType === 'responses'}
  existingNames={connections.map((c) => c.name).filter((n): n is string => !!n)}
  {proxyPools}
  error={addConnectionError}
  onClose={() => {
    addConnectionError = ''
    showAddKeyModal = false
  }}
  onSubmit={handleAddKeyConnection}
  onBulkDone={onRefresh}
/>
<BulkImportJwtModal
  isOpen={showBulkJwtModal}
  providerId={providerId}
  providerName={providerName}
  onClose={() => (showBulkJwtModal = false)}
  onImported={onRefresh}
/>
<ExportKeysModal
  isOpen={showExportKeysModal}
  providerId={providerId}
  providerName={providerName}
  onClose={() => (showExportKeysModal = false)}
/>
<ShareProviderModal
  isOpen={showShareProviderModal}
  providerId={providerId}
  providerName={providerName}
  isCompatibleNode={isCompatibleNode}
  node={providerNode}
  connections={providerConnections}
  availableProviders={shareableProviders}
  onClose={() => (showShareProviderModal = false)}
/>
