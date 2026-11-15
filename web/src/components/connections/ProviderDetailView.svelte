<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import {
    api,
    type FreebuffSessionStatusResponse,
    type ProviderConnection,
    type ProviderNode,
    type ModelCaps,
    type ProxyPool,
    type Settings
  } from '../../api/client'
  import { PROVIDER_CATALOG, PROVIDER_CATALOG_MAP, type ProviderCatalogItem } from '../../lib/providers'
  import {
    clearCallback,
    CODEX_REDIRECT_URI,
    clearPending,
    dashboardCallbackURL,
    loadPendings,
    matchPending,
    OAUTH_CALLBACK_KEY,
    OAUTH_CHANNEL,
    readCallback,
    savePending,
    writeCallback,
  } from '../../lib/oauth-handoff'
  import { getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
  import { copyToClipboard } from '../../lib/clipboard'
  import { parseModelIds } from '../../lib/bulk-models'
  import {
    importCustomModelsBestEffort,
    notifyCustomModelsChanged,
    type CustomModelImportOutcome,
    type CustomModelWrite,
  } from '../../lib/customModels'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import { loadDeprecations, refreshDeprecations, subscribeDeprecations, deprecationFor } from '../../lib/modelDeprecations.svelte'
  import DeprecatedBadge from '../../lib/ui/DeprecatedBadge.svelte'
  import {
    buildAvailableModels,
    fetchProviderModelsData,
    fetchSuggestedModels,
    getProviderStats,
    isChatModel,
    type CustomModelData,
    type ModelItem,
    type SuggestedModel
  } from './types'
  import { type ConnectionHealth } from './connectionHealth'
  import ProviderAccountStatusStrip from './ProviderAccountStatusStrip.svelte'
  import ProviderConnectionsSection from './ProviderConnectionsSection.svelte'
  import NoAuthProxyCard from '../media/NoAuthProxyCard.svelte'
  import {
    formatCheckAllSummary,
    getBlockedModelIds,
    parseModelTestVerdict,
    type CheckAllSummary,
    type ModelTestStatus,
  } from './modelCheckAll'
  import {
    applyModelListFilter,
    EMPTY_MODEL_LIST_FILTER,
    selectedVisibleIds,
    selectionState,
    toggleSelectAll as toggleSelectAllModels,
    type ModelListFilter,
    type ModelRowView,
  } from './modelListFilter'
  import ModelSearchBar from './ModelSearchBar.svelte'
  import ModelFilterChips from './ModelFilterChips.svelte'
  import ModelBatchBar from './ModelBatchBar.svelte'
  import { normalizeProxyTarget } from '../../lib/proxyBindings'
  import AddCustomModelModal from './AddCustomModelModal.svelte'
  import { nodeLimitsDraftOf, nodeLimitsSummary, nodeLimitsUsageSummary } from '../../lib/node-limits'
  import type { ProviderNodeLimitsPatch, ErrorPolicy } from '../../api/client'
  import Badge from '../../lib/ui/Badge.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import { PauseCircle } from 'lucide-svelte'
  import EditCompatibleNodeModal from './EditCompatibleNodeModal.svelte'
  import FreebuffSessionBanner from './FreebuffSessionBanner.svelte'
  import ProviderIcon from './ProviderIcon.svelte'

  interface Props {
    providerId: string
    connections?: ProviderConnection[]
    providerStats?: Record<string, Record<string, any>>
    providerNodes?: ProviderNode[]
    onBack: () => void
    onRefresh: () => void
    /** Called with the new id after a URL-suffix rename moves this node. */
    onSelectProvider?: (id: string) => void
  }

  let {
    providerId,
    connections = [],
    providerStats = {},
    providerNodes = [],
    onBack,
    onRefresh,
    onSelectProvider
  }: Props = $props()

  // Catalog & Node resolution
  let selectedCatalogItem = $derived<ProviderCatalogItem | undefined>(
    PROVIDER_CATALOG.find((p) => p.id === providerId)
  )
  let selectedNode = $derived<ProviderNode | undefined>(
    providerNodes.find((n) => n.id === providerId)
  )
  let providerName = $derived(selectedNode?.name || selectedCatalogItem?.name || providerId)
  let providerColor = $derived(selectedCatalogItem?.color || '#06b6d4')
  let providerWebsite = $derived(
    selectedCatalogItem?.notice?.apiKeyUrl ||
      selectedCatalogItem?.notice?.signupUrl ||
      selectedCatalogItem?.website ||
      (providerId === 'antigravity' ? 'https://antigravity.google' : '')
  )
  // Upstream parity: "Get API Key" when the notice links straight to a key
  // page, otherwise "Sign up / Learn more".
  let providerWebsiteLabel = $derived(
    selectedCatalogItem?.notice?.apiKeyUrl ? 'Get API Key' : 'Sign up / Learn more'
  )
  let providerNoticeText = $derived(selectedCatalogItem?.notice?.text || '')
  let providerNoticeApiKeyUrl = $derived(selectedCatalogItem?.notice?.apiKeyUrl || '')
  let isOAuth = $derived(selectedCatalogItem?.category === 'oauth')
  let isClineOAuth = $derived(providerId === 'cline' || providerId === 'clinepass')
  let isPKCEOAuth = $derived(providerId === 'claude' || providerId === 'codex' || providerId === 'xai' || providerId === 'gitlab')
  let isAuthCodeOAuth = $derived(providerId === 'gemini-cli' || providerId === 'iflow')
  let isCustomOAuth = $derived(providerId === 'trae' || providerId === 'windsurf' || providerId === 'zed')
  let isDeviceOAuth = $derived(
    providerId === 'qoder' || providerId === 'kilocode' || providerId === 'grok-cli' ||
    providerId === 'github' || providerId === 'kiro' || providerId === 'kimi' ||
    providerId === 'kimi-coding' || providerId === 'codebuddy-cn' || providerId === 'codebuddy-intl' ||
    providerId === 'workbuddy' ||
    providerId === 'minimax-code' || providerId === 'minimax-code-global' || providerId === 'muse' ||
    providerId === 'nous' || providerId === 'glm' ||
    providerId === 'zcode'
  )
  // Upstream parity: only the explicit noAuth flag hides the Connections card
  // ([id]/page.js isFreeNoAuth = !!FREE_PROVIDERS[id]?.noAuth). Category "free"
  // is NOT equivalent — kiro/gemini-cli are free with noAuth:false and still
  // need their Connect/OAuth + API Key buttons.
  let isNoAuth = $derived(selectedCatalogItem?.noAuth === true)
  let hasRiskNotice = $derived(providerId === 'antigravity' || Boolean(selectedCatalogItem?.notice?.text?.includes('RISK_NOTICE')))


  onMount(() => {
    return () => {
      if (freebuffPollTimer) {
        clearInterval(freebuffPollTimer)
        freebuffPollTimer = null
      }
    }
  })

  // Storage alias
  let storageAlias = $derived(
    selectedNode?.id || selectedCatalogItem?.alias || PROVIDER_ID_TO_ALIAS[providerId] || providerId
  )

  // Cached stats from providerStats (O(1) lookup)
  let cachedStats = $derived(
    getProviderStats([], providerId, undefined, providerStats)
  )

  // The roster the Connections card renders, synced from
  // ProviderConnectionsSection through bind: — the header status strip,
  // the all-connections toggle below and the models section read these
  // copies; the card's internal deriveds stay the single source.
  let providerConnections = $state<ProviderConnection[]>([])
  let paginatedConnections = $state<ProviderConnection[]>([])
  let connectionHealthById = $state<Map<string, ConnectionHealth>>(new Map())
  let totalConnectionsCount = $state(0)


  let isProviderAllDisabled = $derived(
    cachedStats.total > 0
      ? cachedStats.allDisabled
      : providerConnections.length > 0 && providerConnections.every((c) => c.isActive === 0)
  )
  let isProviderActive = $derived(
    cachedStats.total > 0
      ? cachedStats.connected > 0
      : providerConnections.some((c) => c.isActive === 1)
  )

  async function handleToggleProvider(targetActive: boolean) {
    try {
      await Promise.allSettled(
        providerConnections.map((c) => api.updateConnection(c.id, { isActive: targetActive ? 1 : 0 }))
      )
      onRefresh()
    } catch (err) {
      alert(`Toggle provider failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  // Upstream parity: compatible-node detection drives the details card,
  // bottom Add button, models section, and edit-node modal.
  let isOpenAICompatibleNode = $derived(!!selectedNode && selectedNode.type === 'openai-compatible')
  let isAnthropicCompatibleNode = $derived(!!selectedNode && selectedNode.type === 'anthropic-compatible')
  let isCompatibleNode = $derived(isOpenAICompatibleNode || isAnthropicCompatibleNode)
  let isResponsesNode = $derived(selectedNode?.apiType === 'responses')

  // Models state
  let customModels = $state<CustomModelData[]>([])
  let disabledModelIds = $state<string[]>([])
  let builtInModels = $derived(getModelsByProviderId(providerId).filter(isChatModel))
  let providerCustomModels = $derived(
    customModels.filter(
      (m) => (m.providerAlias === storageAlias || m.providerAlias === providerId) && isChatModel(m)
    )
  )
  let allAvailableModels = $derived<ModelItem[]>(
    buildAvailableModels(builtInModels, providerCustomModels)
  )
  // Registry order, like upstream: `models` there is getModelsByProviderId()
  // verbatim (providers/[id]/page.js:158) with custom models appended after the
  // catalog, so the rows read in the same sequence on both dashboards.
  let visibleModels = $derived(allAvailableModels.filter((m) => !disabledModelIds.includes(m.id)))
  // ── Model list toolbar state (search / filter / batch selection) ──
  // One filter object drives both lists below: the registered-provider grid
  // and the compatible-node list. Selection is id-based so it survives a
  // filter change; it is cleared on provider switch with the rest of the
  // model state (see the reset block below).
  let compatibleRows = $derived.by(() => {
    const rows: Array<{ id: string; source: 'custom' | 'legacyAlias'; alias?: string; addedAt?: number }> = []
    const seen = new Set<string>()
    for (const cm of customModels) {
      if (!cm.id || cm.providerAlias !== storageAlias) continue
      if ((cm.type || 'llm') !== 'llm') continue
      const full = `${storageAlias}/${cm.id}`
      if (seen.has(full)) continue
      seen.add(full)
      // The kv store carries no per-row timestamp, so the add date is the
      // position the row arrived in: the API returns the scope's keys in
      // insertion order, and the newest write is last. A later row with the
      // same key keeps its original position (the upsert does not move it),
      // which is what "when was this first registered" means.
      rows.push({ id: cm.id, source: 'custom', addedAt: customModels.length - customModels.indexOf(cm) })
    }
    const prefix = `${storageAlias}/`
    for (const [alias, full] of Object.entries(modelAliases || {})) {
      if (typeof full !== 'string' || !full.startsWith(prefix)) continue
      const id = full.slice(prefix.length)
      if (!id || seen.has(full)) continue
      seen.add(full)
      rows.push({ id, source: 'legacyAlias', alias })
    }
    // Display-only A–Z sort (case-insensitive) so the list order is stable
    // instead of following the API response object's key order, which varies
    // between requests. rows is created above on every evaluation, so the
    // in-place sort is safe.
    return rows.sort((a, b) => a.id.localeCompare(b.id, undefined, { sensitivity: 'base' }))
  })
  let modelListFilter = $state<ModelListFilter>({ ...EMPTY_MODEL_LIST_FILTER })
  let selectedModelIds = $state<Set<string>>(new Set())

  // Both lists as the toolbar's row shape. The registered grid maps
  // registry + custom models; the compatible list maps custom + legacy
  // alias rows.
  //
  // Caps come from GET /api/models/caps, which resolves BOTH branches: the
  // static registry catalog for a registered provider, and the custom rows
  // a compatible node carries (model_caps.go customModelCaps — a node with
  // only imported models has no static catalog, so that path publishes the
  // saved flags and the capability registry's own resolution). The row's
  // static caps are the fallback for a model the endpoint has not answered
  // yet, so the chips never read all-false on a model that does have them.
  let registeredRows = $derived<ModelRowView[]>(
    allAvailableModels.map((m) => {
      const caps = modelCaps[m.id] ?? m.caps
      return {
        id: m.id,
        name: m.name,
        source: m.isCustom ? 'custom' : 'registry',
        caps: {
          vision: !!caps?.vision,
          reasoning: !!caps?.reasoning,
          search: !!caps?.search,
          free: !!caps?.free,
          jailbreak: !!caps?.jailbreak,
        },
        thinkingLevels: modelCaps[m.id]?.thinkingLevels ?? [],
        deprecated: deprecationFor(providerId, m.id) !== null,
        disabled: disabledModelIds.includes(m.id),
        testStatus: modelTestStatuses[m.id],
        latencyMs: modelTestLatency[m.id],
      }
    })
  )

  let compatibleRowViews = $derived<ModelRowView[]>(
    compatibleRows.map((r) => {
      // A custom row's saved flags arrive through the same caps endpoint;
      // a legacy alias row has no saved block, so the capability registry's
      // own id-based resolution is the only source left.
      const caps = modelCaps[r.id]
      return {
        id: r.id,
        source: r.source,
        caps: {
          vision: !!caps?.vision,
          reasoning: !!caps?.reasoning,
          search: !!caps?.search,
          free: !!caps?.free,
          jailbreak: !!caps?.jailbreak,
        },
        thinkingLevels: caps?.thinkingLevels ?? [],
        deprecated: deprecationFor(providerId, r.id) !== null,
        // The disabledModels KV scope is keyed by the node's storage alias,
        // which for a compatible node is the node id — the same key the
        // registered branch uses. The gate itself already hides these rows
        // from /v1/models (see models_list.go appendConnectionModels); this
        // flag is what makes them selectable and filterable in the UI.
        disabled: disabledModelIds.includes(r.id),
        testStatus: compatibleTestResults[r.id],
        latencyMs: compatibleTestLatency[r.id],
        addedAt: r.addedAt,
      }
    })
  )

  // The list the toolbar filters and both grids render. Registered providers
  // and compatible nodes now share the same rule: the full row set is
  // filtered, and a disabled row stays visible only while the operator is
  // asking to see disabled or unavailable ones.
  let toolbarRows = $derived<ModelRowView[]>(isCompatibleNode ? compatibleRowViews : registeredRows)
  let filteredRows = $derived(applyModelListFilter(toolbarRows, modelListFilter))
  // The grid iterates in the toolbar's order — a Set would silently fall
  // back to registry order and the A–Z / Z–A pick would do nothing. The
  // grid keeps its own membership rule (visible = not disabled) unless the
  // operator asked to see the unusable ones, and still iterates ModelItem
  // because its template reads isCustom and the static caps.
  let gridModels = $derived<ModelItem[]>(
    filteredRows
      .map((r) => allAvailableModels.find((m) => m.id === r.id))
      .filter(
        (m): m is ModelItem =>
          !!m &&
          (modelListFilter.status.disabled ||
            modelListFilter.status.unavailable ||
            !disabledModelIds.includes(m.id))
      )
  )
  let currentSelection = $derived(selectionState(selectedModelIds, filteredRows))
  // Which capabilities the current rows actually carry, so the chips can
  // hide the ones that would empty the list. Computed from toolbarRows —
  // the unfiltered set — so turning a chip on never makes its siblings
  // disappear mid-interaction.
  let availableCaps = $derived({
    thinking: toolbarRows.some((r) => r.caps.reasoning || r.thinkingLevels.length > 0),
    vision: toolbarRows.some((r) => r.caps.vision),
    search: toolbarRows.some((r) => r.caps.search),
    free: toolbarRows.some((r) => r.caps.free),
    jailbreak: toolbarRows.some((r) => r.caps.jailbreak),
  })
  let selectedRowsHaveCustom = $derived(
    toolbarRows.some((r) => selectedModelIds.has(r.id) && r.source !== 'registry')
  )
  // Suggested free models from the provider's public catalog (upstream parity).
  let suggestedModels = $state<SuggestedModel[]>([])
  let suggestedNotAdded = $derived(
    suggestedModels.filter(
      (m) =>
        !builtInModels.some((b) => b.id === m.id) &&
        !providerCustomModels.some((c) => c.id === m.id)
    )
  )
  let allDisabled = $derived(
    allAvailableModels.length > 0 && disabledModelIds.length >= allAvailableModels.length
  )
  let isSyncingModels = $state(false)
  let modelDeprecationCount = $state(0)

  // Capabilities + thinking levels are resolved server-side (GET /api/models/caps):
  // the catalog ships as a static bundle, but caps depend on the provider
  // registry, the capability tables and the synced models.dev catalog.
  let modelCaps = $state<Record<string, ModelCaps>>({})
  // Union of the thinking levels this provider's models accept, with the
  // explicit "auto" reset first — upstream providerThinkingLevels
  // (dashboard/providers/[id]/page.js:186). null hides the picker entirely for a
  // provider whose models have no reasoning.
  let providerThinkingLevels = $derived.by(() => {
    const levels = new Set<string>()
    for (const m of allAvailableModels) {
      for (const l of modelCaps[m.id]?.thinkingLevels ?? []) {
        if (l !== 'none') levels.add(l)
      }
    }
    return levels.size ? ['auto', ...levels] : null
  })
  // A picked level only applies to a model that actually supports it, so the
  // displayed id and the copied id never carry a level the provider rejects.
  function resolveThinkingSuffix(modelId: string): string | null {
    if (!thinkingLevel || thinkingLevel === 'auto') return null
    const levels = modelCaps[modelId]?.thinkingLevels
    return levels && levels.includes(thinkingLevel) ? thinkingLevel : null
  }
  // Compatible nodes (upstream CompatibleModelsSection): rows = custom models
  // + legacy aliases, both keyed by the node row id; display = node prefix.
  let modelAliases = $state<Record<string, string>>({})
  let newCompatibleModel = $state('')
  let isAddingCompatibleModel = $state(false)
  let isImportingCompatibleModels = $state(false)
  let isImportingLiveCatalogModels = $state(false)
  let compatibleTesting = $state<Record<string, boolean>>({})
  let compatibleTestResults = $state<Record<string, ModelTestStatus>>({})
  let compatibleTestErrors = $state<Record<string, string | null>>({})
  let compatibleTestLatency = $state<Record<string, number>>({})
  let compatibleTestAllRunning = $state(false)
  let isRemovingCompatibleModels = $state(false)
  let canImportCompatible = $derived(providerConnections.some((c) => c.isActive !== 0))

  // Settings & Strategies
  let settings = $state<Settings | null>(null)
  let isRoundRobin = $state(false)
  let stickyLimit = $state('1')
  let thinkingLevel = $state('auto')
  let lockedProxyGroup = $state('')
  // A provider binding to ONE pool: the strategy's proxyPoolId holds a
  // bare pool id (the form the Apply Proxy modal's individual-pool choice
  // writes). It is not a selector, so the lock label and the row badges
  // read it from its own state instead of the group/type derivation.
  let lockedProxyPoolId = $state('')

  // Proxy Pools
  let proxyPools = $state<ProxyPool[]>([])

  let copiedModelId = $state<string | null>(null)
  let modelTestStatuses = $state<Record<string, ModelTestStatus>>({})
  let modelTestErrors = $state<Record<string, string | null>>({})
  // Round-trip time of each probe, in ms. Absent means "never measured" —
  // which the latency sort reads as "put this row last", so it must stay
  // absent rather than default to 0.
  let modelTestLatency = $state<Record<string, number | undefined>>({})
  let activeModelTestError = $state<string | null>(null)
  let checkAllSummary = $state<CheckAllSummary | null>(null)

  // Modals state
  let showRiskNoticeModal = $state(false)
  let showOAuthModal = $state(false)
  let oauthAuthUrl = $state('')
  let copiedAuthUrl = $state(false)
  let callbackInput = $state('')
  let pasteNote = $state('')
  let oauthError = $state<string | null>(null)
  let isConnecting = $state(false)
  let clineCodeVerifier = $state('')
  let clineRedirectUri = $state('')
  // Antigravity (fragment 982): the PKCE verifier stays server-side; the
  // modal only keeps the state the authorize step minted.
  let antigravityState = $state('')
  // Antigravity verification step (fragment 1061): when the test that
  // follows a completed exchange finds the account verification-gated,
  // the modal stays open on the fresh verification URL that test just
  // minted (never stored — it is only worth clicking while this
  // browser's sign-in session is warm). Check re-runs the test; a pass
  // closes the modal, a still-gated verdict replaces the URL with the
  // newer one from that test.
  let pendingVerification = $state<{ connectionId: string; url: string } | null>(null)
  let copiedVerifyUrl = $state(false)
  let isCheckingVerification = $state(false)
  let pkceCodeVerifier = $state('')
  let pkceState = $state('')
  let pkceRedirectUri = $state('')
  let gitlabBaseUrl = $state('')
  let gitlabClientId = $state('')
  let gitlabClientSecret = $state('')
  let customState = $state('')
  let customVerifier = $state('')
  let customSystemId = $state('')
  let deviceUserCode = $state('')
  let deviceCode = $state('')
  let deviceSession: Record<string, unknown> = $state({})
  let deviceInterval = $state(5)
  let devicePollTimer: ReturnType<typeof setInterval> | null = $state(null)
  // Kiro method selection (upstream KiroAuthModal/KiroOAuthWrapper parity):
  // builder-id | idc | api-key | import | import-cli-proxy | social-google | social-github
  let kiroMethod = $state<string | null>(null)
  let kiroIdcStartUrl = $state('')
  let kiroIdcRegion = $state('us-east-1')
  let kiroApiKey = $state('')
  let kiroApiKeyRegion = $state('us-east-1')
  let kiroRefreshToken = $state('')
  let kiroCliProxyJson = $state('')
  let kiroSocialAuthUrl = $state('')
  let kiroSocialCallback = $state('')
  // OAuth auto-handoff: callback tab writes to storage + BroadcastChannel,
  // this modal restores the pending session and auto-submits.
  let autoSubmitted = $state(false)
  // Codex completes its login on a server-owned loopback listener, so the
  // modal watches the server instead of the browser callback page.
  let codexPollTimer: ReturnType<typeof setInterval> | null = $state(null)

  function dashboardCallback(): string {
    return dashboardCallbackURL(dashboardOrigin())
  }

  function rememberPending(p: { state: string; verifier?: string; redirectUri?: string; extra?: Record<string, string> }) {
    if (typeof window === 'undefined') return
    try {
      savePending(window.localStorage, { provider: providerId, ...p })
    } catch {
      /* storage blocked — degrade to manual paste */
    }
  }

  function consumeOAuthCallback(): boolean {
    if (typeof window === 'undefined' || autoSubmitted || isConnecting || !showOAuthModal) return false
    // Antigravity never returns through the dashboard /callback page
    // (fragment 982): Google displays the code on its own page and the
    // user pastes it into the modal, so the auto-handoff skips it.
    if (providerId === 'antigravity') return false
    let cb
    try {
      cb = readCallback(window.localStorage)
    } catch {
      return false
    }
    if (!cb || (!cb.raw && !cb.error)) return false
    if (cb.error) {
      oauthError = `Login failed: ${cb.error}${cb.errorDesc ? ` — ${cb.errorDesc}` : ''}`
      try {
        clearCallback(window.localStorage)
      } catch {
        /* noop */
      }
      return true
    }
    let pendings
    try {
      pendings = loadPendings(window.localStorage)
    } catch {
      return false
    }
    const pending = matchPending(pendings, providerId, cb.state)
    if (!pending || pending.provider !== providerId) return false
    // Restore session values saved at authorize time.
    if (pending.verifier) {
      if (isClineOAuth) clineCodeVerifier = pending.verifier
      else if (isPKCEOAuth) pkceCodeVerifier = pending.verifier
      else customVerifier = pending.verifier
    }
    if (pending.redirectUri) {
      if (isClineOAuth) clineRedirectUri = pending.redirectUri
      else pkceRedirectUri = pending.redirectUri
    }
    const extra = pending.extra || {}
    if (extra.state) {
      if (isCustomOAuth) customState = extra.state
      else pkceState = extra.state
    }
    if (providerId === 'gitlab') {
      if (extra.baseUrl) gitlabBaseUrl = extra.baseUrl
      if (extra.clientId) gitlabClientId = extra.clientId
      if (extra.clientSecret) gitlabClientSecret = extra.clientSecret
    }
    if (extra.systemId) customSystemId = extra.systemId
    callbackInput = cb.raw
    try {
      clearPending(window.localStorage, pending.provider, pending.state)
      clearCallback(window.localStorage)
    } catch {
      /* noop */
    }
    autoSubmitted = true
    void submitManualCallback()
    return true
  }

  // Listen for the callback tab while the OAuth modal is open.
  $effect(() => {
    if (!showOAuthModal || typeof window === 'undefined') return
    autoSubmitted = false
    consumeOAuthCallback()
    const onStorage = (e: StorageEvent) => {
      if (e.key === OAUTH_CALLBACK_KEY || e.key === null) consumeOAuthCallback()
    }
    window.addEventListener('storage', onStorage)
    const onWindowMessage = (e: MessageEvent) => {
      const trustedOrigin = e.origin === window.location.origin
      if (!trustedOrigin) return
      if (e.data?.type === 'oauth_callback' && e.data?.data) {
        const data = e.data.data as {
          code?: string
          token?: string
          state?: string
          error?: string
          errorDescription?: string
        }
        if (data.error) {
          oauthError = `Login failed: ${data.error}${data.errorDescription ? ` — ${data.errorDescription}` : ''}`
          return
        }
        const raw = data.code || data.token || ''
        if (raw) {
          try {
            writeCallback(window.localStorage, { state: data.state || '', raw, error: '', errorDesc: '' })
          } catch {
            /* storage blocked */
          }
          consumeOAuthCallback()
        }
        return
      }
    }
    window.addEventListener('message', onWindowMessage)
    let bc: BroadcastChannel | null = null
    try {
      bc = new BroadcastChannel(OAUTH_CHANNEL)
      bc.onmessage = () => {
        consumeOAuthCallback()
      }
    } catch {
      /* BroadcastChannel unavailable — poll covers it */
    }
    const timer = setInterval(consumeOAuthCallback, 1500)
    return () => {
      window.removeEventListener('storage', onStorage)
      window.removeEventListener('message', onWindowMessage)
      try {
        bc?.close()
      } catch {
        /* noop */
      }
      clearInterval(timer)
    }
  })
  let isSpecialOAuth = $derived(
    providerId === 'cursor' || providerId === 'kimchi' || providerId === 'xiaomi-mimo'
  )
  let specialToken = $state('')
  let specialExtra = $state('')
  let specialBaseUrl = $state('')


  let showAddKeyModal = $state(false)
  let addConnectionError = $state('')
  let showAddCustomModelModal = $state(false)
  let showEditNodeModal = $state(false)
  let showExportKeysModal = $state(false)
  let showShareProviderModal = $state(false)
  // Freebuff specific state & session tracking
  let isFreebuff = $derived(
    providerId === 'freebuff' || storageAlias === 'fb' || storageAlias === 'freebuff'
  )
  // The session API has to be asked about an account that can actually serve.
  // Connections are ordered by priority, and a disabled one can sort first —
  // its rejected credential reports `banned`/`unauthorized` and blanks the
  // panel while the live seat sits on the next account.
  let selectedFreebuffConnId = $state<string>('')
  let freebuffSessions = $state<Record<string, FreebuffSessionStatusResponse>>({})
  let targetFreebuffConn = $derived.by(() => {
    if (selectedFreebuffConnId) {
      const found = providerConnections.find((c) => c.id === selectedFreebuffConnId)
      if (found) return found
    }
    return providerConnections.find((c) => c.isActive === 1) ?? providerConnections[0]
  })
  let freebuffSession = $state<FreebuffSessionStatusResponse | null>(null)
  let isLoadingSession = $state(false)
  let isAuthorizingFreebuff = $state(false)
  let freebuffPollTimer = $state<ReturnType<typeof setInterval> | null>(null)
  let currentFreebuffInit = $state<{
    fingerprintId: string
    fingerprintHash: string
    expiresAt: number | string
    loginUrl: string
  } | null>(null)

  async function loadFreebuffSession() {
    if (!isFreebuff || providerConnections.length === 0) {
      freebuffSession = null
      return
    }
    const target = targetFreebuffConn
    if (!target) {
      freebuffSession = null
      return
    }
    isLoadingSession = true
    try {
      const res = await api.getFreebuffSessionStatus(target.id)
      freebuffSession = res
      if (res && target.id) {
        freebuffSessions = { ...freebuffSessions, [target.id]: res }
      }
      for (const c of providerConnections) {
        if (c.id !== target.id) {
          api.getFreebuffSessionStatus(c.id).then((st) => {
            if (st) {
              freebuffSessions = { ...freebuffSessions, [c.id]: st }
            }
          }).catch(() => {})
        }
      }
    } catch (err) {
      console.error('Failed to fetch Freebuff session status:', err)
      freebuffSession = null
    } finally {
      isLoadingSession = false
    }
  }

  function selectFreebuffAccount(connId: string) {
    selectedFreebuffConnId = connId
    loadFreebuffSession()
  }

  let sessionExpiresInMin = $derived.by(() => {
    if (!freebuffSession?.expiresAt) return null
    const exp = new Date(freebuffSession.expiresAt).getTime()
    const diffMs = exp - Date.now()
    return Math.max(0, Math.round(diffMs / 60000))
  })

  function checkIsActiveSession(modelId: string): boolean {
    if (!isFreebuff || freebuffSession?.status !== 'active' || !freebuffSession?.currentModel) {
      return false
    }
    const cur = freebuffSession.currentModel.toLowerCase().trim()
    const mid = modelId.toLowerCase().trim()
    return mid === cur || mid.endsWith('/' + cur) || cur.endsWith('/' + mid)
  }

  // Ends the active Freebuff session and re-admits it on the chosen model.
  // Freebuff serves one model per session and a session lives an hour even when
  // idle, so this is the only way to change models without waiting it out —
  // and it must stay an explicit user action (each switch spends a session).
  async function switchFreebuffModel(model: string) {
    const target = targetFreebuffConn
    const res = await api.switchFreebuffSession(model, target?.id)
    await loadFreebuffSession()
    onRefresh()
    return res
  }

  async function startFreebuffFlow() {
    try {
      isAuthorizingFreebuff = true
      oauthError = null
      callbackInput = ''
      copiedAuthUrl = false
      const init = await api.initiateFreebuff()
      currentFreebuffInit = {
        fingerprintId: init.fingerprintId,
        fingerprintHash: init.fingerprintHash,
        expiresAt: init.expiresAt,
        loginUrl: init.loginUrl
      }
      oauthAuthUrl = init.loginUrl
      showOAuthModal = true

      if (typeof window !== 'undefined' && init.loginUrl) {
        window.open(init.loginUrl, '_blank')
      }

      if (freebuffPollTimer) clearInterval(freebuffPollTimer)
      freebuffPollTimer = setInterval(async () => {
        try {
          if (!showOAuthModal) {
            if (freebuffPollTimer) {
              clearInterval(freebuffPollTimer)
              freebuffPollTimer = null
            }
            isAuthorizingFreebuff = false
            return
          }
          const res = await api.pollFreebuff(init.fingerprintId, init.fingerprintHash, init.expiresAt)
          if (res?.status === 'authorized') {
            if (freebuffPollTimer) {
              clearInterval(freebuffPollTimer)
              freebuffPollTimer = null
            }
            isAuthorizingFreebuff = false
            showOAuthModal = false
            onRefresh()
            loadFreebuffSession()
          }
        } catch {}
      }, 2500)
    } catch (err) {
      isAuthorizingFreebuff = false
      alert(`Failed to start Freebuff flow: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Load models, settings, proxy pools
  async function loadData() {
    try {
      const [modelsData, settingsData, poolsData, aliasesData, capsData] = await Promise.all([
        fetchProviderModelsData(providerId, storageAlias),
        api.getSettings().catch((): Settings => ({})),
        api.getProxyPools().catch(() => []),
        api.getModelAliases().catch(() => ({ aliases: {} })),
        // The caps endpoint answers for compatible nodes too: a node with
        // only imported models has no static catalog, so the handler falls
        // back to the custom rows' saved flags (model_caps.go). Skipping
        // the call here left every capability chip reading all-false on a
        // custom provider, which is the one place an operator needs them.
        //
        // The second call is for the custom rows: they are stored under the
        // provider's alias ("ds"), which the handler resolves separately
        // from the provider id ("deepseek") — and a row saved under the
        // alias is absent from the id's map. Both are merged below, the
        // alias answer winning only where the id's map has nothing, so a
        // static catalog entry is never overwritten by a custom row.
        PROVIDER_CATALOG_MAP.has(providerId) || PROVIDER_CATALOG_MAP.has(storageAlias) || isCompatibleNode
          ? api.getModelCaps(providerId).catch(() => ({ caps: {} as Record<string, ModelCaps> }))
          : Promise.resolve({ caps: {} as Record<string, ModelCaps> }),
      ])
      const capsById = capsData?.caps || {}
      // Same guard as the call above: an alias equal to the id would spend a
      // second round trip on a map we already have.
      let capsMerged = capsById
      if (storageAlias && storageAlias !== providerId) {
        const aliasCaps = await api
          .getModelCaps(storageAlias)
          .catch(() => ({ caps: {} as Record<string, ModelCaps> }))
        const aliasMap = aliasCaps?.caps || {}
        for (const [id, entry] of Object.entries(aliasMap)) {
          if (!capsMerged[id]) capsMerged[id] = entry
        }
      }
      customModels = modelsData.customModels
      disabledModelIds = modelsData.disabledModelIds
      modelAliases = aliasesData?.aliases || {}
      settings = settingsData
      proxyPools = poolsData
      modelCaps = capsMerged


      // Suggested free models from the provider's public catalog (if configured in upstream registry).
      const fetcher = selectedCatalogItem?.modelsFetcher
      if (fetcher?.url && fetcher?.type) {
        fetchSuggestedModels(fetcher).then((list) => {
          suggestedModels = list
        })
      } else {
        suggestedModels = []
      }

      // Extract Round Robin strategy
      const strategy = settingsData?.providerStrategies?.[providerId]
      isRoundRobin = strategy?.fallbackStrategy === 'round-robin'
      stickyLimit = String(strategy?.stickyRoundRobinLimit ?? 1)
      // normalizeProxyTarget, never a hand-rolled `group:${...}`: a
      // proxyGroup stored already-prefixed ("group:ISP-BD") used to come
      // back as "group:group:ISP-BD", a selector matching no pool, so
      // every badge under the lock painted red while traffic flowed.
      lockedProxyGroup =
        (strategy?.proxyPoolId?.startsWith('group:') || strategy?.proxyPoolId?.startsWith('type:') ? strategy.proxyPoolId : '') ||
        (strategy?.proxyGroup ? normalizeProxyTarget(strategy.proxyGroup) : '') ||
        ''
      // A bare proxyPoolId (no group:/type: prefix, no comma list) is a
      // binding to ONE pool: the resolver returns it verbatim, so the UI
      // surfaces it as a pool binding, never as a group selector.
      const rawStrategyPoolId = strategy?.proxyPoolId
      lockedProxyPoolId =
        rawStrategyPoolId &&
        !rawStrategyPoolId.startsWith('group:') &&
        !rawStrategyPoolId.startsWith('type:') &&
        !rawStrategyPoolId.includes(',') &&
        rawStrategyPoolId !== '__none__' &&
        rawStrategyPoolId !== 'direct'
          ? rawStrategyPoolId
          : ''

      // Extract Thinking Level
      const thinking = (settingsData as any)?.providerThinking?.[providerId]
      thinkingLevel = thinking?.mode || 'auto'
      if (isFreebuff) {
        loadFreebuffSession()
      }
    } catch (err) {
      console.error('Failed to load provider details:', err)
    }
  }


  let lastLoadedProviderId = ''
  $effect(() => {
    const pid = providerId
    if (pid) {
      untrack(() => {
        if (lastLoadedProviderId !== pid) {
          lastLoadedProviderId = pid
          suggestedModels = []
        }
        loadData()
      })
    }
  })

  // Strategy saves

  async function handleThinkingChange(event: Event) {
    const val = (event.target as HTMLSelectElement).value
    thinkingLevel = val
    try {
      const current = (settings as any)?.providerThinking || {}
      const updated = { ...current }
      if (val && val !== 'auto') {
        updated[providerId] = { mode: val }
      } else {
        delete updated[providerId]
      }
      if (settings) (settings as any).providerThinking = updated
      await api.updateSettings({ providerThinking: updated })
    } catch (err) {
      console.error('Error saving provider thinking:', err)
    }
  }


  function copyVerifyUrl() {
    if (!pendingVerification) return
    copyToClipboard(pendingVerification.url)
    copiedVerifyUrl = true
    setTimeout(() => (copiedVerifyUrl = false), 2000)
  }

  // The modal's Check: re-run the connection test for the account the
  // exchange just created/re-connected. A pass closes the modal; a
  // still-gated verdict swaps in the URL the new test minted.
  async function checkPendingVerification() {
    if (!pendingVerification || isCheckingVerification) return
    isCheckingVerification = true
    oauthError = null
    try {
      const res = await api.testConnection(pendingVerification.connectionId)
      if (res?.valid) {
        pendingVerification = null
        showOAuthModal = false
        onRefresh()
      } else if (res?.verificationUrl) {
        pendingVerification = { connectionId: pendingVerification.connectionId, url: res.verificationUrl }
        copiedVerifyUrl = false
      } else if (res?.error) {
        oauthError = res.error
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isCheckingVerification = false
    }
  }



  // Add Connection Button flow
  function openAddKeyModal() {
    addConnectionError = ''
    showAddKeyModal = true
  }

  async function handleAddConnectionClick() {
    if (providerId === 'antigravity') {
      const confirmed = typeof window !== 'undefined' && localStorage.getItem('ag_risk_confirmed') === 'true'
      if (!confirmed) {
        showRiskNoticeModal = true
      } else {
        openAntigravityOAuth()
      }
    } else if (providerId === 'freebuff') {
      startFreebuffFlow()
    } else if (isClineOAuth) {
      openClineOAuth()
    } else if (isPKCEOAuth) {
      openPKCEOAuth()
    } else if (isAuthCodeOAuth) {
      openAuthCodeOAuth()
    } else if (isCustomOAuth) {
      openCustomOAuth()
    } else if (providerId === 'kiro') {
      openKiroOAuth()
    } else if (isDeviceOAuth) {
      // Device-code providers (qoder, grok-cli, github, kilocode, kimi,
      // codebuddy-*) have no authorize endpoint to redirect to: the backend
      // mints a PKCE/nonce pair locally and hands back a verification URL to
      // open, then polls for the token. Without this branch they fell through
      // to openGenericOAuth and the user was asked to paste a token by hand
      // instead of getting the login page — isDeviceOAuth was computed but
      // never read, and openDeviceOAuth was never called.
      openDeviceOAuth()
    } else if (isSpecialOAuth) {
      openSpecialOAuth()
    } else if (isOAuth) {
      openGenericOAuth()
    } else {
      showAddKeyModal = true
    }
  }

  function confirmRiskAndProceed() {
    if (typeof window !== 'undefined') {
      localStorage.setItem('ag_risk_confirmed', 'true')
    }
    showRiskNoticeModal = false
    openAntigravityOAuth()
  }

  async function openAntigravityOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    antigravityState = ''
    pendingVerification = null
    copiedVerifyUrl = false
    try {
      // agy-style flow (fragment 982): Google's own page shows the code;
      // the user pastes it into the modal. Only the state is kept here —
      // the PKCE verifier never leaves the server.
      const res = await api.getAntigravityAuthorizeUrl()
      oauthAuthUrl = res.authUrl || res.url || res.redirectUrl
      antigravityState = res.state || ''
      if (res.state) {
        rememberPending({ state: res.state })
      }
      // No auto-open (Ricki, 2026-11-07): the modal presents the login
      // URL with a copy affordance and an explicit "Open login page"
      // button — the new tab opens only on that manual click.
      showOAuthModal = true
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function openPKCEOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      // Codex cannot use the dashboard callback: OpenAI only accepts the
      // redirect URI registered for the Codex CLI client.
      const cb = providerId === 'codex' ? CODEX_REDIRECT_URI : dashboardCallback()
      const res = await api.pkceAuthorize(
        providerId,
        providerId === 'gitlab'
          ? {
              redirectUri: cb,
              baseUrl: gitlabBaseUrl.trim() || undefined,
              clientId: gitlabClientId.trim() || undefined,
            }
          : { redirectUri: cb }
      )
      oauthAuthUrl = res.url || res.authUrl
      pkceCodeVerifier = res.codeVerifier || ''
      pkceState = res.state || ''
      pkceRedirectUri = res.redirectUri || cb
      if (providerId === 'codex') {
        await startCodexLoopback()
      }
      rememberPending({
        state: pkceState,
        verifier: pkceCodeVerifier,
        redirectUri: pkceRedirectUri,
        extra: {
          state: pkceState,
          baseUrl: gitlabBaseUrl.trim(),
          clientId: gitlabClientId.trim(),
          clientSecret: gitlabClientSecret.trim(),
        },
      })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Codex redirects the browser to a fixed loopback port, so the server needs
  // that listener running before the popup opens — otherwise the callback dies
  // on a closed port and the login can never complete.
  async function startCodexLoopback() {
    stopCodexPoll()
    const proxy = await api.codexStartProxy({
      appPort: window.location.port || (window.location.protocol === 'https:' ? '443' : '80'),
      state: pkceState,
      codeVerifier: pkceCodeVerifier,
      redirectUri: CODEX_REDIRECT_URI,
    })
    if (!proxy.success) {
      throw new Error(
        proxy.reason === 'port_busy'
          ? 'Port 1455 is in use; close the conflicting process and retry'
          : 'Could not start the local login listener',
      )
    }
    codexPollTimer = setInterval(pollCodexStatus, 1500)
  }

  async function pollCodexStatus() {
    try {
      const res = await api.codexPollStatus(pkceState)
      if (res.status === 'done') {
        stopCodexPoll()
        showOAuthModal = false
        onRefresh()
      } else if (res.status === 'error') {
        stopCodexPoll()
        oauthError = res.error || 'Authorization failed'
      } else if (res.status === 'unknown') {
        // The server no longer tracks this login. Stop polling, but say so:
        // silently freezing the modal on "Waiting for popup authorization…"
        // leaves the user with a login that can never finish and no way to
        // tell that apart from simply being slow.
        stopCodexPoll()
        oauthError = 'The local login listener is no longer running. Close this window and click Login again.'
      }
    } catch {
      // Biarkan polling berikutnya mencoba lagi.
    }
  }

  function stopCodexPoll() {
    if (codexPollTimer) {
      clearInterval(codexPollTimer)
      codexPollTimer = null
    }
  }

  // Dismissing the modal abandons the login, so any loopback listener and
  // poll loop it started have to be released with it.
  function closeOAuthModal() {
    showOAuthModal = false
    stopDevicePoll()
    stopCodexPoll()
    if (providerId === 'codex') {
      void api.codexStopProxy().catch(() => {})
    }
  }

  async function openAuthCodeOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const res = await api.authcodeAuthorize(providerId, dashboardCallback())
      oauthAuthUrl = res.url || res.authUrl
      pkceState = res.state || ''
      pkceRedirectUri = res.redirectUri || ''
      pkceCodeVerifier = ''
      rememberPending({ state: pkceState, redirectUri: pkceRedirectUri, extra: { state: pkceState } })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function openCustomOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const res = await api.customAuthorize(providerId as 'trae' | 'windsurf' | 'zed', dashboardCallback())
      oauthAuthUrl = res.url || res.authUrl
      customState = res.state || res.loginTraceId || ''
      customVerifier = res.codeVerifier || ''
      customSystemId = res.systemId || ''
      pkceRedirectUri = res.redirectUri || ''
      rememberPending({
        state: customState,
        verifier: customVerifier,
        redirectUri: pkceRedirectUri,
        extra: { state: customState, systemId: customSystemId },
      })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
  function resetKiroMethod() {
    kiroMethod = null
    kiroIdcStartUrl = ''
    kiroIdcRegion = 'us-east-1'
    kiroApiKey = ''
    kiroApiKeyRegion = 'us-east-1'
    kiroRefreshToken = ''
    kiroCliProxyJson = ''
    kiroSocialAuthUrl = ''
    kiroSocialCallback = ''
  }

  async function openKiroOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    stopDevicePoll()
    resetKiroMethod()
    showOAuthModal = true
  }

  async function startKiroDeviceFlow(authMethod: 'builder-id' | 'idc') {
    oauthError = null
    copiedAuthUrl = false
    stopDevicePoll()
    isConnecting = true
    try {
      const res = await api.deviceStart(
        'kiro',
        authMethod === 'idc'
          ? { startUrl: kiroIdcStartUrl.trim(), region: kiroIdcRegion.trim() || 'us-east-1', authMethod: 'idc' }
          : { authMethod: 'builder-id' }
      )
      oauthAuthUrl = res.verification_uri_complete || res.verification_uri || ''
      deviceUserCode = res.user_code || ''
      deviceCode = res.device_code || ''
      deviceSession = res.session || {}
      deviceInterval = res.interval && res.interval > 0 ? res.interval : 5
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank')
      }
      pollDeviceOnce()
      devicePollTimer = setInterval(pollDeviceOnce, deviceInterval * 1000)
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroApiKey() {
    if (!kiroApiKey.trim()) {
      oauthError = 'Enter your Kiro/CodeWhisperer API key'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroApiKey(kiroApiKey.trim(), kiroApiKeyRegion.trim() || 'us-east-1')
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroImport() {
    if (!kiroRefreshToken.trim()) {
      oauthError = 'Enter the refresh token from Kiro IDE'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroImport(kiroRefreshToken.trim())
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroCliProxyImport() {
    if (!kiroCliProxyJson.trim()) {
      oauthError = 'Paste CLIProxyAPI auth JSON'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroImportCliProxy(kiroCliProxyJson.trim())
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function startKiroAutoImport() {
    oauthError = null
    isConnecting = true
    try {
      const res = await api.kiroAutoImport()
      if (res?.found && res?.refreshToken) {
        kiroRefreshToken = res.refreshToken as string
      } else {
        oauthError = (res as { error?: string })?.error || 'No token detected on this host'
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function startKiroSocial(provider: 'google' | 'github') {
    oauthError = null
    kiroSocialAuthUrl = ''
    kiroSocialCallback = ''
    isConnecting = true
    try {
      const res = await api.kiroSocialAuthorize(provider)
      kiroSocialAuthUrl = res.authUrl || res.url || ''
      if (typeof window !== 'undefined' && kiroSocialAuthUrl) {
        window.open(kiroSocialAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  async function submitKiroSocialCallback() {
    const raw = kiroSocialCallback.trim()
    if (!raw) {
      oauthError = 'Paste the callback URL / code from the browser'
      return
    }
    // Accept a full kiro:// callback URL or a bare code (upstream
    // KiroSocialOAuthModal parity: code + state query params).
    let code = raw
    try {
      const url = new URL(raw)
      const errParam = url.searchParams.get('error')
      if (errParam) {
        oauthError = url.searchParams.get('error_description') || errParam
        return
      }
      code = url.searchParams.get('code') || raw
    } catch {
      // Not a URL — treat the input as a bare code.
    }
    if (!code) {
      oauthError = 'No authorization code found in the callback URL'
      return
    }
    oauthError = null
    isConnecting = true
    try {
      await api.kiroSocialExchange(code)
      showOAuthModal = false
      resetKiroMethod()
      onRefresh()
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }


  let isPollingDevice = $state(false)

  async function openDeviceOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    stopDevicePoll()
    try {
      const res = await api.deviceStart(providerId)
      oauthAuthUrl = res.verification_uri_complete || res.verification_uri || ''
      deviceUserCode = res.user_code || ''
      deviceCode = res.device_code || ''
      deviceSession = res.session || {}
      deviceInterval = res.interval && res.interval > 0 ? res.interval : 5
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank')
      }
      devicePollTimer = setInterval(pollDeviceOnce, deviceInterval * 1000)
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
      showOAuthModal = true
    }
  }

  async function pollDeviceOnce() {
    if (!showOAuthModal || !deviceCode || isPollingDevice) return
    isPollingDevice = true
    try {
      const res = await api.devicePoll(providerId, deviceCode, deviceSession)
      if (res?.interval && res.interval > deviceInterval) {
        deviceInterval = res.interval
        if (devicePollTimer) {
          clearInterval(devicePollTimer)
          devicePollTimer = setInterval(pollDeviceOnce, deviceInterval * 1000)
        }
      }
      if (res?.status === 'authorized') {
        stopDevicePoll()
        showOAuthModal = false
        onRefresh()
      } else if (res?.status === 'error') {
        stopDevicePoll()
        oauthError = res?.error || 'Authorization failed'
      }
    } catch {
      // Biarkan polling berikutnya mencoba lagi.
    } finally {
      isPollingDevice = false
    }
  }

  function stopDevicePoll() {
    if (devicePollTimer) {
      clearInterval(devicePollTimer)
      devicePollTimer = null
    }
    deviceUserCode = ''
    deviceCode = ''
  }

  function openSpecialOAuth() {
    // cursor (import/auto-import), kimchi (browser token), xiaomi-mimo (ECDH).
    // gitlab PAT & iflow cookie reuse the generic modal + special submit.
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    specialToken = ''
    specialExtra = ''
    specialBaseUrl = ''
    customVerifier = ''
    oauthAuthUrl = ''
    if (providerId === 'kimchi' || providerId === 'xiaomi-mimo') {
      openSpecialAuthorize()
      return
    }
    showOAuthModal = true
  }

  async function openSpecialAuthorize() {
    try {
      const cb = dashboardCallback()
      const res = providerId === 'kimchi'
        ? await api.kimchiAuthorize(cb)
        : await api.mimoAuthorize(cb)
      oauthAuthUrl = res.url || res.authUrl
      customVerifier = (res as { codeVerifier?: string }).codeVerifier || ''
      rememberPending({
        state: (res as { state?: string }).state || '',
        verifier: customVerifier || undefined,
      })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank')
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
      showOAuthModal = true
    }
  }

  async function cursorAutoImportNow() {
    oauthError = null
    isConnecting = true
    try {
      const res = await api.cursorAutoImport()
      if (!res || (res as { error?: string })?.error || !(res as { success?: boolean })?.success) {
        oauthError = (res as { error?: string })?.error || 'Auto-import failed. Make sure Cursor IDE is installed and signed in on this host.'
      } else {
        showOAuthModal = false
        onRefresh()
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  function dashboardOrigin(): string {
    if (typeof window !== 'undefined' && window.location?.origin) return window.location.origin
    return 'http://localhost:20130'
  }

  function openGenericOAuth() {
    // No generic authorize endpoint: never redirect to Google/Antigravity.
    // Show the modal with manual-token import instructions instead.
    oauthError = null
    oauthAuthUrl = ''
    callbackInput = ''
    copiedAuthUrl = false
    clineCodeVerifier = ''
    clineRedirectUri = ''
    showOAuthModal = true
  }

  async function openClineOAuth() {
    oauthError = null
    callbackInput = ''
    copiedAuthUrl = false
    try {
      const res = await api.getClineAuthorizeUrl(providerId, dashboardCallback())
      oauthAuthUrl = res.url || res.authUrl
      clineCodeVerifier = res.codeVerifier || ''
      clineRedirectUri = res.redirectUri || ''
      rememberPending({ state: res.state || '', verifier: clineCodeVerifier, redirectUri: clineRedirectUri })
      showOAuthModal = true
      if (typeof window !== 'undefined' && oauthAuthUrl) {
        window.open(oauthAuthUrl, '_blank', 'width=600,height=700')
      }
    } catch (err) {
      alert(`Failed to initiate authorization: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  function copyAuthUrl() {
    if (!oauthAuthUrl) return
    copyToClipboard(oauthAuthUrl)
    copiedAuthUrl = true
    setTimeout(() => (copiedAuthUrl = false), 2000)
  }

  // Step 2's Paste button: pull the code straight from the clipboard
  // instead of making the operator long-press the field. A clipboard
  // read needs a secure context and a granted permission, so a refusal
  // degrades to a hint under the field rather than an error.
  async function pasteCallbackCode() {
    pasteNote = ''
    try {
      const text = await navigator.clipboard.readText()
      if (text && text.trim()) {
        callbackInput = text.trim()
      } else {
        pasteNote = 'Clipboard is empty — copy the code from Google first.'
      }
    } catch {
      pasteNote = 'Clipboard read is blocked in this browser — long-press the field and choose Paste.'
    }
  }

  async function submitManualCallback() {
    const raw = callbackInput.trim()
    isConnecting = true
    oauthError = null
    try {
      if (providerId === 'freebuff') {
        // Direct manual token input
        if (raw && !raw.includes('http') && !raw.includes('auth_code=') && raw.length >= 24 && !raw.includes('/')) {
          await api.createConnection({
            provider: 'freebuff',
            authType: 'oauth',
            name: 'Freebuff (manual)',
            apiKey: raw,
          })
          if (freebuffPollTimer) {
            clearInterval(freebuffPollTimer)
            freebuffPollTimer = null
          }
          isAuthorizingFreebuff = false
          showOAuthModal = false
          onRefresh()
          loadFreebuffSession()
          return
        }

        // Immediate poll verification
        if (currentFreebuffInit) {
          const res = await api.pollFreebuff(
            currentFreebuffInit.fingerprintId,
            currentFreebuffInit.fingerprintHash,
            currentFreebuffInit.expiresAt
          )
          if (res?.status === 'authorized') {
            if (freebuffPollTimer) {
              clearInterval(freebuffPollTimer)
              freebuffPollTimer = null
            }
            isAuthorizingFreebuff = false
            showOAuthModal = false
            onRefresh()
            loadFreebuffSession()
            return
          } else if (res?.status === 'pending') {
            oauthError = 'Status is still pending. If the Freebuff tab opened on the /onboard page, finish the onboarding step in that tab, then click Check & Connect again.'
          } else if (res?.status === 'expired') {
            oauthError = 'The authorization session has expired. Close this modal and click Authorize Freebuff CLI again.'
          } else {
            oauthError = `Status: ${res?.status || 'pending'}. Make sure the browser login is complete.`
          }
        } else {
          oauthError = 'The Freebuff session has not been started yet. Click Authorize Freebuff CLI again.'
        }
        return
      }

      if (!raw) return
      let code = raw.trim()
      let redirectUri: string | undefined
      if (code.includes('code=')) {
        try {
          const u = new URL(code)
          code = u.searchParams.get('code') || code
          redirectUri = `${u.origin}${u.pathname}`
        } catch {
          const match = code.match(/code=([^&]+)/)
          if (match) code = decodeURIComponent(match[1])
        }
      }
      while (code.includes('%')) {
        try {
          const decoded = decodeURIComponent(code)
          if (decoded === code) break
          code = decoded
        } catch {
          break
        }
      }
      if (isClineOAuth) {
        if (!clineCodeVerifier) {
          oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
          return
        }
        try {
          const res = await api.clineExchange(providerId, code, clineCodeVerifier, redirectUri || clineRedirectUri || undefined)
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isPKCEOAuth) {
        if (!pkceCodeVerifier) {
          oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
          return
        }
        try {
          const res = await api.pkceExchange({
            provider: providerId,
            code,
            codeVerifier: pkceCodeVerifier,
            redirectUri: redirectUri || pkceRedirectUri || undefined,
            state: pkceState || undefined,
            baseUrl: providerId === 'gitlab' ? gitlabBaseUrl.trim() || undefined : undefined,
            clientId: providerId === 'gitlab' ? gitlabClientId.trim() || undefined : undefined,
            clientSecret: providerId === 'gitlab' ? gitlabClientSecret.trim() || undefined : undefined,
          })
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isAuthCodeOAuth) {
        try {
          const res = await api.authcodeExchange({
            provider: providerId,
            code,
            redirectUri: redirectUri || pkceRedirectUri || undefined,
            state: pkceState || undefined,
          })
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isCustomOAuth) {
        if (!raw) return
        try {
          const res = await api.customExchange(providerId as 'trae' | 'windsurf' | 'zed', {
            code: raw,
            state: customState || undefined,
            codeVerifier: customVerifier || undefined,
            systemId: customSystemId || undefined,
          })
          if (!res || (res as { error?: string })?.error || (res as { status?: string })?.status === 'error') {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (isSpecialOAuth) {
        const tok = (specialToken || callbackInput).trim()
        if (!tok && providerId !== 'cursor') {
          oauthError = 'Tempel token / callback terlebih dahulu.'
          return
        }
        try {
          let res: { success?: boolean; status?: string; error?: string } | null = null
          if (providerId === 'cursor') {
            if (!tok || !specialExtra.trim()) {
              oauthError = 'Enter the access token and machine ID.'
              return
            }
            res = await api.cursorImport(tok, specialExtra.trim())
          } else if (providerId === 'kimchi') {
            res = await api.kimchiExchange(tok)
          } else if (providerId === 'xiaomi-mimo') {
            if (!customVerifier) {
              oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
              return
            }
            res = await api.mimoExchange(tok, customVerifier)
          }
          if (!res || res?.error || (res.status && res.status === 'error')) {
            oauthError = res?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (providerId === 'gitlab' && specialToken.trim()) {
        // GitLab PAT mode (disamping OAuth PKCE).
        try {
          const res = await api.gitlabPAT(specialToken.trim(), specialBaseUrl.trim() || undefined)
          if (!res || (res as { error?: string })?.error || !(res as { success?: boolean })?.success) {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (providerId === 'iflow' && specialToken.trim()) {
        // iFlow cookie mode (disamping OAuth authcode).
        try {
          const res = await api.iflowCookie(specialToken.trim())
          if (!res || (res as { error?: string })?.error || !(res as { success?: boolean })?.success) {
            oauthError = (res as { error?: string })?.error || 'Authorization failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      if (providerId !== 'antigravity') {
        if (raw.includes('code=') || raw.includes('http')) {
          oauthError = `Direct OAuth login is not supported for ${providerName}. Paste an access token / refresh token instead.`
          return
        }
        try {
          const res = await api.importOAuthToken(providerId, raw)
          if (!res || (res as { error?: string })?.error) {
            oauthError = (res as { error?: string })?.error || 'Token import failed'
          } else {
            showOAuthModal = false
            onRefresh()
          }
        } catch (err) {
          oauthError = err instanceof Error ? err.message : String(err)
        }
        return
      }
      let pending = null
      try {
        pending = matchPending(loadPendings(window.localStorage), providerId, '')
      } catch {
        pending = null
      }
      const state = antigravityState || pending?.state || ''
      if (!state) {
        oauthError = 'The authorization session has not been started yet. Close this modal and click Log in again.'
        return
      }
      const res = await api.antigravityExchange(code, state)
      if (res?.success === false || (res as { error?: string })?.error) {
        oauthError = (res as { error?: string })?.error || 'Authorization failed'
      } else {
        // Decide serve-ability right here, while this browser's
        // sign-in session is still warm (fragment 1061): the test
        // test-calls the chat API, and a verification-gated verdict
        // keeps the modal open on the fresh verification URL that
        // test minted — the only moment that URL is worth clicking.
        const connId = res?.connection?.id || ''
        let gated = false
        if (connId) {
          try {
            const t = await api.testConnection(connId)
            if (t?.verificationUrl) {
              pendingVerification = { connectionId: connId, url: t.verificationUrl }
              copiedVerifyUrl = false
              gated = true
            }
          } catch {
            // A failed test leaves the row to speak for itself.
          }
        }
        if (!gated) {
          showOAuthModal = false
        }
        onRefresh()
      }
    } catch (err) {
      oauthError = err instanceof Error ? err.message : String(err)
    } finally {
      isConnecting = false
    }
  }

  // Model actions
  function copyModelId(modelId: string) {
    const level = resolveThinkingSuffix(modelId)
    const full = `${storageAlias}/${modelId}${level ? `(${level})` : ''}`
    copyToClipboard(full)
    copiedModelId = modelId
    setTimeout(() => (copiedModelId = null), 2000)
  }

  async function testModel(modelId: string, silent = false) {
    // Probes take seconds; if the panel switches provider mid-flight the
    // verdict belongs to the old provider and must not be written here.
    const pid = providerId
    modelTestStatuses[modelId] = 'testing'
    modelTestErrors[modelId] = null
    if (!silent) activeModelTestError = null
    try {
      const res = await api.testModel(`${storageAlias}/${modelId}`)
      if (pid !== providerId) return
      const verdict = parseModelTestVerdict(res)
      modelTestStatuses[modelId] = verdict.status
      modelTestErrors[modelId] = verdict.error
      // The round trip is what the latency sort orders by, so it is kept
      // next to the verdict it came from. A failed probe still reports a
      // latency (the request completed, the model refused), and that number
      // is worth having; a thrown request has none, so the previous
      // measurement is cleared rather than left stale.
      modelTestLatency[modelId] = res.latencyMs
      if (!silent && verdict.error) {
        activeModelTestError = `${modelId}: ${verdict.error}`
      }
    } catch (err) {
      if (pid !== providerId) return
      modelTestStatuses[modelId] = 'error'
      modelTestLatency[modelId] = undefined
      const msg = err instanceof Error ? err.message : 'Model test failed'
      modelTestErrors[modelId] = msg
      if (!silent) activeModelTestError = `${modelId}: ${msg}`
    }
  }

  async function handleDisableModel(modelId: string) {
    const updated = Array.from(new Set([...disabledModelIds, modelId]))
    disabledModelIds = updated
    try {
      await api.saveDisabledModels(storageAlias, updated)
    } catch (err) {
      console.error('Failed to disable model:', err)
    }
  }

  async function handleEnableModel(modelId: string) {
    const updated = disabledModelIds.filter((id) => id !== modelId)
    disabledModelIds = updated
    try {
      await api.saveDisabledModels(storageAlias, updated)
    } catch (err) {
      console.error('Failed to enable model:', err)
    }
  }

  async function handleToggleAllModels() {
    if (allDisabled) {
      disabledModelIds = []
      try {
        await api.saveDisabledModels(storageAlias, [])
      } catch (err) {
        console.error('Failed to enable all models:', err)
      }
    } else {
      if (!confirm(`Disable all ${allAvailableModels.length} model(s)?`)) return
      const updated = allAvailableModels.map((m) => m.id)
      disabledModelIds = updated
      try {
        await api.saveDisabledModels(storageAlias, updated)
      } catch (err) {
        console.error('Failed to disable all models:', err)
      }
    }
  }

  // Custom Model & Node handlers
  async function submitAddCustomModel(
    modelId: string,
    caps?: { vision?: boolean; reasoning?: boolean; jailbreak?: boolean },
    limits?: { contextWindow?: number; maxOutput?: number },
  ) {
    try {
      await api.saveCustomModel(`${storageAlias}|${modelId}|llm`, {
        id: modelId,
        providerAlias: storageAlias,
        type: 'llm',
        ...(caps ? { caps } : {}),
        ...(limits?.contextWindow ? { contextWindow: limits.contextWindow } : {}),
        ...(limits?.maxOutput ? { maxOutput: limits.maxOutput } : {}),
      })
      showAddCustomModelModal = false
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      notifyCustomModelsChanged()
    } catch (err) {
      alert(`Failed to add custom model: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  /**
   * A bulk paste writes rows the panel's own list has to show, so the reload is
   * the same one a single add does — the modal cannot do it, because the model
   * list belongs to this view.
   */
  async function handleBulkModelsSaved() {
    try {
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      notifyCustomModelsChanged()
    } catch (err) {
      alert(`Models were saved but the list could not be reloaded: ${err instanceof Error ? err.message : String(err)}`)
    }
  }


  async function handleDeleteProviderNode(nodeId: string) {
    if (!confirm('Delete custom provider endpoint and all attached credentials?')) return
    try {
      await api.deleteProviderNode(nodeId)
      onBack()
      onRefresh()
    } catch (err) {
      alert(`Delete failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Upstream parity: PUT /api/provider-nodes/[id] then refresh the node list.
  //
  // A changed URL suffix renames the provider id, and this page is routed by
  // that id — so a rename has to navigate to the new one or the user lands on a
  // node that no longer exists.
  async function handleSaveEditedNode(data: {
    name: string
    prefix: string
    apiType?: string
    baseUrl: string
    urlSuffix: string
    limits: ProviderNodeLimitsPatch
    errorPolicy?: ErrorPolicy
  }) {
    if (!selectedNode) return
    const previousId = selectedNode.id
    try {
      const res = await api.updateProviderNode(previousId, {
        name: data.name,
        prefix: data.prefix,
        ...(selectedNode.type === 'openai-compatible' && data.apiType ? { apiType: data.apiType } : {}),
        baseUrl: data.baseUrl,
        urlSuffix: data.urlSuffix || undefined,
        errorPolicy: data.errorPolicy,
      })
      // The id the server answered with, which differs from the one we sent when the
      // URL suffix changed. The policy is written against that id: the limits columns
      // live on the node's own row, so an edited suffix moves them, and a write aimed
      // at the old id would leave the node looking unlimited.
      const savedId = res?.node?.id || previousId
      try {
        const saved = await api.setProviderNodeLimits(savedId, data.limits)
        selectedNode = { ...selectedNode, id: savedId, limits: saved.limits }
      } catch (err) {
        alert(
          `Provider node saved, but its capacity limits did not: ${err instanceof Error ? err.message : String(err)}`,
        )
      }
      showEditNodeModal = false
      if (savedId !== previousId) {
        onSelectProvider?.(savedId)
      }
      onRefresh()
    } catch (err) {
      alert(`Save failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Compatible nodes: upstream CompatibleModelsSection handlers.
  function displayAlias(): string {
    return selectedNode?.prefix || providerId
  }

  async function refreshCompatibleModels() {
    try {
      const [modelsData, aliasesData] = await Promise.all([
        fetchProviderModelsData(providerId, storageAlias),
        api.getModelAliases().catch(() => ({ aliases: {} })),
      ])
      customModels = modelsData.customModels
      modelAliases = aliasesData?.aliases || {}
    } catch (err) {
      console.error('Failed to refresh compatible models:', err)
    }
  }

  // The Model ID field above is where an operator actually pastes a provider's model
  // list, so it takes one id or a whole list. Splitting happens on the same separators
  // the Add Model dialog uses (spaces, commas, semicolons, newlines), because "paste
  // three ids and get one model whose id contains the spaces" is what this field did
  // before — a row nothing can address without reproducing them exactly.
  let newCompatibleModelsPlan = $derived(parseModelIds(newCompatibleModel, storageAlias))

  async function handleAddCompatibleModel() {
    const plan = parseModelIds(newCompatibleModel.trim(), storageAlias)
    if (plan.ids.length === 0 || isAddingCompatibleModel) return
    if (plan.ids.length === 1) {
      await addOneCompatibleModel(plan.ids[0])
      return
    }
    isAddingCompatibleModel = true
    try {
      // One request for the list, so a refused line cannot stop the ones after it, and
      // the response says which lines landed instead of leaving it to the refresh.
      const out = await api.saveCustomModelsBulk({
        providerAlias: storageAlias,
        models: plan.ids.map((id) => ({ id, type: 'llm' })),
      })
      newCompatibleModel = ''
      await refreshCompatibleModels()
      notifyCustomModelsChanged()
      if (out.skipped.length > 0) {
        alert(
          `Added ${out.added.length} of ${plan.ids.length}.\n\nNot added:\n` +
            out.skipped.map((skip) => `${skip.id} — ${skip.reason}`).join('\n'),
        )
      }
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to add models')
      console.error('Error adding models:', err)
    } finally {
      isAddingCompatibleModel = false
    }
  }

  async function addOneCompatibleModel(modelId: string) {
    if (compatibleRows.some((r) => r.id === modelId)) {
      alert('Model already exists for this provider.')
      return
    }
    isAddingCompatibleModel = true
    try {
      await api.saveCustomModel(`${storageAlias}|${modelId}|llm`, {
        id: modelId,
        providerAlias: storageAlias,
        type: 'llm',
      })
      newCompatibleModel = ''
      await refreshCompatibleModels()
      notifyCustomModelsChanged()
    } catch (err) {
      // A refusal (the id cannot be an address, or it already means something else) has
      // to reach the user, like the other import paths on this page.
      alert(err instanceof Error ? err.message : 'Failed to add model')
      console.error('Error adding model:', err)
    } finally {
      isAddingCompatibleModel = false
    }
  }

  async function handleDeleteCompatibleModel(row: { id: string; source: string; alias?: string }) {
    try {
      if (row.source === 'custom') {
        await api.deleteCustomModel(`${storageAlias}|${row.id}|llm`)
      } else if (row.source === 'legacyAlias' && row.alias) {
        await api.deleteModelAlias(row.alias)
      }
      await refreshCompatibleModels()
      notifyCustomModelsChanged()
    } catch (err) {
      console.error('Error deleting model:', err)
    }
  }

  async function handleTestCompatibleModel(modelId: string) {
    if (compatibleTesting[modelId]) return
    compatibleTesting[modelId] = true
    compatibleTestErrors[modelId] = null
    try {
      const res = await api.testModel(`${storageAlias}/${modelId}`)
      // Kept absent when the server reports nothing, so the latency sort
      // treats it as unmeasured rather than as the fastest model on the list.
      if (res.latencyMs !== undefined) compatibleTestLatency[modelId] = res.latencyMs
      const verdict = parseModelTestVerdict(res)
      compatibleTestResults[modelId] = verdict.status
      compatibleTestErrors[modelId] = verdict.error
    } catch (err) {
      compatibleTestResults[modelId] = 'error'
      compatibleTestErrors[modelId] = err instanceof Error ? err.message : 'Model test failed'
    } finally {
      compatibleTesting[modelId] = false
    }
  }

  // Bulk removal for a node whose list came from an import. The single-row
  // delete beside each model is the same operation repeated, so this only adds
  // the one-request path; the rows it removes are exactly the ones the import
  // wrote (custom models + the aliases pointing at them), never the catalog.
  async function handleRemoveAllCompatibleModels() {
    if (isRemovingCompatibleModels || compatibleTestAllRunning) return
    const total = compatibleRows.length
    if (total === 0) return
    if (
      !confirm(
        `Remove all ${total} model(s) from ${displayAlias()}? Imported models and any aliases pointing at them are deleted. Models that ship with the build stay. This cannot be undone.`
      )
    )
      return
    isRemovingCompatibleModels = true
    try {
      const res = await api.deleteProviderModels(storageAlias)
      compatibleTestResults = {}
      compatibleTestErrors = {}
      compatibleTestLatency = {}
      await refreshCompatibleModels()
      notifyCustomModelsChanged()
      if (!res || res.count === 0) {
        alert('Nothing was removed: this node has no imported models of its own.')
      }
    } catch (err) {
      alert(`Remove failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isRemovingCompatibleModels = false
    }
  }

  // Each probe is a real completion that consumes account quota, so Test all
  // runs at most 2 at once; a lower ceiling than the registry sweep's 6.
  const COMPATIBLE_TEST_CONCURRENCY = 2

  // Bounded pool: see COMPATIBLE_TEST_CONCURRENCY above. Guarded the way
  // runModelSweep is: a run that outlives its panel stops writing, because
  // storageAlias may already belong to the next provider by then.
  async function handleTestAllCompatibleModels() {
    if (compatibleTestAllRunning) return
    const pid = providerId
    const ids = compatibleRows.map((r) => r.id)
    if (ids.length === 0) return
    compatibleTestAllRunning = true
    compatibleTestResults = {}
    compatibleTestErrors = {}
    compatibleTestLatency = {}
    try {
      let cursor = 0
      const workers = Array.from({ length: Math.min(COMPATIBLE_TEST_CONCURRENCY, ids.length) }, async () => {
        while (cursor < ids.length && pid === providerId) {
          await handleTestCompatibleModel(ids[cursor++])
        }
      })
      await Promise.allSettled(workers)
    } finally {
      if (pid === providerId) compatibleTestAllRunning = false
    }
  }

  let compatibleTestSummary = $derived(formatCheckAllSummary(compatibleTestResults))

  function copyCompatibleModel(modelId: string) {
    copyToClipboard(`${displayAlias()}/${modelId}`)
    copiedModelId = modelId
    setTimeout(() => (copiedModelId = null), 2000)
  }

  // Mirrors providers.IsFreeTierModel (internal/providers/registry_models.go).
  function isFreeTierModelId(id: string): boolean {
    return id.endsWith(':free') || id.endsWith('/free') || id.endsWith('-free')
  }

  // The server names the occupant in the refusal body, so the summary can say
  // what has to be renamed instead of only that the id was skipped.
  function reportImportRefusals(outcome: CustomModelImportOutcome) {
    if (outcome.refused.length === 0) return
    const noun = outcome.refused.length === 1 ? 'model' : 'models'
    alert(
      `Imported ${outcome.imported} model${outcome.imported === 1 ? '' : 's'}. ` +
        `${outcome.refused.length} ${noun} could not be registered:\n\n` +
        outcome.refused.map((r) => `${r.id} — ${r.reason}`).join('\n'),
    )
  }

  async function handleImportCompatibleModels(freeOnly = false) {
    if (isImportingCompatibleModels) return
    const active = providerConnections.find((c) => c.isActive !== 0)
    if (!active) return
    isImportingCompatibleModels = true
    try {
      const res = await api.getConnectionModels(active.id)
      const models = res.models || []
      if (models.length === 0) {
        alert('No models returned from /models.')
        return
      }
      const pending: CustomModelWrite[] = []
      for (const m of models) {
        const modelId = typeof m === 'string' ? m : (m.id || m.name || m.model || '')
        if (!modelId || compatibleRows.some((r) => r.id === modelId)) continue
        if (freeOnly && !isFreeTierModelId(modelId)) continue
        pending.push({
          id: modelId,
          key: `${storageAlias}|${modelId}|llm`,
          value: { id: modelId, providerAlias: storageAlias, type: 'llm' },
        })
      }
      const outcome = await importCustomModelsBestEffort(pending, api.saveCustomModel)
      await refreshCompatibleModels()
      if (outcome.imported > 0) notifyCustomModelsChanged()
      else if (outcome.refused.length === 0 && freeOnly) alert('No free models found in /models.')
      reportImportRefusals(outcome)
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to import models')
    } finally {
      isImportingCompatibleModels = false
    }
  }


  async function handleImportLiveCatalogModels() {
    if (isImportingLiveCatalogModels) return
    const active = providerConnections.find((c) => c.isActive !== 0)
    if (!active) {
      alert('Add an active connection first to fetch models.')
      return
    }
    isImportingLiveCatalogModels = true
    try {
      const res = await api.getConnectionModels(active.id)
      const models = res.models || []
      if (models.length === 0) {
        alert('No models returned from /models.')
        return
      }
      const pending: CustomModelWrite[] = []
      for (const m of models) {
        const modelId = typeof m === 'string' ? m : (m.id || m.name || m.model || '')
        if (!modelId) continue
        const alreadyBuiltin = builtInModels.some((b) => b.id === modelId)
        const alreadyCustom = providerCustomModels.some((c) => c.id === modelId)
        if (alreadyBuiltin || alreadyCustom) continue
        const caps =
          typeof m === 'object' &&
          m !== null &&
          'capabilities' in m &&
          m.capabilities &&
          typeof m.capabilities === 'object'
            ? (m.capabilities as { vision?: boolean; reasoning?: boolean })
            : undefined
        // An endpoint that reports its own limits is the authoritative source;
        // saving them means /v1/models keeps publishing them after the row
        // outlives this import.
        const declaredCtxLen =
          typeof m === 'object' && m !== null && 'context_length' in m
            ? Number(m.context_length) || 0
            : 0
        const declaredMaxOut =
          typeof m === 'object' && m !== null && 'max_completion_tokens' in m
            ? Number(m.max_completion_tokens) || 0
            : 0
        pending.push({
          id: modelId,
          key: `${storageAlias}|${modelId}|llm`,
          value: {
            id: modelId,
            providerAlias: storageAlias,
            type: 'llm',
            ...(caps ? { caps } : {}),
            ...(declaredCtxLen > 0 ? { contextWindow: declaredCtxLen } : {}),
            ...(declaredMaxOut > 0 ? { maxOutput: declaredMaxOut } : {}),
          },
        })
      }
      const outcome = await importCustomModelsBestEffort(pending, api.saveCustomModel)
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      if (outcome.imported > 0) notifyCustomModelsChanged()
      reportImportRefusals(outcome)
    } catch (err) {
      alert(err instanceof Error ? err.message : 'Failed to import models')
    } finally {
      isImportingLiveCatalogModels = false
    }
  }

  // --- Feature: check latest models / accessibility / prune unusable ----------

  let latestModels = $state<Array<{ id: string; name?: string }>>([])
  let isCheckingLatest = $state(false)
  let latestError = $state<string | null>(null)
  let isCheckingAll = $state(false)
  let checkAllProgress = $state({ done: 0, total: 0 })

  // ConnectionsView renders this panel without a {#key providerId}, so the
  // instance survives a provider switch. Everything computed for the old
  // provider is dropped before the new provider's DOM commits — otherwise the
  // stale "new models" list stays clickable and Add would save a model from
  // provider A into provider B's store.
  $effect.pre(() => {
    if (!providerId) return
    latestModels = []
    latestError = null
    isCheckingLatest = false
    isCheckingAll = false
    checkAllProgress = { done: 0, total: 0 }
    modelTestStatuses = {}
    modelTestErrors = {}
    checkAllSummary = null
    activeModelTestError = null
    // The compatible-model probes share this panel's lifetime too: without
    // these, a provider switch shows the previous provider's verdicts and
    // latencies against the new provider's model list.
    compatibleTesting = {}
    compatibleTestResults = {}
    compatibleTestErrors = {}
    compatibleTestLatency = {}
    compatibleTestAllRunning = false
    // The toolbar's filter and selection describe the previous provider's
    // rows; carrying them over would select ids that do not exist here and
    // hide the new provider's models behind a stale chip set.
    modelListFilter = { ...EMPTY_MODEL_LIST_FILTER }
    selectedModelIds = new Set()
  })

  // Providers whose /models the backend can enumerate
  // (internal/providers.modelsListURL + compatible nodes + live-catalog
  // providers). Kept in sync so the button only renders where it can work.
  const ModelsListURLs = new Set([
    'tokenharbor',
    'sumopod',
    'clixad',
    'dahl',
    'atria',
    'agnes',
    'bai',
    'muse',
    // Command Code's Provider API publishes its whole catalogue — free tiers
    // included — on the standard OpenAI /v1/models shape. Its registry
    // catalog is a 22-model snapshot, so this is the only path that surfaces
    // models added after it (reflection/beam:free, minimax/minimax-m3.1-flash).
    'commandcode',
    // Five more keyless catalogues, each probed live at 200 with no
    // Authorization header. vercel-ai-gateway and chutes ship no static
    // catalog at all, so this is the only surface that can list a model for
    // either; nvidia and ollama carry 11 and 8 static rows against 84 and 23
    // live ones; openrouter already had a free-tier fetcher but no backend
    // entry, so its button never rendered.
    'vercel-ai-gateway',
    'chutes',
    'nvidia',
    'ollama',
    'openrouter',
  ])

  // canListLiveModels mirrors the backend's list of providers with a live
  // catalogue (HandleGetConnectionModels): a button that cannot work should
  // not render.
  let canListLiveModels = $derived(
    providerId.startsWith('openai-compatible-') ||
      isCompatibleNode ||
      ['antigravity', 'gemini-cli', 'cline', 'clinepass', 'qoder', 'qoder-cn', 'zed'].includes(providerId) ||
      ModelsListURLs.has(providerId)
  )

  async function handleCheckLatestModels() {
    if (isCheckingLatest) return
    // Captured up front: a provider switch mid-fetch invalidates the response.
    const pid = providerId
    const active = providerConnections.find((c) => c.isActive !== 0)
    if (!active) {
      alert('Add an active connection first to fetch models.')
      return
    }
    isCheckingLatest = true
    latestError = null
    latestModels = []
    try {
      const res = await api.getConnectionModels(active.id)
      if (pid !== providerId) return
      const known = new Set<string>([
        ...builtInModels.map((m) => m.id),
        ...providerCustomModels.map((m) => m.id),
      ])
      for (const m of res.models || []) {
        const item = typeof m === 'string'
          ? { id: m }
          : { id: (m.id || m.model || m.name || '') as string, name: (m.name || '') as string | undefined }
        if (!item.id || known.has(item.id)) continue
        latestModels.push(item)
      }
      latestModels.sort((a, b) => a.id.localeCompare(b.id))
      if (latestModels.length === 0) {
        latestError = 'Catalog is up to date — no new models found.'
      }
    } catch (err) {
      if (pid !== providerId) return
      latestError = err instanceof Error ? err.message : 'Failed to fetch models'
    } finally {
      if (pid === providerId) isCheckingLatest = false
    }
  }

  async function handleAddLatestModel(id: string, name?: string) {
    try {
      await api.saveCustomModel(`${storageAlias}|${id}|llm`, {
        id,
        providerAlias: storageAlias,
        type: 'llm',
        ...(name ? { name } : {}),
      })
      latestModels = latestModels.filter((m) => m.id !== id)
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      customModels = modelsData.customModels
      notifyCustomModelsChanged()
    } catch (err) {
      alert(`Failed to add model: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Bounded concurrency pool: each probe is a real chat completion that can
  // take seconds, so running all of them at once would hammer the provider and
  // the browser (a ~30-model provider fires 30 simultaneous fetches).
  const TEST_CONCURRENCY = 6

  // Runs ids through the bounded pool, then publishes the sweep summary. A
  // run that outlives its provider panel publishes nothing: the verdicts it
  // produced belong to a provider this panel no longer shows.
  async function runModelSweep(ids: string[]) {
    const pid = providerId
    isCheckingAll = true
    checkAllProgress = { done: 0, total: ids.length }
    try {
      let cursor = 0
      const workers = Array.from({ length: Math.min(TEST_CONCURRENCY, ids.length) }, async () => {
        while (cursor < ids.length && pid === providerId) {
          const i = cursor++
          await testModel(ids[i], true)
          if (pid !== providerId) return
          checkAllProgress = { ...checkAllProgress, done: checkAllProgress.done + 1 }
        }
      })
      await Promise.allSettled(workers)
      if (pid !== providerId) return
      const summary = formatCheckAllSummary(modelTestStatuses)
      checkAllSummary = summary
      activeModelTestError = summary.message
    } finally {
      if (pid === providerId) isCheckingAll = false
    }
  }

  async function handleCheckAllModels() {
    if (isCheckingAll) return
    // Reset previous verdicts so the run is not confused with stale ones.
    // Only the full sweep clears them — a retry must keep the passed and
    // failed verdicts it is not re-testing.
    modelTestStatuses = {}
    modelTestErrors = {}
    checkAllSummary = null
    // visibleModels, not allAvailableModels: the table below renders exactly
    // this list, so probing a disabled model produced a verdict no row could
    // show and spent an upstream request on a model the operator switched off.
    await runModelSweep(visibleModels.map((m) => m.id))
  }

  // A blocked model was never asked anything, so re-testing only those is
  // cheap. It reuses the same bounded pool: a serial loop would leave the
  // button looking hung for as long as the cooldown, which is exactly the
  // wait the operator is sitting through.
  async function handleRetryBlockedModels() {
    if (isCheckingAll) return
    const blockedIds = getBlockedModelIds(modelTestStatuses)
    if (blockedIds.length === 0) return
    await runModelSweep(blockedIds)
  }

  // ── Batch actions over the toolbar selection ──
  // Every handler re-reads the selection at call time and guards on the
  // provider captured up front: a switch mid-batch must not write the old
  // provider's ids into the new one's store.

  function toggleModelSelected(modelId: string) {
    const next = new Set(selectedModelIds)
    if (next.has(modelId)) next.delete(modelId)
    else next.add(modelId)
    selectedModelIds = next
  }

  function handleToolbarToggleAll() {
    const next = toggleSelectAllModels(selectedModelIds, filteredRows, !currentSelection.allSelected)
    selectedModelIds = next
  }

  function handleToolbarClearSelection() {
    selectedModelIds = new Set()
  }

  // Compatible nodes probe at concurrency 2 (each probe is a real
  // completion that spends account quota), so the batch reuses that
  // ceiling rather than the registry sweep's 6.
  async function handleTestSelectedCompatibleModels() {
    if (compatibleTestAllRunning) return
    const pid = providerId
    const ids = selectedVisibleIds(selectedModelIds, filteredRows)
    if (ids.length === 0) return
    compatibleTestAllRunning = true
    try {
      let cursor = 0
      const workers = Array.from({ length: Math.min(COMPATIBLE_TEST_CONCURRENCY, ids.length) }, async () => {
        while (cursor < ids.length && pid === providerId) {
          await handleTestCompatibleModel(ids[cursor++])
        }
      })
      await Promise.allSettled(workers)
    } finally {
      if (pid === providerId) compatibleTestAllRunning = false
    }
  }

  // Test selected reuses the same bounded sweep as Check All, so the
  // concurrency ceiling and the pid guard are the ones already reviewed.
  async function handleTestSelectedModels() {
    if (isCheckingAll) return
    const ids = selectedVisibleIds(selectedModelIds, filteredRows)
    if (ids.length === 0) return
    await runModelSweep(ids)
  }

  async function handleDisableSelectedModels() {
    const pid = providerId
    const ids = selectedVisibleIds(selectedModelIds, filteredRows)
    if (ids.length === 0) return
    if (!confirm(`Disable ${ids.length} selected model(s)? They stop appearing in this provider's model list.`)) return
    const updated = Array.from(new Set([...disabledModelIds, ...ids]))
    disabledModelIds = updated
    try {
      await api.saveDisabledModels(storageAlias, updated)
    } catch (err) {
      console.error('Failed to disable selected models:', err)
      if (pid === providerId) {
        const modelsData = await fetchProviderModelsData(providerId, storageAlias)
        disabledModelIds = modelsData.disabledModelIds
      }
    }
  }

  async function handleEnableSelectedModels() {
    const pid = providerId
    const ids = new Set(selectedVisibleIds(selectedModelIds, filteredRows))
    if (ids.size === 0) return
    const updated = disabledModelIds.filter((id) => !ids.has(id))
    disabledModelIds = updated
    try {
      await api.saveDisabledModels(storageAlias, updated)
    } catch (err) {
      console.error('Failed to enable selected models:', err)
      if (pid === providerId) {
        const modelsData = await fetchProviderModelsData(providerId, storageAlias)
        disabledModelIds = modelsData.disabledModelIds
      }
    }
  }

  /**
   * Disables every model whose last probe failed or whose account is in
   * cooldown, in one request.
   *
   * The verdicts are already on screen from the sweep that produced them, so
   * this sends only the failures — the server merges them into the stored
   * list, which is what keeps two open tabs from disabling each other's
   * models. "Blocked" counts as dead here: the account is parked and the model
   * cannot serve until the cooldown lifts, which is the same operator-visible
   * outcome as a failure. A model that was never probed is not included —
   * nobody has evidence about it.
   */
  async function handleAutoDisableDeadModels() {
    const pid = providerId
    const deadIds = isCompatibleNode
      ? compatibleRows
          .filter((r) => {
            const s = compatibleTestResults[r.id]
            return s === 'error' || s === 'blocked'
          })
          .map((r) => r.id)
      : allAvailableModels
          .filter((m) => {
            const s = modelTestStatuses[m.id]
            return s === 'error' || s === 'blocked'
          })
          .map((m) => m.id)
    if (deadIds.length === 0) {
      alert('No dead models — every model that was probed either passed or has not been tested yet.')
      return
    }
    const preview = await api
      .autoDisableModels(storageAlias, deadIds, true)
      .catch(() => null)
    const alreadyOff = preview?.alreadyDisabled.length ?? 0
    const willDisable = preview?.disabled.length ?? deadIds.length
    const parts = [`Disable ${willDisable} dead model(s)?`]
    if (alreadyOff > 0) parts.push(`${alreadyOff} already disabled.`)
    parts.push('They stop appearing in /v1/models and every model picker, and leave the combos they were in.')
    if (!confirm(parts.join(' '))) return

    try {
      const res = await api.autoDisableModels(storageAlias, deadIds)
      if (pid !== providerId) return
      const modelsData = await fetchProviderModelsData(providerId, storageAlias)
      disabledModelIds = modelsData.disabledModelIds
      customModels = modelsData.customModels
      notifyCustomModelsChanged()
      selectedModelIds = new Set()
      const summary = [`${res.disabled.length} disabled`]
      if (res.alreadyDisabled.length > 0) summary.push(`${res.alreadyDisabled.length} already off`)
      alert(`${summary.join(', ')}.`)
    } catch (err) {
      alert(`Auto-disable failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  /**
   * Delete selected: custom models are removed from the store, registry
   * models are static and get disabled instead (the existing single-row
   * rule), and compatible legacy aliases lose their alias. One confirm for
   * the whole batch — the per-row confirm would be N dialogs.
   */
  async function handleDeleteSelectedModels() {
    if (isCheckingAll) return
    const pid = providerId
    const rows = filteredRows.filter((r) => selectedModelIds.has(r.id))
    if (rows.length === 0) return
    const customCount = rows.filter((r) => r.source !== 'registry').length
    const registryCount = rows.length - customCount
    const parts = [`Delete ${rows.length} selected model(s)?`]
    if (customCount > 0) parts.push(`${customCount} custom/imported model(s) are removed permanently.`)
    if (registryCount > 0) parts.push(`${registryCount} registry model(s) cannot be deleted and will be disabled instead.`)
    if (!confirm(parts.join(' '))) return

    let deleted = 0
    let disabledOnly = 0
    const failures: string[] = []
    for (const row of rows) {
      try {
        if (row.source === 'registry') {
          await handleDisableModel(row.id)
          disabledOnly++
        } else if (row.source === 'legacyAlias') {
          const alias = compatibleRows.find((r) => r.id === row.id)?.alias
          if (alias) await api.deleteModelAlias(alias)
          else await api.deleteCustomModel(`${storageAlias}|${row.id}|llm`)
          deleted++
        } else {
          await api.deleteCustomModel(`${storageAlias}|${row.id}|llm`)
          deleted++
        }
      } catch (err) {
        failures.push(`${row.id}: ${err instanceof Error ? err.message : String(err)}`)
      }
    }
    if (pid !== providerId) return
    const modelsData = await fetchProviderModelsData(providerId, storageAlias)
    customModels = modelsData.customModels
    disabledModelIds = modelsData.disabledModelIds
    notifyCustomModelsChanged()
    selectedModelIds = new Set()
    const summary = [`${deleted} deleted`]
    if (disabledOnly > 0) summary.push(`${disabledOnly} disabled`)
    if (failures.length > 0) summary.push(`${failures.length} failed`)
    alert(`${summary.join(', ')}.${failures.length > 0 ? `\n\n${failures.join('\n')}` : ''}`)
  }

  // Delete is only real for custom models; registry models are static, so an
  // unusable built-in model is disabled instead (the existing disable path).
  async function handleDeleteModel(modelId: string) {
    const custom = providerCustomModels.find((m) => m.id === modelId)
    if (custom) {
      if (!confirm(`Delete custom model "${modelId}"?`)) return
      try {
        await api.deleteCustomModel(`${storageAlias}|${modelId}|llm`)
        const modelsData = await fetchProviderModelsData(providerId, storageAlias)
        customModels = modelsData.customModels
        notifyCustomModelsChanged()
      } catch (err) {
        alert(`Failed to delete model: ${err instanceof Error ? err.message : String(err)}`)
      }
      return
    }
    await handleDisableModel(modelId)
  }

  // Deprecated models of this provider, so the section can say how many of
  // the listed models are dead upstream instead of leaving the operator to
  // count the badges.
  async function reloadDeprecations() {
    const pid = providerId
    const deps = await loadDeprecations()
    if (pid !== providerId) return
    modelDeprecationCount = Object.values(deps).filter(
      (d) => d.provider === providerId && visibleModels.some((m) => m.id === d.model)
    ).length
  }

  /**
   * Re-reads the provider's live catalogue and clears the badge from any model
   * it still serves. Only revival is decided here: a model missing from the
   * catalogue is not badged on that evidence, because catalogues are routinely
   * partial and a healthy model would be blacklisted with it.
   */
  async function handleSyncModelDeprecations() {
    if (isSyncingModels) return
    const pid = providerId
    isSyncingModels = true
    try {
      const res = await api.syncProviderModels(providerId)
      if (pid !== providerId) return
      await refreshDeprecations()
      if (res.revived > 0) {
        await reloadDeprecations()
        alert(`Sync complete — ${res.revived} model(s) are serving again and no longer marked Deprecated.`)
      } else {
        await reloadDeprecations()
        alert('Sync complete. Models the provider no longer serves stay marked until a live request confirms they are gone.')
      }
    } catch (err) {
      alert(`Model sync failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      if (pid === providerId) isSyncingModels = false
    }
  }

  $effect(() => {
    const unsubscribe = subscribeDeprecations(reloadDeprecations)
    void reloadDeprecations()
    return unsubscribe
  })
</script>

<div class="flex min-w-0 flex-col gap-6 px-1 sm:gap-8 sm:px-0">
  <!-- 0. Header: Back button + Provider Icon & Name -->
  <div class="min-w-0">
    <button
      type="button"
      onclick={onBack}
      class="inline-flex items-center gap-1 text-sm text-text-muted hover:text-primary transition-colors mb-4 cursor-pointer"
    >
      <span class="material-symbols-outlined text-lg">arrow_back</span>
      Back to Providers
    </button>

    <div class="flex min-w-0 items-center justify-between gap-3 sm:gap-4">
      <div class="flex min-w-0 items-center gap-3 sm:gap-4">
        <div
          class="flex size-12 shrink-0 items-center justify-center rounded-lg"
          style="background-color: {providerColor}15;"
        >
          <ProviderIcon
            id={providerId}
            apiType={selectedNode?.apiType}
            size="lg"
            class="border-transparent bg-transparent"
          />
        </div>

        <div class="min-w-0">
          <div class="flex items-center gap-3 flex-wrap">
            <h1 class="truncate text-2xl font-semibold tracking-tight sm:text-3xl">
              {providerName}
            </h1>
            {#if providerWebsite}
              <a
                href={providerWebsite}
                target="_blank"
                rel="noopener noreferrer"
                class="text-xs text-primary hover:underline inline-flex items-center gap-1"
              >
                <span class="material-symbols-outlined text-sm">open_in_new</span>
                {providerWebsiteLabel}
              </a>
            {/if}
          </div>
          <div class="flex items-center gap-2 flex-wrap">
            <p class="text-text-muted">
              {totalConnectionsCount} connection{totalConnectionsCount === 1 ? '' : 's'}
            </p>
            {#if totalConnectionsCount > 0}
              <ProviderAccountStatusStrip connections={paginatedConnections} stats={cachedStats} healthById={connectionHealthById} showCount={false} />
            {/if}
            {#if isProviderAllDisabled}
              <Badge variant="default" size="sm">
                <span class="flex items-center gap-1">
                  <PauseCircle class="w-3 h-3" />
                  Disabled
                </span>
              </Badge>
            {/if}
          </div>
        </div>
      </div>

      {#if totalConnectionsCount > 0}
        <div class="flex shrink-0 items-center gap-2 self-center ml-auto">
          <Toggle
            size="sm"
            checked={isProviderActive}
            onChange={handleToggleProvider}
            title={isProviderActive ? 'Disable all connections for this provider' : 'Enable all connections for this provider'}
          />
        </div>
      {/if}
    </div>
  </div>

  <!-- 1. Risk Notice (for OAuth / subscription providers) -->
  {#if hasRiskNotice}
    <div class="flex items-center gap-2 px-3 py-2 rounded-lg bg-yellow-500/10 border border-yellow-500/30">
      <span class="material-symbols-outlined text-[16px] text-yellow-500 mt-0.5 shrink-0">warning</span>
      <p class="text-xs text-red-600 dark:text-yellow-400 leading-relaxed">
        ⚠️ Risk Notice: This provider uses a subscription/OAuth session not officially licensed for proxy/router use. Account may be restricted or banned. Use at your own risk.
      </p>
    </div>
  {/if}

  <!-- 1b. Provider notice from upstream registry (apiKeyUrl → Get API Key button) -->
  {#if providerNoticeText}
    <div class="flex flex-col gap-2 rounded-lg border border-blue-500/30 bg-blue-500/10 px-3 py-2 sm:flex-row sm:items-center">
      <span class="material-symbols-outlined text-[16px] text-blue-500 shrink-0">info</span>
      <p class="min-w-0 flex-1 text-xs leading-relaxed text-blue-600 dark:text-blue-400">{providerNoticeText}</p>
      {#if providerNoticeApiKeyUrl}
        <a
          href={providerNoticeApiKeyUrl}
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex justify-center rounded bg-blue-500 px-2 py-1 text-xs font-medium text-white transition-colors hover:bg-blue-600 sm:py-0.5"
        >
          Get API Key →
        </a>
      {/if}
    </div>
  {/if}

  <!-- 2. Compatible Node details (if custom node) — upstream parity: endpoint line + Add API Key / Edit / Delete -->
  {#if selectedNode}
    <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] p-6">
      <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <h2 class="text-lg font-semibold">
            {isAnthropicCompatibleNode ? 'Anthropic Compatible Details' : 'OpenAI Compatible Details'}
          </h2>
          <p class="break-all text-sm text-text-muted">
            {isAnthropicCompatibleNode
              ? `Messages API · ${(selectedNode.baseUrl || '').replace(/\/$/, '')}/messages`
              : `${isResponsesNode ? 'Responses API' : 'Chat Completions'} · ${(selectedNode.baseUrl || '').replace(/\/$/, '')}/${isResponsesNode ? 'responses' : 'chat/completions'}`}
          </p>
        </div>
        <div class="grid grid-cols-1 gap-2 sm:flex sm:items-center">
          <button
            type="button"
            onclick={openAddKeyModal}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-brand-500 hover:bg-brand-600 text-white shadow-sm h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
          >
            <span class="material-symbols-outlined text-[18px]">add</span>
            Add API Key
          </button>
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
          <button
            type="button"
            onclick={() => (showEditNodeModal = true)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
          >
            <span class="material-symbols-outlined text-[18px]">edit</span>
            Edit
          </button>
          <button
            type="button"
            onclick={() => handleDeleteProviderNode(selectedNode!.id)}
            class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px] w-full sm:w-auto"
          >
            <span class="material-symbols-outlined text-[18px]">delete</span>
            Delete
          </button>
        </div>
      </div>
      <!-- Status only: the policy is edited in the node's dialog (the same fields as
           Name and Base URL there), so this line answers "what is this node allowed"
           without turning the page into a form. -->
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-text-muted">
        <span class="font-medium text-text-main">Capacity</span>
        <span class="font-mono">{nodeLimitsSummary(nodeLimitsDraftOf(selectedNode.limits ?? null))}</span>
        {#if selectedNode.limits}
          <span class="text-text-subtle">·</span>
          <span class="font-mono">{nodeLimitsUsageSummary(selectedNode.limits)}</span>
          {#if selectedNode.limits.expired}
            <Badge tone="error">Contract ended</Badge>
          {:else if selectedNode.limits.tokenQuota > 0 && selectedNode.limits.usedTokens >= selectedNode.limits.tokenQuota}
            <Badge tone="error">Quota spent</Badge>
          {/if}
        {/if}
      </div>
    </div>
  {/if}

  {#if isNoAuth}
    <NoAuthProxyCard providerId={providerId} />
  {:else}
  <!-- 3. Connections Card -->
  <ProviderConnectionsSection
    {providerId}
    provider={selectedCatalogItem}
    providerNode={selectedNode}
    {connections}
    {providerStats}
    {providerNodes}
    {proxyPools}
    bind:settings
    bind:isRoundRobin
    bind:stickyLimit
    bind:lockedProxyGroup
    bind:lockedProxyPoolId
    bind:showAddKeyModal
    bind:showExportKeysModal
    bind:showShareProviderModal
    bind:addConnectionError
    bind:providerConnections
    bind:paginatedConnections
    bind:connectionHealthById
    bind:totalConnectionsCount
    {freebuffSessions}
    {isAuthorizingFreebuff}
    {onRefresh}
    onAddConnection={handleAddConnectionClick}
    onStartFreebuff={startFreebuffFlow}
    onSelectFreebuffAccount={selectFreebuffAccount}
  />
  {/if}

  <!-- 4. Models Card: compatible nodes use upstream CompatibleModelsSection layout -->
  {#if isCompatibleNode}
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] p-6">
    <div class="flex flex-col gap-4">
      <p class="text-sm text-text-muted">
        Add {isAnthropicCompatibleNode ? 'Anthropic' : 'OpenAI'}-compatible models manually or import them from the /models endpoint.
      </p>
      <!-- Row 1: the field and its own button. Row 2: the bulk actions, which
           are a different kind of thing — they act on the whole list, not on
           what was typed — so they get their own line instead of competing
           with the input for width. The two cost notes stay visible under
           row 2: they are warnings about quota and about deleting imported
           work, and hiding them behind a hover means an operator meets the
           consequence only after clicking. -->
      <div class="flex flex-col gap-2 rounded-lg border border-border-subtle bg-surface-2/40 p-2.5">
        <div class="flex flex-wrap items-end gap-2">
          <div class="min-w-[200px] flex-1">
            <label for="new-compatible-model-input" class="mb-1 block text-[10px] font-semibold uppercase tracking-wide text-text-muted/70">Model ID</label>
            <input
              id="new-compatible-model-input"
              type="text"
              bind:value={newCompatibleModel}
              onkeydown={(e) => { if (e.key === 'Enter') handleAddCompatibleModel() }}
              placeholder={isAnthropicCompatibleNode ? 'claude-3-opus-20240229' : 'gpt-4o — paste a list for several'}
              class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm text-text-main placeholder:text-text-muted focus:outline-none focus:border-primary"
            />
            {#if newCompatibleModelsPlan.ids.length > 1 || newCompatibleModelsPlan.invalid.length > 0}
              <p class="mt-1 text-xs text-text-muted">
                {newCompatibleModelsPlan.ids.length} id{newCompatibleModelsPlan.ids.length === 1 ? '' : 's'} to add
                {#if newCompatibleModelsPlan.duplicates.length > 0}
                  · {newCompatibleModelsPlan.duplicates.length} repeated
                {/if}
                {#if newCompatibleModelsPlan.invalid.length > 0}
                  · {newCompatibleModelsPlan.invalid.length} rejected (no slash, pipe or quotes in an id)
                {/if}
              </p>
            {/if}
          </div>
          <button
            type="button"
            onclick={handleAddCompatibleModel}
            disabled={!newCompatibleModel.trim() || isAddingCompatibleModel}
            class="inline-flex h-8 items-center justify-center gap-1.5 rounded-[8px] bg-brand-500 px-3 text-xs font-semibold text-white shadow-sm transition-all duration-150 ease-out hover:bg-brand-600 active:scale-[0.97] disabled:opacity-50 cursor-pointer disabled:cursor-not-allowed"
          >
            <span class="material-symbols-outlined text-[16px]">add</span>
            {isAddingCompatibleModel ? 'Adding...' : 'Add'}
          </button>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onclick={() => handleImportCompatibleModels(false)}
            disabled={!canImportCompatible || isImportingCompatibleModels}
            title="Fetch this endpoint's /models and add everything it lists"
            class="inline-flex h-8 items-center justify-center gap-1.5 rounded-[8px] border border-border bg-surface-2 px-3 text-xs font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 cursor-pointer disabled:cursor-not-allowed"
          >
            <span class="material-symbols-outlined text-[16px]">download</span>
            {isImportingCompatibleModels ? 'Importing...' : 'Import from /models'}
          </button>
          <button
            type="button"
            onclick={() => handleImportCompatibleModels(true)}
            disabled={!canImportCompatible || isImportingCompatibleModels}
            title="Same as import, but skip anything that is not on the provider's free tier"
            class="inline-flex h-8 items-center justify-center gap-1.5 rounded-[8px] border border-border bg-surface-2 px-3 text-xs font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 cursor-pointer disabled:cursor-not-allowed"
          >
            <span class="material-symbols-outlined text-[16px]">savings</span>
            Import free only
          </button>
          <button
            type="button"
            onclick={handleTestAllCompatibleModels}
            disabled={compatibleRows.length === 0 || compatibleTestAllRunning}
            title="Test all sends a real completion per model and consumes account quota"
            class="inline-flex h-8 items-center justify-center gap-1.5 rounded-[8px] border border-border bg-surface-2 px-3 text-xs font-medium text-text-main transition-colors hover:bg-surface-3 disabled:opacity-50 cursor-pointer disabled:cursor-not-allowed"
          >
            <span class="material-symbols-outlined text-[16px]">{compatibleTestAllRunning ? 'progress_activity' : 'science'}</span>
            {compatibleTestAllRunning ? `Testing ${compatibleTestSummary.total}/${compatibleRows.length}...` : 'Test all models'}
          </button>
          <button
            type="button"
            onclick={handleRemoveAllCompatibleModels}
            disabled={compatibleRows.length === 0 || isRemovingCompatibleModels || compatibleTestAllRunning}
            title="Remove all deletes this node's imported models and the aliases pointing at them; models that ship with the build stay"
            class="inline-flex h-8 items-center justify-center gap-1.5 rounded-[8px] border border-border bg-surface-2 px-3 text-xs font-medium text-text-main transition-colors hover:border-red-500/40 hover:bg-red-500/10 hover:text-red-500 disabled:opacity-50 cursor-pointer disabled:cursor-not-allowed"
          >
            <span class="material-symbols-outlined text-[16px]">{isRemovingCompatibleModels ? 'progress_activity' : 'delete_sweep'}</span>
            {isRemovingCompatibleModels ? 'Removing...' : 'Remove all models'}
          </button>
          <button
            type="button"
            onclick={handleAutoDisableDeadModels}
            disabled={compatibleRows.length === 0}
            title="Disable every model whose last probe failed or whose account is in cooldown. They stop appearing in /v1/models and every model picker, and leave the combos they were in."
            class="inline-flex h-8 items-center justify-center gap-1.5 rounded-[8px] border border-amber-500/40 bg-amber-500/10 px-3 text-xs font-medium text-amber-600 transition-colors hover:bg-amber-500/20 disabled:opacity-50 cursor-pointer disabled:cursor-not-allowed dark:text-amber-400"
          >
            <span class="material-symbols-outlined text-[16px]">heart_broken</span>
            Disable dead models
          </button>
        </div>
        <p class="text-xs text-text-muted">Test all sends a real completion per model and consumes account quota.</p>
        <p class="text-xs text-text-muted">Remove all deletes this node's imported models and the aliases pointing at them; models that ship with the build stay.</p>
        {#if !canImportCompatible}
          <p class="text-xs text-text-muted">Add a connection to enable importing models.</p>
        {/if}
      </div>
      <!-- Toolbar: search / filter / batch selection over the rows below.
           Same bordered panel as the registered branch, so the two lists
           read as the same control surface rather than two inventions. -->
      <div class="flex flex-col gap-2 rounded-lg border border-border-subtle bg-surface-2/40 p-2.5">
        <ModelSearchBar bind:value={modelListFilter.query} onSearch={(q) => (modelListFilter = { ...modelListFilter, query: q })} />
        <ModelFilterChips
          filter={modelListFilter}
          onChange={(f) => (modelListFilter = f)}
          {availableCaps}
        />
        <ModelBatchBar
          selection={currentSelection}
          busy={compatibleTestAllRunning}
          canDisable
          canDelete={selectedRowsHaveCustom}
          onToggleAll={handleToolbarToggleAll}
          onTest={handleTestSelectedCompatibleModels}
          onDisable={handleDisableSelectedModels}
          onEnable={handleEnableSelectedModels}
          onDelete={handleDeleteSelectedModels}
          onClear={handleToolbarClearSelection}
        />
      </div>
      {#if filteredRows.length === 0 && compatibleRows.length > 0}
        <p class="text-xs text-text-muted">No models match the current search and filters.</p>
      {/if}
      {#if compatibleRows.length > 0}
        <div class="flex flex-col gap-3">
          {#each filteredRows as row (row.id)}
            {@const tStatus = compatibleTestResults[row.id]}
            {@const tError = compatibleTestErrors[row.id]}
            {@const isTestingRow = compatibleTesting[row.id]}
            <div class="flex items-start gap-3 p-3 rounded-lg border {tStatus === 'ok' ? 'border-green-500/40' : tStatus === 'blocked' ? 'border-amber-500/40' : tStatus === 'error' ? 'border-red-500/40' : 'border-border'} hover:bg-sidebar/50">
              <input
                type="checkbox"
                checked={selectedModelIds.has(row.id)}
                onchange={() => toggleModelSelected(row.id)}
                class="mt-1.5 size-3.5 shrink-0 accent-brand-500 cursor-pointer"
                aria-label="Select {row.id}"
              />
              <span
                class="material-symbols-outlined text-base text-text-muted mt-0.5"
                style={tStatus === 'ok' ? 'color:#22c55e' : tStatus === 'blocked' ? 'color:#f59e0b' : tStatus === 'error' ? 'color:#ef4444' : undefined}
              >
                {tStatus === 'ok' ? 'check_circle' : tStatus === 'blocked' ? 'schedule' : tStatus === 'error' ? 'cancel' : 'smart_toy'}
              </span>
              <div class="flex-1 min-w-0">
                <p class="text-sm font-medium truncate">{row.id}</p>
                <div class="flex items-center gap-1 mt-1 flex-wrap">
                  <code class="max-w-[72vw] truncate text-xs text-text-muted font-mono bg-sidebar px-1.5 py-0.5 rounded sm:max-w-[360px]">{displayAlias()}/{row.id}</code>
                  <!-- Capability icons, mirroring the registered grid's row:
                       the same model id can be imported into a compatible
                       node, and a row that shows nothing there reads as a
                       model with no capabilities at all. -->
                  {#if row.caps.vision}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-text-muted/70" style="font-size: 12px;">visibility</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Vision — Supports image input
                      </div>
                    </div>
                  {/if}
                  {#if row.caps.reasoning}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-text-muted/70" style="font-size: 12px;">neurology</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Reasoning — Supports reasoning / thinking
                      </div>
                    </div>
                  {/if}
                  {#if row.caps.jailbreak}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-red-500/80" style="font-size: 12px;">local_fire_department</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Jailbreak — Uncensored / unrestricted
                      </div>
                    </div>
                  {/if}
                  <div class="relative group/btn">
                    <button
                      type="button"
                      onclick={() => copyCompatibleModel(row.id)}
                      class="p-0.5 hover:bg-sidebar rounded text-text-muted hover:text-primary cursor-pointer"
                    >
                      <span class="material-symbols-outlined text-sm">{copiedModelId === row.id ? 'check' : 'content_copy'}</span>
                    </button>
                    <span class="pointer-events-none absolute top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                      {copiedModelId === row.id ? 'Copied!' : 'Copy'}
                    </span>
                  </div>
                  {#if providerConnections.length > 0}
                    <div class="relative group/btn">
                      <button
                        type="button"
                        onclick={() => handleTestCompatibleModel(row.id)}
                        disabled={isTestingRow}
                        class="p-0.5 hover:bg-sidebar rounded text-text-muted hover:text-primary transition-colors cursor-pointer"
                      >
                        <span class="material-symbols-outlined text-sm" style={isTestingRow ? 'animation: spin 1s linear infinite' : undefined}>
                          {isTestingRow ? 'progress_activity' : 'science'}
                        </span>
                      </button>
                      <span class="pointer-events-none absolute top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                        {isTestingRow ? 'Testing...' : 'Test'}
                      </span>
                    </div>
                  {/if}
                  {#if tStatus && compatibleTestLatency[row.id] !== undefined}
                    <span class="text-xs text-text-muted font-mono">{compatibleTestLatency[row.id]} ms</span>
                  {/if}
                  <!-- Enable/disable this one row. A disabled row stops being
                       published by /v1/models and disappears from the pickers,
                       which is the difference between this and Delete: the row
                       stays here, reversible, and the import that wrote it is
                       left alone. -->
                  <div class="relative group/btn">
                    <button
                      type="button"
                      onclick={() => (row.disabled ? handleEnableModel(row.id) : handleDisableModel(row.id))}
                      class="p-0.5 rounded text-text-muted transition-colors hover:text-primary cursor-pointer {row.disabled ? 'text-amber-500' : ''}"
                      title={row.disabled ? 'Enable this model — it returns to /v1/models and the pickers' : 'Disable this model — it stops appearing in /v1/models and the pickers'}
                    >
                      <span class="material-symbols-outlined text-sm">{row.disabled ? 'visibility_off' : 'visibility'}</span>
                    </button>
                    <span class="pointer-events-none absolute top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                      {row.disabled ? 'Disabled — enable' : 'Disable'}
                    </span>
                  </div>
                </div>
                {#if row.disabled}
                  <div class="mt-2 flex items-start gap-1.5 text-xs text-amber-600 dark:text-amber-400 bg-amber-500/10 px-2.5 py-1.5 rounded-md border border-amber-500/20">
                    <span class="material-symbols-outlined text-sm shrink-0 mt-0.5">visibility_off</span>
                    <span class="font-medium leading-relaxed">Disabled — hidden from <code class="font-mono">/v1/models</code> and every model picker. The row stays here so you can enable it again.</span>
                  </div>
                {/if}
                {#if tError}
                  <div class="mt-2 flex items-start gap-1.5 text-xs text-red-500 bg-red-500/10 px-2.5 py-1.5 rounded-md border border-red-500/20">
                    <span class="material-symbols-outlined text-sm shrink-0 mt-0.5">error</span>
                    <span class="break-words font-medium leading-relaxed">{tError}</span>
                  </div>
                {/if}
              </div>
              <button
                type="button"
                onclick={() => handleDeleteCompatibleModel(row)}
                class="p-1 hover:bg-red-50 rounded text-red-500 cursor-pointer"
                title="Remove model"
              >
                <span class="material-symbols-outlined text-sm">delete</span>
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
  {:else}
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] p-6">
    <!-- Header -->
    <div class="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
      <div class="flex items-center gap-3">
        <h2 class="text-lg font-semibold">Available Models</h2>
        {#if modelDeprecationCount > 0}
          <span
            class="inline-flex items-center gap-1 rounded-full bg-warning/10 px-2 py-0.5 text-[11px] font-medium text-warning border border-warning/25"
            title="These models were retired by the provider and cannot serve a request"
          >
            <span class="material-symbols-outlined text-[13px] leading-none" aria-hidden="true">error</span>
            {modelDeprecationCount} deprecated
          </span>
        {/if}
        {#if providerThinkingLevels}
        <select
          title="Appends (level) suffix to copied model names"
          value={thinkingLevel}
          onchange={handleThinkingChange}
          class="rounded-md border border-border bg-background px-2 py-1 text-xs focus:border-primary focus:outline-none cursor-pointer"
        >
          {#each providerThinkingLevels as opt (opt)}
            <option value={opt}>Thinking: {opt.charAt(0).toUpperCase() + opt.slice(1)}</option>
          {/each}
        </select>
        {/if}
      </div>

      <div class="flex gap-2">
        <button
          type="button"
          onclick={handleToggleAllModels}
          class="inline-flex items-center justify-center gap-2 font-semibold transition-all duration-150 ease-out cursor-pointer active:scale-[0.97] bg-surface-2 hover:bg-surface-3 text-text-main border border-border h-7 px-3 text-xs rounded-[8px]"
        >
          <span class="material-symbols-outlined text-[18px]">block</span>
          {allDisabled ? 'Enable All' : 'Disable All'}
        </button>
      </div>
    </div>

    {#if activeModelTestError}
      {@const isBlockedSeverity = checkAllSummary?.severity === 'blocked'}
      {@const blockedCount = getBlockedModelIds(modelTestStatuses).length}
      <div class="mb-3 flex items-start gap-2.5 rounded-lg border {isBlockedSeverity ? 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400' : 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400'} p-3 text-xs">
        <span class="material-symbols-outlined shrink-0 text-base">{isBlockedSeverity ? 'schedule' : 'error'}</span>
        <div class="flex-1 font-medium leading-relaxed flex items-center justify-between gap-2 flex-wrap">
          <span>{activeModelTestError}</span>
          {#if blockedCount > 0}
            <button
              type="button"
              onclick={handleRetryBlockedModels}
              disabled={isCheckingAll}
              class="px-2 py-0.5 rounded bg-amber-500/20 hover:bg-amber-500/30 text-amber-700 dark:text-amber-300 font-semibold text-xs cursor-pointer disabled:opacity-50"
            >
              Retry blocked ({blockedCount})
            </button>
          {/if}
        </div>
        <button
          type="button"
          onclick={() => { activeModelTestError = null; checkAllSummary = null; }}
          class="hover:opacity-75 cursor-pointer shrink-0"
          title="Dismiss"
        >
          <span class="material-symbols-outlined text-sm">close</span>
        </button>
      </div>
    {/if}
    {#if isFreebuff}
      <div class="mb-4">
        <FreebuffSessionBanner
          session={freebuffSession}
          isLoading={isLoadingSession}
          expiresInMin={sessionExpiresInMin}
          onRefresh={loadFreebuffSession}
          models={visibleModels.map((m) => ({ id: m.id, name: m.name ?? m.id }))}
          onSwitch={switchFreebuffModel}
          connections={providerConnections.map((c) => {
            const specific = c.providerSpecificData as Record<string, any> | undefined
            return {
              id: c.id,
              name: formatEmailLabel(c.name || c.email || 'Freebuff Account', $emailPrivacy),
              isActive: c.isActive === 1,
              currentModel: freebuffSessions[c.id]?.currentModel || (specific?.freebuffModel as string) || (specific?.assignedModel as string),
              status: freebuffSessions[c.id]?.status,
            }
          })}
          selectedConnectionId={targetFreebuffConn?.id}
          onSelectConnection={selectFreebuffAccount}
        />
      </div>
    {/if}
    <!-- Toolbar: search / filter / batch selection over the grid below.
         One bordered panel instead of three loose blocks: the search field,
         the two chip rows and the batch bar are one control surface, and
         separating them made the grid look like it started twice. -->
    <div class="mb-3 flex flex-col gap-2 rounded-lg border border-border-subtle bg-surface-2/40 p-2.5">
      <ModelSearchBar bind:value={modelListFilter.query} onSearch={(q) => (modelListFilter = { ...modelListFilter, query: q })} />
      <ModelFilterChips filter={modelListFilter} onChange={(f) => (modelListFilter = f)} {availableCaps} />
      <ModelBatchBar
        selection={currentSelection}
        busy={isCheckingAll}
        canDisable
        canDelete={selectedRowsHaveCustom}
        onToggleAll={handleToolbarToggleAll}
        onTest={handleTestSelectedModels}
        onDisable={handleDisableSelectedModels}
        onEnable={handleEnableSelectedModels}
        onDelete={handleDeleteSelectedModels}
        onClear={handleToolbarClearSelection}
      />
    </div>
    {#if gridModels.length === 0 && allAvailableModels.length > 0}
      <p class="mb-3 text-xs text-text-muted">No models match the current search and filters.</p>
    {/if}
    <!-- Models flex-wrap list matching upstream -->
    <div class="flex flex-wrap gap-3">
      {#each gridModels as model (model.id)}
        {@const level = resolveThinkingSuffix(model.id)}
        {@const fullModelId = `${storageAlias}/${model.id}${level ? `(${level})` : ''}`}
        {@const rowCaps = modelCaps[model.id] ?? model.caps}
        {@const testStatus = modelTestStatuses[model.id]}
        {@const isTestingThis = testStatus === 'testing'}
        {@const isSessionActive = checkIsActiveSession(model.id)}
        <div
          class="group min-w-0 max-w-full rounded-lg border px-3 py-2 {testStatus === 'ok' ? 'border-green-500/40' : testStatus === 'blocked' ? 'border-amber-500/40' : testStatus === 'error' ? 'border-red-500/40' : 'border-border'} hover:bg-sidebar/50 transition-colors"
        >
          <div class="flex min-w-0 items-start gap-2 sm:items-center">
            <input
              type="checkbox"
              checked={selectedModelIds.has(model.id)}
              onchange={() => toggleModelSelected(model.id)}
              class="mt-1 sm:mt-0 size-3.5 shrink-0 accent-brand-500 cursor-pointer"
              aria-label="Select {model.id}"
            />
            <span class="material-symbols-outlined shrink-0 text-base text-text-muted">smart_toy</span>
            <div class="flex min-w-0 flex-1 flex-col gap-1">
              <div class="flex items-center gap-1.5 flex-wrap">
                <code class="max-w-[72vw] truncate rounded bg-sidebar px-1.5 py-0.5 font-mono text-xs text-text-muted sm:max-w-[360px]">
                  {fullModelId}
                </code>
                {#if rowCaps?.free}
                  <span class="inline-flex items-center text-[10px] font-semibold px-1.5 py-0.5 rounded bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border border-emerald-500/30">
                    FREE
                  </span>
                {:else if typeof rowCaps?.credit === 'number'}
                  <span
                    class="inline-flex items-center text-[10px] font-semibold px-1.5 py-0.5 rounded bg-surface-3 text-text-muted border border-border"
                    title="Credits per request"
                  >
                    x{rowCaps.credit.toFixed(2)}
                  </span>
                {/if}
                {#if isSessionActive}
                  <span class="inline-flex items-center gap-1 text-[10px] font-semibold px-2 py-0.5 rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border border-emerald-500/30">
                    <span class="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse"></span>
                    Active Session
                  </span>
                {/if}
                <DeprecatedBadge provider={providerId} model={model.id} />
              </div>
              <span class="flex min-w-0 items-center text-[9px] gap-1 pl-1">
                <span class="truncate text-[9px] italic text-text-muted/70">{model.name}</span>
                <span class="inline-flex items-center gap-0.5">
                  {#if rowCaps?.vision}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-text-muted/70" style="font-size: 12px;">visibility</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Vision — Supports image input
                      </div>
                    </div>
                  {/if}
                  {#if rowCaps?.reasoning}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-text-muted/70" style="font-size: 12px;">neurology</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Reasoning — Supports reasoning / thinking
                      </div>
                    </div>
                  {/if}
                  {#if rowCaps?.jailbreak}
                    <div class="relative inline-flex group/tt">
                      <span class="material-symbols-outlined leading-none cursor-help text-red-500/80" style="font-size: 12px;">local_fire_department</span>
                      <div class="pointer-events-none absolute bottom-full left-1/2 -translate-x-1/2 mb-1.5 z-50 w-max max-w-56 rounded px-2 py-1 text-[11px] leading-snug bg-gray-900 text-white opacity-0 group-hover/tt:opacity-100 transition-opacity duration-150 whitespace-normal shadow-lg">
                        Jailbreak — Uncensored / unrestricted
                      </div>
                    </div>
                  {/if}
                </span>
              </span>
            </div>
              {#if modelTestErrors[model.id]}
                <span class="text-[9px] {testStatus === 'blocked' ? 'text-amber-500 dark:text-amber-400' : 'text-red-500 dark:text-red-400'} font-medium pl-1 truncate max-w-[280px]" title={modelTestErrors[model.id]}>
                  {modelTestErrors[model.id]}
                </span>
              {/if}

            <!-- Test button -->
            <div class="relative shrink-0 group/btn">
              <button
                type="button"
                onclick={() => testModel(model.id)}
                disabled={isTestingThis}
                class="rounded p-0.5 text-text-muted transition-opacity hover:bg-sidebar hover:text-primary opacity-100 sm:opacity-0 sm:group-hover:opacity-100 cursor-pointer"
                title={modelTestErrors[model.id] || (testStatus === 'ok' ? 'Test Passed' : 'Test')}
              >
                {#if isTestingThis}
                  <span class="material-symbols-outlined text-sm animate-spin text-primary">progress_activity</span>
                {:else if testStatus === 'ok'}
                  <span class="material-symbols-outlined text-sm text-green-500">check</span>
                {:else if testStatus === 'blocked'}
                  <span class="material-symbols-outlined text-sm text-amber-500">schedule</span>
                {:else if testStatus === 'error'}
                  <span class="material-symbols-outlined text-sm text-red-500">error</span>
                {:else}
                  <span class="material-symbols-outlined text-sm">science</span>
                {/if}
              </button>
              <span class="pointer-events-none absolute mt-1 top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity z-20 bg-surface-2 px-1 rounded shadow border border-border">
                {#if isTestingThis}
                  Testing...
                {:else if testStatus === 'ok'}
                  Passed
                {:else if testStatus === 'blocked'}
                  {modelTestErrors[model.id] || 'Blocked — account in cooldown'}
                {:else if testStatus === 'error'}
                  {modelTestErrors[model.id] || 'Failed'}
                {:else}
                  Test
                {/if}
              </span>
            </div>
            <!-- Copy button -->
            <div class="relative shrink-0 group/btn">
              <button
                type="button"
                onclick={() => copyModelId(model.id)}
                class="rounded p-0.5 text-text-muted hover:bg-sidebar hover:text-primary cursor-pointer"
              >
                {#if copiedModelId === model.id}
                  <span class="material-symbols-outlined text-sm text-green-500">check</span>
                {:else}
                  <span class="material-symbols-outlined text-sm">content_copy</span>
                {/if}
              </button>
              <span class="pointer-events-none absolute mt-1 top-5 left-1/2 -translate-x-1/2 text-[10px] text-text-muted whitespace-nowrap opacity-0 group-hover/btn:opacity-100 transition-opacity">
                Copy
              </span>
            </div>

            <!-- Disable button -->
            <button
              type="button"
              onclick={() => handleDisableModel(model.id)}
              class="ml-auto rounded p-0.5 text-text-muted opacity-100 transition-opacity hover:bg-red-500/10 hover:text-red-500 sm:opacity-0 sm:group-hover:opacity-100 cursor-pointer"
              title="Disable this model"
            >
              <span class="material-symbols-outlined text-sm">close</span>
            </button>
            {#if testStatus === 'error'}
              <!-- Unusable model: delete it. Custom models are removed from the
                   store; built-in registry models are static, so those are
                   disabled instead (the closest equivalent). -->
              <button
                type="button"
                onclick={() => handleDeleteModel(model.id)}
                class="rounded p-0.5 text-red-500 opacity-100 transition-opacity hover:bg-red-500/10 hover:text-red-600 sm:opacity-0 sm:group-hover:opacity-100 cursor-pointer"
                title={model.isCustom ? 'Delete unusable model' : 'Unusable — disable this registry model'}
              >
                <span class="material-symbols-outlined text-sm">delete</span>
              </button>
            {/if}
          </div>
        </div>
      {/each}

      <!-- Add Model button inside the same flex-wrap -->
      <button
        type="button"
        onclick={() => (showAddCustomModelModal = true)}
        class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-primary/40 px-3 py-2 text-xs text-primary transition-colors hover:border-primary hover:bg-primary/5 sm:w-auto cursor-pointer"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        Add Model
      </button>

      {#if (providerId === 'cline' || providerId === 'clinepass' || providerId === 'qoder' || providerId === 'qoder-cn' || providerId === 'commandcode' || providerId === 'vercel-ai-gateway' || providerId === 'chutes' || providerId === 'nvidia' || providerId === 'ollama' || providerId === 'openrouter') && providerConnections.some((c) => c.isActive !== 0)}
        <button
          type="button"
          onclick={handleImportLiveCatalogModels}
          disabled={isImportingLiveCatalogModels}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
        >
          <span class="material-symbols-outlined text-sm {isImportingLiveCatalogModels ? 'animate-spin' : ''}">
            {isImportingLiveCatalogModels ? 'progress_activity' : 'download'}
          </span>
          {isImportingLiveCatalogModels
            ? 'Fetching...'
            : providerId === 'qoder' || providerId === 'qoder-cn'
              ? 'Fetch Qoder Models'
              : 'Import from /models'}
        </button>
      {/if}

      {#if visibleModels.length > 0}
        <button
          type="button"
          onclick={handleCheckAllModels}
          disabled={isCheckingAll}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
          title="Send a minimal request to every model and mark the ones that fail"
        >
          <span class="material-symbols-outlined text-sm">{isCheckingAll ? 'progress_activity' : 'troubleshoot'}</span>
          {isCheckingAll
            ? `Checking ${checkAllProgress.done}/${checkAllProgress.total}...`
            : `Check All Models (${visibleModels.length})`}
        </button>
        <button
          type="button"
          onclick={handleAutoDisableDeadModels}
          disabled={isCheckingAll}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-amber-500/40 bg-amber-500/10 hover:bg-amber-500/20 px-3 py-2 text-xs text-amber-600 transition-colors sm:w-auto cursor-pointer disabled:opacity-50 dark:text-amber-400"
          title="Disable every model whose last probe failed or whose account is in cooldown. They stop appearing in /v1/models and every model picker, and leave the combos they were in."
        >
          <span class="material-symbols-outlined text-sm">heart_broken</span>
          Disable dead models
        </button>
      {/if}

      {#if canListLiveModels && providerConnections.some((c) => c.isActive !== 0)}
        <button
          type="button"
          onclick={handleCheckLatestModels}
          disabled={isCheckingLatest}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
          title="Fetch the provider's live catalog and list models you have not added yet"
        >
          <span class="material-symbols-outlined text-sm">{isCheckingLatest ? 'progress_activity' : 'cached'}</span>
          {isCheckingLatest ? 'Checking...' : 'Check Latest Models'}
        </button>
      {/if}

      {#if providerConnections.some((c) => c.isActive !== 0)}
        <button
          type="button"
          onclick={handleSyncModelDeprecations}
          disabled={isSyncingModels}
          class="flex w-full items-center justify-center gap-1.5 rounded-lg border border-border bg-surface-2 hover:bg-surface-3 px-3 py-2 text-xs text-text-main transition-colors sm:w-auto cursor-pointer disabled:opacity-50"
          title="Re-read the provider's live catalog and clear the Deprecated badge from any model it still serves"
        >
          <span class="material-symbols-outlined text-sm {isSyncingModels ? 'animate-spin' : ''}">
            {isSyncingModels ? 'progress_activity' : 'sync'}
          </span>
          {isSyncingModels ? 'Syncing...' : 'Sync Models'}
        </button>
      {/if}
    </div>

    {#if latestModels.length > 0 || latestError}
      <div class="w-full mt-2 rounded-lg border border-border bg-surface-2 p-3">
        {#if latestModels.length > 0}
          <p class="text-xs text-text-muted mb-2">{latestModels.length} new model(s) not yet added:</p>
          <div class="flex flex-col gap-1.5">
            {#each latestModels as m (m.id)}
              <div class="flex items-center gap-2">
                <code class="flex-1 truncate text-xs font-mono bg-sidebar px-1.5 py-1 rounded">{m.id}</code>
                <button
                  type="button"
                  onclick={() => handleAddLatestModel(m.id, m.name)}
                  class="shrink-0 inline-flex items-center gap-1 rounded px-2 py-1 text-xs text-primary hover:bg-primary/10 border border-primary/40 cursor-pointer"
                >
                  <span class="material-symbols-outlined text-[13px]">add</span>
                  Add
                </button>
              </div>
            {/each}
          </div>
        {:else}
          <p class="text-xs text-text-muted">{latestError}</p>
        {/if}
      </div>
    {/if}
    <!-- Suggested models from provider API — show only models not yet added -->
    {#if suggestedNotAdded.length > 0}
      <div class="w-full mt-2">
        <p class="text-xs text-text-muted mb-2">Suggested free models (≥200k context):</p>
        <div class="flex flex-wrap gap-2">
          {#each suggestedNotAdded as m (m.id)}
            <button
              type="button"
              onclick={() => submitAddCustomModel(m.id)}
              class="flex items-center gap-1 px-2.5 py-1.5 rounded-lg border border-black/10 dark:border-white/10 text-xs text-text-muted hover:text-primary hover:border-primary/40 hover:bg-primary/5 transition-colors cursor-pointer"
              title={m.contextLength ? `${m.name || m.id} · ${Math.round(m.contextLength / 1000)}k ctx` : (m.name || m.id)}
            >
              <span class="material-symbols-outlined text-[13px]">add</span>
              {m.id.split('/').pop()}
            </button>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Disabled Models pills -->
    {#if disabledModelIds.length > 0}
      <div class="w-full mt-4">
        <p class="text-xs text-text-muted mb-2">Disabled models ({disabledModelIds.length}):</p>
        <div class="flex flex-wrap gap-2">
          {#each disabledModelIds as dId}
            <button
              type="button"
              onclick={() => handleEnableModel(dId)}
              class="flex items-center gap-1 px-2.5 py-1.5 rounded-lg border border-black/10 dark:border-white/10 text-xs text-text-muted hover:text-primary hover:border-primary/40 hover:bg-primary/5 transition-colors cursor-pointer"
            >
              <span class="material-symbols-outlined text-[13px]">add</span>
              {dId.split('/').pop()}
            </button>
          {/each}
        </div>
      </div>
    {/if}
  </div>
  {/if}
</div>

<!-- Modals -->

<!-- 1. Risk Notice Modal -->
{#if showRiskNoticeModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 fade-in"
      onclick={() => (showRiskNoticeModal = false)}
      role="presentation"
    ></div>
    <div class="relative w-full bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-elev)] fade-in max-w-md p-6">
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle mb-4">
        <h2 class="text-lg font-semibold text-text-main">Risk Notice</h2>
        <button
          type="button"
          onclick={() => (showRiskNoticeModal = false)}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      <p class="text-xs text-red-600 dark:text-yellow-400 leading-relaxed mb-6">
        ⚠️ Risk Notice: This provider uses a subscription/OAuth session not officially licensed for proxy/router use. Account may be restricted or banned. Use at your own risk.
      </p>
      <div class="flex gap-2 justify-end">
        <button
          type="button"
          onclick={() => (showRiskNoticeModal = false)}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={confirmRiskAndProceed}
          class="px-3 py-1.5 text-xs font-semibold rounded-[8px] bg-red-600 hover:bg-red-700 text-white shadow-sm cursor-pointer"
        >
          I Understand, Continue
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- 2. OAuth / Antigravity Connect Modal -->
{#if showOAuthModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div
        class="absolute inset-0 bg-black/50 fade-in"
        onclick={closeOAuthModal}
        role="presentation"
      ></div>
    <div class="relative w-full bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-elev)] fade-in max-w-lg p-4 sm:p-6">
      <div class="flex items-center justify-between pb-3 border-b border-border-subtle mb-4">
        <h2 class="text-lg font-semibold text-text-main">Connect {providerName}</h2>
        <button
          type="button"
          onclick={closeOAuthModal}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      {#if providerId === 'kiro' && !kiroMethod && !deviceUserCode}
        <p class="text-sm text-text-muted mb-3">Choose your authentication method:</p>
        <div class="space-y-2 mb-4">
          <button type="button" onclick={() => { kiroMethod = 'builder-id'; startKiroDeviceFlow('builder-id') }} disabled={isConnecting} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors disabled:opacity-50 cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">shield</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">AWS Builder ID</h3><p class="text-xs text-text-muted">Recommended. Free AWS account, device-code flow.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'idc' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">business</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">AWS IAM Identity Center</h3><p class="text-xs text-text-muted">Enterprise with your organization start URL.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'api-key' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">key</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">API Key</h3><p class="text-xs text-text-muted">Long-lived headless Kiro/CodeWhisperer key.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'social-google'; startKiroSocial('google') }} disabled={isConnecting} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors disabled:opacity-50 cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">account_circle</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">Google Account</h3><p class="text-xs text-text-muted">Log in with Google, paste callback manually.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'social-github'; startKiroSocial('github') }} disabled={isConnecting} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors disabled:opacity-50 cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">code</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">GitHub Account</h3><p class="text-xs text-text-muted">Log in with GitHub, paste callback manually.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'import' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">file_upload</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">Import Token</h3><p class="text-xs text-text-muted">Paste refresh token from Kiro IDE.</p></div>
            </div>
          </button>
          <button type="button" onclick={() => { kiroMethod = 'import-cli-proxy' }} class="w-full p-3 text-left border border-border rounded-lg hover:bg-sidebar transition-colors cursor-pointer">
            <div class="flex items-start gap-3">
              <span class="material-symbols-outlined text-primary mt-0.5">data_object</span>
              <div class="flex-1"><h3 class="font-semibold text-sm mb-0.5">Import CLIProxyAPI JSON</h3><p class="text-xs text-text-muted">external_idp auth JSON (Microsoft login).</p></div>
            </div>
          </button>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'idc' && !deviceUserCode}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <div>
            <p class="text-xs font-medium mb-1">IDC Start URL <span class="text-red-500">*</span></p>
            <input bind:value={kiroIdcStartUrl} placeholder="https://your-org.awsapps.com/start" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          </div>
          <div>
            <p class="text-xs font-medium mb-1">AWS Region</p>
            <input bind:value={kiroIdcRegion} placeholder="us-east-1" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          </div>
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={() => startKiroDeviceFlow('idc')} disabled={isConnecting || !kiroIdcStartUrl.trim()} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Connecting…' : 'Continue'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'api-key'}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <input bind:value={kiroApiKey} type="password" placeholder="Kiro / CodeWhisperer API key" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <input bind:value={kiroApiKeyRegion} placeholder="Region (default us-east-1)" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroApiKey} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Saving…' : 'Save'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'import'}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <button type="button" onclick={startKiroAutoImport} disabled={isConnecting} class="w-full py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 border border-border disabled:opacity-50 cursor-pointer">{isConnecting ? 'Reading…' : 'Auto-detect from this host'}</button>
          <input bind:value={kiroRefreshToken} placeholder="Refresh token (aorAAAAAG…)" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroImport} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Importing…' : 'Import'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && kiroMethod === 'import-cli-proxy'}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          <textarea bind:value={kiroCliProxyJson} rows={4} placeholder="Paste CLIProxyAPI auth JSON (external_idp)" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"></textarea>
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroCliProxyImport} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Importing…' : 'Import'}</button>
          </div>
        </div>
      {/if}
      {#if providerId === 'kiro' && (kiroMethod === 'social-google' || kiroMethod === 'social-github')}
        <div class="space-y-3 p-3 border border-border rounded-md bg-sidebar/50 mb-4">
          {#if kiroSocialAuthUrl}
            <p class="text-xs text-text-muted">Open this URL, log in, then paste the callback URL (kiro://…) below:</p>
            <div class="flex gap-2">
              <input readonly value={kiroSocialAuthUrl} class="min-w-0 flex-1 px-2.5 h-8 text-xs border border-border rounded-md bg-background text-text-muted select-all font-mono" />
              <button type="button" onclick={() => window.open(kiroSocialAuthUrl, '_blank')} class="flex h-8 shrink-0 items-center px-3 text-xs font-semibold whitespace-nowrap rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Open</button>
            </div>
          {/if}
          <input bind:value={kiroSocialCallback} placeholder="kiro://kiro.kiroAgent/authenticate-success?code=…" class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono" />
          <div class="flex gap-2">
            <button type="button" onclick={() => { kiroMethod = null; kiroSocialAuthUrl = '' }} class="px-3 py-1.5 text-xs rounded-md border border-border bg-surface-2 hover:bg-surface-3 cursor-pointer">Back</button>
            <button type="button" onclick={submitKiroSocialCallback} disabled={isConnecting} class="px-3 py-1.5 text-xs font-semibold rounded-md bg-primary text-white disabled:opacity-50 cursor-pointer">{isConnecting ? 'Exchanging…' : 'Exchange code'}</button>
          </div>
        </div>
      {/if}
      {#if providerId !== 'kiro' || kiroMethod || deviceUserCode}
      <div class="flex items-center gap-2 px-3 py-2 border border-border rounded-lg bg-sidebar/50 mb-4">
        <span class="material-symbols-outlined text-base text-primary animate-spin">progress_activity</span>
        <span class="text-sm">
          {providerId === 'antigravity'
            ? 'Open the login page with the button below, sign in with Google, then paste the code Google shows you.'
            : providerId === 'freebuff'
              ? 'Waiting for Freebuff authorization… (auto-polling active)'
              : deviceUserCode
                ? `Waiting for device authorization… (auto-check every ${deviceInterval}s)`
                : 'Waiting for popup authorization…'}
        </span>
      </div>

      <div class="flex items-center gap-3 my-3">
        <div class="flex-1 h-px bg-border"></div>
        <span class="text-xs text-text-muted uppercase tracking-wider">
          {providerId === 'antigravity'
            ? 'Paste the code from Google'
            : providerId === 'freebuff'
              ? 'Authorization link & manual check'
              : isClineOAuth
                ? 'Log in via Cline, then paste the callback'
                : oauthAuthUrl
                  ? 'Or paste callback URL manually'
                  : 'Manual token import'}
        </span>
        <div class="flex-1 h-px bg-border"></div>
      </div>

      <div class="space-y-4">
        {#if providerId === 'cursor'}
          <div class="space-y-2 p-3 border border-border rounded-md bg-sidebar/50">
            <p class="text-[11px] text-text-muted">Get it from Cursor IDE's <span class="font-mono">state.vscdb</span> (<span class="font-mono">cursorAuth/accessToken</span> + <span class="font-mono">storage.serviceMachineId</span>):</p>
            <input
              bind:value={specialToken}
              placeholder="Access token (cursorAuth/accessToken)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <input
              bind:value={specialExtra}
              placeholder="Machine ID (storage.serviceMachineId)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <button
              type="button"
              onclick={cursorAutoImportNow}
              disabled={isConnecting}
              class="w-full py-1.5 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border disabled:opacity-50 cursor-pointer"
            >
              {isConnecting ? 'Reading…' : 'Auto-import from Cursor on this host'}
            </button>
          </div>
        {/if}
        {#if providerId === 'gitlab'}
          <div class="space-y-2 p-3 border border-border rounded-md bg-sidebar/50">
            <p class="text-[11px] text-text-muted">Or use a Personal Access Token (in addition to the OAuth login above):</p>
            <input
              bind:value={specialToken}
              type="password"
              placeholder="GitLab PAT (scope api, read_user)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <input
              bind:value={specialBaseUrl}
              placeholder="Base URL (default https://gitlab.com)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
          </div>
        {/if}
        {#if providerId === 'iflow'}
          <div class="space-y-2 p-3 border border-border rounded-md bg-sidebar/50">
            <p class="text-[11px] text-text-muted">Or use a platform.iflow.cn cookie (in addition to the OAuth login above):</p>
            <input
              bind:value={specialToken}
              placeholder="Cookie (must contain BXAuth=...)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
          </div>
        {/if}
        {#if deviceUserCode}
          <div class="p-3 border border-border rounded-md bg-sidebar/50 text-center">
            <p class="text-[11px] text-text-muted mb-1">Enter this code on the login page that opened:</p>
            <p class="text-2xl font-mono font-bold tracking-[0.3em] text-text-main select-all">{deviceUserCode}</p>
          </div>
        {/if}
        {#if oauthAuthUrl}
          <div>
            <p class="text-sm font-medium mb-1">Step 1: Open this URL in your browser</p>
            <!-- Stacked on phones: the URL field takes its own row and the
                 two actions share a row of equal halves, so neither button
                 is pushed off the modal's edge nor wraps its label into a
                 tall stack. On sm+ the wrapper dissolves (sm:contents) and
                 the three sit on one row again. -->
            <div class="flex flex-col gap-2 sm:flex-row">
              <input
                readonly
                value={oauthAuthUrl}
                class="min-w-0 flex-1 px-2.5 h-8 text-xs border border-border rounded-md bg-background text-text-muted select-all font-mono"
              />
              <div class="grid grid-cols-2 gap-2 sm:contents">
                <button
                  type="button"
                  onclick={() => window.open(oauthAuthUrl, '_blank')}
                  class="flex h-8 items-center justify-center gap-1 px-3 text-xs font-semibold whitespace-nowrap rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">open_in_new</span>
                  {providerId === 'antigravity' ? 'Open login page' : 'Open'}
                </button>
                <button
                  type="button"
                  onclick={copyAuthUrl}
                  class="flex h-8 items-center justify-center gap-1 px-3 text-xs font-semibold whitespace-nowrap rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">{copiedAuthUrl ? 'check' : 'content_copy'}</span>
                  {copiedAuthUrl ? 'Copied' : 'Copy'}
                </button>
              </div>
            </div>
          </div>
        {/if}

        {#if providerId === 'gitlab'}
          <div class="grid grid-cols-1 gap-2 p-3 border border-border rounded-md bg-sidebar/50 mb-1">
            <p class="text-[11px] text-text-muted">Self-hosted GitLab / own OAuth app (optional — defaults to gitlab.com without a client):</p>
            <input
              bind:value={gitlabBaseUrl}
              placeholder="Base URL (default https://gitlab.com)"
              class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <div class="grid grid-cols-2 gap-2">
              <input
                bind:value={gitlabClientId}
                placeholder="OAuth Client ID"
                class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
              />
              <input
                bind:value={gitlabClientSecret}
                type="password"
                placeholder="OAuth Client Secret"
                class="w-full px-2.5 py-1.5 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
              />
            </div>
          </div>
        {/if}
        {#if !deviceUserCode}
        <div>
          <p class="text-sm font-medium mb-1">
            {providerId === 'antigravity'
              ? 'Step 2: Paste the code from Google here'
              : providerId === 'freebuff'
                ? 'Step 2: Finish in the browser / paste URL / Code / Token'
                : isClineOAuth
                  ? 'Step 2: Paste the callback URL here'
                  : oauthAuthUrl
                    ? 'Step 2: Paste the callback URL here'
                    : 'Manual token import (no browser login for this provider)'}
          </p>
          <div class="flex gap-2">
            <input
              bind:value={callbackInput}
              placeholder={providerId === 'antigravity'
                ? 'Paste the authorization code shown by Google'
                : providerId === 'freebuff'
                  ? 'https://freebuff.com/onboard?auth_code=... or paste authToken'
                  : `${dashboardCallback()}?code=...&state=...`}
              class="min-w-0 flex-1 px-2.5 h-8 text-xs border border-border rounded-md bg-background focus:outline-none focus:border-primary font-mono"
            />
            <button
              type="button"
              onclick={pasteCallbackCode}
              class="flex h-8 shrink-0 items-center gap-1 px-3 text-xs font-semibold whitespace-nowrap rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">content_paste</span>
              Paste
            </button>
          </div>
          {#if pasteNote}
            <p class="text-[11px] text-text-muted mt-1">{pasteNote}</p>
          {/if}
          <p class="text-[11px] text-text-muted mt-1">
            {providerId === 'antigravity'
              ? 'After you sign in, Google’s Antigravity page displays an authorization code. Copy it, paste it above, then click Connect. The code works once — if it expires, close this modal and click Log in again.'
              : providerId === 'freebuff'
                ? 'If the browser lands on /onboard, finish onboarding in the Freebuff tab, then paste the URL above or click Check & Connect.'
                : isClineOAuth
                  ? 'Log in via the Cline tab that opened — the connection completes automatically. If it fails, copy the redirect URL (containing code=...) here and click Connect.'
                  : oauthAuthUrl
                    ? 'Complete the login in the browser tab — the connection completes automatically. If it fails, copy the full URL from the browser here.'
                    : 'Paste the access token here, then click Connect.'}
          </p>
        </div>
        {/if}

        {#if pendingVerification}
          <div>
            <p class="text-sm font-medium mb-1">Step 3: Verify this account</p>
            <div class="flex flex-col gap-2 sm:flex-row">
              <input
                readonly
                value={pendingVerification.url}
                class="min-w-0 flex-1 px-2.5 h-8 text-xs border border-border rounded-md bg-background text-text-muted select-all font-mono"
              />
              <div class="grid grid-cols-2 gap-2 sm:contents">
                <button
                  type="button"
                  onclick={() => window.open(pendingVerification?.url, '_blank')}
                  class="flex h-8 items-center justify-center gap-1 px-3 text-xs font-semibold whitespace-nowrap rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">open_in_new</span>
                  Open
                </button>
                <button
                  type="button"
                  onclick={copyVerifyUrl}
                  class="flex h-8 items-center justify-center gap-1 px-3 text-xs font-semibold whitespace-nowrap rounded-md border border-border bg-surface-2 hover:bg-surface-3 text-text-main cursor-pointer"
                >
                  <span class="material-symbols-outlined text-sm">{copiedVerifyUrl ? 'check' : 'content_copy'}</span>
                  {copiedVerifyUrl ? 'Copied' : 'Copy'}
                </button>
              </div>
            </div>
          </div>
        {/if}

        {#if oauthError}
          <p class="text-xs text-red-500">{oauthError}</p>
        {/if}

        <div class="flex gap-2 pt-2">
          <button
            type="button"
            onclick={() => {
              if (pendingVerification) {
                checkPendingVerification()
              } else if (deviceUserCode) {
                pollDeviceOnce()
              } else {
                submitManualCallback()
              }
            }}
            disabled={isConnecting || isPollingDevice || isCheckingVerification}
            class="flex-1 h-8 text-xs font-semibold rounded-[8px] bg-brand-500 hover:bg-brand-600 text-white shadow-sm disabled:opacity-50 cursor-pointer"
          >
            {isConnecting || isPollingDevice || isCheckingVerification ? 'Checking…' : pendingVerification ? 'Check' : deviceUserCode ? 'Check now' : providerId === 'freebuff' ? 'Check & Connect' : 'Connect'}
          </button>
          <button
            type="button"
            onclick={() => {
              showOAuthModal = false
              pendingVerification = null
              stopDevicePoll()
              if (freebuffPollTimer) {
                clearInterval(freebuffPollTimer)
                freebuffPollTimer = null
              }
              isAuthorizingFreebuff = false
            }}
            class="flex-1 h-8 text-xs font-semibold rounded-[8px] bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
          >
            Cancel
          </button>
        </div>
      </div>
      {/if}
    </div>
  </div>
{/if}



<!-- 5. Add Custom Model Modal -->
{#if !isCompatibleNode}
<AddCustomModelModal
  isOpen={showAddCustomModelModal}
  providerAlias={storageAlias}
  onClose={() => (showAddCustomModelModal = false)}
  onSave={submitAddCustomModel}
  onBulkSaved={handleBulkModelsSaved}
/>
{/if}

<!-- 5b. Edit Compatible Node Modal (upstream EditCompatibleNodeModal parity) -->
<EditCompatibleNodeModal
  isOpen={showEditNodeModal && isCompatibleNode}
  node={selectedNode}
  isAnthropic={isAnthropicCompatibleNode}
  onClose={() => (showEditNodeModal = false)}
  onSave={handleSaveEditedNode}
  onLimitsSaved={(limits) => {
    selectedNode = selectedNode ? { ...selectedNode, limits } : selectedNode
  }}
/>


