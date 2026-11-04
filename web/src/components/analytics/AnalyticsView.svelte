<script lang="ts">
  import { api, getAuthHeaders, normalizeLastError, type ProviderConnection, type ProviderNode } from '../../api/client'
  import { PROVIDER_CATALOG } from '../../lib/providers'
  import {
    USAGE_SECTION_BY_TAB,
    USAGE_SECTION_BY_VALUE,
    type ActiveTab,
    type UsageSection,
  } from '../../lib/router'
  import {
    type Period,
    type StatsData,
    type RequestDetailItem,
    type ActiveRequestItem,
    type RecentRequestItem
  } from './types'
  import CacheAnalyticsView from '../CacheAnalyticsView.svelte'
  import CompressionAnalyticsView from '../CompressionAnalyticsView.svelte'
  import PeriodSelect from './PeriodSelect.svelte'
  import SectionMenu from './SectionMenu.svelte'
  import RequestDetailsTab from './RequestDetailsTab.svelte'
  import AxonKpiCards from './AxonKpiCards.svelte'
  import ProviderTopologyCard from './ProviderTopologyCard.svelte'
  import AxonRecentRequests from './AxonRecentRequests.svelte'
  import AxonRequestStream from './AxonRequestStream.svelte'
  import AxonRealtime from './AxonRealtime.svelte'
  import AxonVolumeGrid from './AxonVolumeGrid.svelte'
  import AxonBreakdownTable from './AxonBreakdownTable.svelte'
  import AxonComboRouting from './AxonComboRouting.svelte'
  import { isPanelVisible } from '../../lib/usagePanel.svelte'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  interface Props {
    connections?: ProviderConnection[]
    providerNodes?: ProviderNode[]
    tab?: ActiveTab
    onNavigate?: (tab: ActiveTab) => void
  }

  let { connections = [], providerNodes = [], tab = 'analytics', onNavigate = () => {} }: Props = $props()

  // Cache and Compression Analytics became sections of this page (issue #200),
  // each with a tab of its own, so a reload on /dashboard/usage/cache restores
  // that section. Overview and Details share one path and keep their state
  // here instead.
  // The initial value is read once, on purpose: `tab` is the tab the router
  // resolved on mount, and re-reading it later would yank the user off a
  // section they picked after a browser-back navigation restored the old path.
  // svelte-ignore state_referenced_locally
  let section = $state<UsageSection>(USAGE_SECTION_BY_TAB[tab] ?? 'overview')
  let period = $state<Period>('today')
  let isFetching = $state(false)

  // The Overview mirrors AxonRouter's Usage Overview tab: a fixed layout with
  // three sub-tabs (Overview / Breakdown / Combo Routing) instead of the old
  // arrangement-driven panels. The arrangement system now only governs the
  // Details section's panels.
  let overviewTab = $state<'overview' | 'breakdown' | 'combo'>('overview')
  // Below lg the topology and Recent Requests collapse into one panel with a
  // switch, as Axon's Overview does; at lg and up both render side by side.
  let liveSwitch = $state<'topology' | 'recent'>('topology')
  // Axon's Overview period set. The Details section keeps the wider
  // PeriodSelect; both write the same `period` state.
  const AXON_PERIODS: { value: Period; label: string }[] = [
    { value: 'today', label: 'Today' },
    { value: '24h', label: '24h' },
    { value: '7d', label: '7D' },
    { value: '30d', label: '30D' },
    { value: '60d', label: '60D' },
  ]

  // Choosing a section has to move the URL when that section owns one, or the
  // next reload silently drops back to Overview.
  $effect(() => {
    const desired = USAGE_SECTION_BY_VALUE[section]
    if (desired && desired !== tab) onNavigate(desired)
  })

  let stats = $state<StatsData>({})
  let activeRequests = $state<ActiveRequestItem[]>([])
  let pulseProvider = $state<string>('')
  let lastProvider = $state<string>('')
  let errorProvider = $state<string>('')

  // A failed stats read leaves the previous period's numbers on screen, and
  // those read as live. Since #148 the server answers 500 instead of a zeroed
  // body, so the failure arrives here as a thrown error: keep the message, and
  // let the template show the stale data as stale rather than as current.
  let statsError = $state('')
  let detailsError = $state('')
  let pulseTimer: ReturnType<typeof setTimeout> | null = null

  function triggerPulse(provider: string) {
    if (!provider) return
    pulseProvider = provider
    if (pulseTimer) clearTimeout(pulseTimer)
    pulseTimer = setTimeout(() => {
      pulseProvider = ''
    }, 3000)
  }

  // Topology presence rule (Ricki, 2026-11-04): a provider belongs on the
  // map only while it is active or its last request is under five minutes
  // old; after five quiet minutes it leaves the map on its own. The clock
  // below is what lets nodes expire without any new data arriving, and
  // observedActivity remembers when each provider was last seen doing
  // something (live requests, pulses, errors, recent rows) so the
  // derivation in topologyProviders can judge freshness even for activity
  // the period-scoped stats maps have already rotated past.
  let topologyNow = $state(Date.now())
  let observedActivity = $state(new Map<string, number>())

  // The last errorProvider value the presence effect below has stamped.
  // Plain (untracked) on purpose: it is bookkeeping for the effect, not
  // rendered state.
  let prevErrorProvider = ''

  $effect(() => {
    const t = setInterval(() => {
      topologyNow = Date.now()
    }, 5000)
    return () => clearInterval(t)
  })

  $effect(() => {
    const stamps = new Map<string, number>()
    const stamp = (provider: string, at: number) => {
      if (!provider || Number.isNaN(at)) return
      const key = provider.trim().toLowerCase()
      if (key && at > (stamps.get(key) || 0)) stamps.set(key, at)
    }
    const now = Date.now()
    for (const r of activeRequests) stamp(r.provider || '', now)
    stamp(pulseProvider, now)
    // errorProvider is sticky — it names the most recent erroring provider
    // and is never cleared back to '' — so it is stamped only when its
    // value changes. Stamping it with "now" on every run (this effect
    // re-runs on every stats poll and SSE merge) kept an errored
    // provider's presence fresh forever, and the five-minute rule never
    // expired it (review cycle 3, A-1). A different provider erroring
    // re-stamps; the previous one's stamp ages out on the normal rule.
    if (errorProvider !== prevErrorProvider) {
      stamp(errorProvider, now)
      prevErrorProvider = errorProvider
    }
    for (const r of stats.recentRequests || []) {
      if (r.provider) stamp(r.provider, r.timestamp ? new Date(r.timestamp).getTime() : NaN)
    }
    let changed = false
    const next = new Map(observedActivity)
    for (const [k, v] of stamps) {
      if (v > (next.get(k) || 0)) {
        next.set(k, v)
        changed = true
      }
    }
    if (changed) observedActivity = next
  })
  // mergeRecent unions an incoming SSE list with what is on screen. The SSE
  // stream carries only this process's in-memory ring, so a plain replace
  // collapses the DB-backed list (20 rows after a REST load) down to the few
  // rows seen since the last restart — the list visibly blinks and rows below
  // vanish. Union + dedupe + newest-first keeps rows the stream has not seen.
  function mergeRecent(
    prev: RecentRequestItem[] | undefined,
    next: RecentRequestItem[] | undefined,
  ): RecentRequestItem[] {
    const byKey = new Map<string, RecentRequestItem>()
    const keyOf = (r: RecentRequestItem) =>
      `${r.model}|${r.provider}|${r.promptTokens}|${r.completionTokens}|${(r.timestamp || '').slice(0, 16)}`
    for (const r of prev || []) byKey.set(keyOf(r), r)
    for (const r of next || []) byKey.set(keyOf(r), r)
    return [...byKey.values()]
      .sort((a, b) => (b.timestamp || '').localeCompare(a.timestamp || ''))
      .slice(0, 20)
  }
  // Request details tab state
  let details = $state<RequestDetailItem[]>([])
  let detailsTotal = $state(0)
  let detailsPage = $state(1)
  let detailsLoading = $state(false)
  /**
   * Reads the usage aggregates for a window.
   *
   * `views` names the breakdowns the server should build. The five-second poll
   * passes 'none' (totals, activeRequests and recentRequests only) because that
   * is all the KPI row and the live pulse draw, while the four per-model maps
   * are 98% of the payload — 415 KB at 411 models, every five seconds. The
   * breakdown table is refreshed on its own, slower schedule and keeps its
   * previous maps in between, so a poll that omits them must merge rather than
   * replace: replacing would blank the table for four seconds out of every
   * five.
   *
   * Calls overlap by design (the section/period effect, the 5 s totals
   * poll, the 30 s full read), so each call captures a sequence number and
   * only the latest call applies its result, its error, or the isFetching
   * reset: a slow full read that resolves after a fresher poll would
   * otherwise land on the wholesale path below, overwrite the poll's
   * totals, and resurrect just-finished active requests (review cycle 3,
   * A-2). A superseded full read's breakdown maps simply stay stale until
   * the next 30 s read.
   */
  let loadStatsSeq = 0
  async function loadStats(targetPeriod: Period, views?: string) {
    const seq = ++loadStatsSeq
    isFetching = true
    try {
      const res = await api.getUsageStats(targetPeriod, views)
      if (res && seq === loadStatsSeq) {
        statsError = ''
        // A totals-only poll carries empty breakdown maps, so only the fields
        // it actually fills are merged. A plain spread would blank the
        // breakdown table for four seconds out of every five.
        stats =
          views === 'none'
            ? {
                ...stats,
                totalRequests: res.totalRequests,
                totalPromptTokens: res.totalPromptTokens,
                totalCompletionTokens: res.totalCompletionTokens,
                totalCachedTokens: res.totalCachedTokens,
                totalCost: res.totalCost,
                totalErrors: res.totalErrors,
                successRate: res.successRate,
                byProvider: res.byProvider,
                activeRequests: res.activeRequests,
                recentRequests: res.recentRequests,
                errorProvider: res.errorProvider,
                last10Minutes: res.last10Minutes,
                pending: res.pending,
              }
            : res
        if (Array.isArray(res.activeRequests)) {
          activeRequests = res.activeRequests
        }
        if (!lastProvider) {
          if (Array.isArray(res.activeRequests) && res.activeRequests.length > 0 && res.activeRequests[0].provider) {
            lastProvider = res.activeRequests[0].provider
          } else if (Array.isArray(res.recentRequests) && res.recentRequests.length > 0) {
            lastProvider = res.recentRequests[0].provider || ''
          }
        }
        if (res.errorProvider) {
          errorProvider = res.errorProvider
        }
      }
    } catch (err) {
      // A superseded call's failure is not the screen's problem: a newer
      // call is in flight or has already landed.
      if (seq === loadStatsSeq) {
        // A broken read used to arrive as a zeroed 200 and land on screen as
        // "no traffic this period". The server now answers 500, so the failure
        // has to be named here or it vanishes into the console while the
        // previous period's numbers keep being read as current.
        statsError = normalizeLastError(err) || 'Usage could not be loaded.'
        console.error('Failed to load usage stats:', err)
      }
    } finally {
      if (seq === loadStatsSeq) {
        isFetching = false
      }
    }
  }

  async function loadDetails(page = 1, window: Period = period) {
    detailsLoading = true
    detailsError = ''
    try {
      const limit = 20
      const offset = (page - 1) * limit
      const res = await api.getRequestDetails(limit, offset, window)
      if (res && Array.isArray(res.details)) {
        details = res.details
        detailsTotal = res.total || 0
        detailsPage = page
      }
    } catch (err) {
      detailsError = normalizeLastError(err) || 'Request details could not be loaded.'
      console.error('Failed to load request details:', err)
    } finally {
      detailsLoading = false
    }
  }

  // The usage and details fetches follow the Overview and Details sections.
  // The moved Cache and Compression sections load their own data, so the
  // Overview's read must not run behind them.
  $effect(() => {
    if (section === 'overview') loadStats(period)
  })

  // The Realtime card's error modal deep-links here: it cannot route to the
  // Details section itself (the section is state on this same path), so it
  // dispatches a window event and this listener performs the switch.
  $effect(() => {
    const openDetails = () => {
      section = 'details'
    }
    window.addEventListener('open-request-details', openDetails)
    return () => window.removeEventListener('open-request-details', openDetails)
  })

  $effect(() => {
    // The paged request table is one of the panels the arrangement can hide.
    // With it hidden the section draws no rows, so the read would only cost a
    // request and the page weight of a table nobody sees (issue #506).
    if (section === 'details' && isPanelVisible('details.requests')) {
      loadDetails(detailsPage, period)
    }
  })

  // SSE real-time updates for activeRequests, recentRequests and error notifications
  $effect(() => {
    // The live stream fills the Overview's panels and nothing else, so it only
    // runs while the Overview section is on screen.
    if (section !== 'overview') return
    let isCancelled = false
    let controller: AbortController | null = null
    let reconnectTimeout: ReturnType<typeof setTimeout> | null = null

    const token = typeof localStorage !== 'undefined' ? localStorage.getItem('9router_key') || '' : ''
    let streamInitialized = false
    // Reconnect backoff for the stream: doubles from 3 s to a 30 s cap
    // (plus jitter) across consecutive failures and resets on a successful
    // connect. The fixed 3 s retry it replaces ran at full rate against a
    // dead gateway forever, and every open dashboard reconnected in
    // lockstep after an outage (review cycle 3, A-3).
    let reconnectDelayMs = 3000

    const connectStream = async () => {
      if (isCancelled) return
      controller = new AbortController()

      try {
        const streamUrl = token ? `/api/usage/stream?key=${encodeURIComponent(token)}` : '/api/usage/stream'
        const res = await fetch(streamUrl, {
          headers: getAuthHeaders(),
          signal: controller.signal,
        })

        if (!res.ok) {
          throw new Error(`usage stream failed: ${res.status}`)
        }

        const reader = res.body?.getReader()
        const decoder = new TextDecoder()
        if (!reader) return
        // The stream is up: the next failure starts the backoff over.
        reconnectDelayMs = 3000

        let buffer = ''
        while (!isCancelled) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            const trimmed = line.trim()
            if (!trimmed || trimmed.startsWith(':')) continue
            if (!trimmed.startsWith('data: ')) continue

            try {
              const data = JSON.parse(trimmed.slice(6))
              if (Array.isArray(data.recentRequests) && data.recentRequests.length > 0) {
                const prevTop = stats.recentRequests?.[0]
                const newTop = data.recentRequests[0]
                // Only pulse animation when a GENUINE new model request arrives AFTER stream initialization
                if (streamInitialized && prevTop) {
                  const isNewRequest =
                    newTop.timestamp !== prevTop.timestamp ||
                    newTop.model !== prevTop.model ||
                    newTop.tokens !== prevTop.tokens
                  if (isNewRequest && newTop.provider) {
                    lastProvider = newTop.provider
                    triggerPulse(newTop.provider)
                  }
                } else if (!lastProvider && newTop.provider) {
                  lastProvider = newTop.provider
                }
                streamInitialized = true
                stats = { ...stats, recentRequests: mergeRecent(stats.recentRequests, data.recentRequests) }
              }
              if (Array.isArray(data.activeRequests)) {
                activeRequests = data.activeRequests
                stats = { ...stats, activeRequests: data.activeRequests }
                if (data.activeRequests.length > 0 && data.activeRequests[0].provider) {
                  lastProvider = data.activeRequests[0].provider
                }
              }
              if (data.errorProvider) {
                errorProvider = data.errorProvider
              }
            } catch (err) {
              console.error('Failed to parse SSE usage stream:', err)
            }
          }
        }
      } catch (err) {
        if (!isCancelled) {
          const jitter = Math.floor(Math.random() * 1000)
          reconnectTimeout = setTimeout(connectStream, reconnectDelayMs + jitter)
          reconnectDelayMs = Math.min(reconnectDelayMs * 2, 30000)
        }
      }
    }

    connectStream()

    // Auto-poll stats every 5s so KPI counters smoothly increment in real time.
    //
    // The poll asks for totals only. The four per-model breakdown maps are 98%
    // of the payload (415 KB at 411 models) and the breakdown table draws one
    // of them at a time, so rebuilding all four every five seconds was 29 MB a
    // minute of JSON nobody was looking at. They are refreshed below on a
    // slower tick, and on a period change (which reads everything).
    const pollTimer = setInterval(() => {
      if (section === 'overview' && (typeof document === 'undefined' || !document.hidden)) {
        loadStats(period, 'none')
      }
    }, 5000)

    // A full read on a slower tick keeps the topology catalog's byProvider
    // window current (the Breakdown sub-tab fetches its own maps now).
    const breakdownTimer = setInterval(() => {
      if (section === 'overview' && (typeof document === 'undefined' || !document.hidden)) {
        loadStats(period)
      }
    }, 30000)

    return () => {
      isCancelled = true
      if (controller) controller.abort()
      if (reconnectTimeout) clearTimeout(reconnectTimeout)
      if (pulseTimer) clearTimeout(pulseTimer)
      clearInterval(pollTimer)
      clearInterval(breakdownTimer)
    }
  })
  let nodeNameById = $derived.by(() => {
    const m = new Map<string, string>()
    for (const n of providerNodes || []) {
      if (n?.id && n?.name) {
        m.set(n.id, n.name)
        // Also index by lowercased id so lookups from lowercased keys still match
        m.set(n.id.toLowerCase().trim(), n.name)
      }
    }
    return m
  })

  function topologyName(providerId: string, fallbackName?: string): string {
    // The fallback is a connection name, and for OAuth connections that is
    // the account email — mask it the same way the connection lists do.
    const raw = (providerId || '').trim()
    const resolvedName = nodeNameById.get(raw) ?? nodeNameById.get(raw.toLowerCase())
    const nodeName = formatEmailLabel(resolvedName ?? '', $emailPrivacy)
    if (nodeName) return nodeName
    const cat = PROVIDER_CATALOG.find(
      (p) => p.id === raw || p.alias === raw || p.id.toLowerCase() === raw.toLowerCase() || p.alias?.toLowerCase() === raw.toLowerCase()
    )
    if (cat?.name) return cat.name
    // Only fall back to the connection label if it isn't a numeric ID (a
    // connection label like "12") and isn't an OAuth account email: an email
    // is the connection's identity, not the provider's name, so it must never
    // stand in as the topology node label even with privacy masking off.
    if (fallbackName && fallbackName !== providerId) {
      const trimmed = fallbackName.trim()
      if (!/^\d+$/.test(trimmed) && !trimmed.includes('@')) return formatEmailLabel(fallbackName, $emailPrivacy)
      return providerId
    }
    return providerId
  }

  let topologyProviders = $derived.by(() => {
    const seen = new Set<string>()
    const list: { id: string; alias?: string; name: string; color?: string; type: string }[] = []

    // "Who is on the bus" is a presence question, and presence expires:
    // every source below is gated on the provider's last activity being
    // under five minutes old (the rule stated at topologyNow above), NOT
    // on the selected stats period — the period drives the KPIs and
    // tables, never who is on the map. The backend keys usage by the
    // canonical provider id, while a connection row can hold either that
    // or the published alias, so both are tried before a provider counts
    // as idle.
    const TOPOLOGY_RETENTION_MS = 5 * 60_000
    const activityMs = (provId: string) => {
      const raw = provId.trim()
      const canonical = raw.toLowerCase()
      const cat = PROVIDER_CATALOG.find((p) => p.id.toLowerCase() === canonical || p.alias?.toLowerCase() === canonical)
      // A custom node id is stored verbatim by the backend ("openai-compatible-chat-Wildy"),
      // so `stats.byProvider` is keyed by that raw case. Check both raw and lowercased
      // variants alongside catalog ids so mixed-case nodes aren't judged idle.
      const keys = [raw, canonical, cat?.id, cat?.alias].filter(Boolean) as string[]
      let latest = 0
      for (const k of keys) {
        const lastUsed = stats.byProvider?.[k]?.lastUsed
        if (lastUsed) {
          const t = Date.parse(lastUsed)
          if (!Number.isNaN(t) && t > latest) latest = t
        }
        const observed = observedActivity.get(k.toLowerCase())
        if (observed && observed > latest) latest = observed
      }
      return latest
    }
    const fresh = (provId: string) => {
      const t = activityMs(provId)
      return t > 0 && topologyNow - t <= TOPOLOGY_RETENTION_MS
    }
    const addProvider = (provId: string, type: string, customName?: string) => {
      if (!provId) return
      const raw = provId.trim()
      const canonical = raw.toLowerCase()
      const cat = PROVIDER_CATALOG.find((p) => p.id.toLowerCase() === canonical || (p.alias && p.alias.toLowerCase() === canonical))
      // A provider the registry hides stays routable, but it has no place in a
      // map that reads as "who is on the bus" — the same reason it is kept out
      // of the provider list.
      if (cat?.hidden) return
      // Custom compatible nodes keep the operator's casing ("...-Wildy") so
      // node-name lookups, icon paths, and usage history agree with what the
      // database actually stored.
      const targetId = cat?.id || raw
      if (seen.has(canonical) || seen.has(targetId)) return
      seen.add(targetId)
      seen.add(canonical)
      if (cat?.alias) seen.add(cat.alias.toLowerCase())

      list.push({
        id: targetId,
        alias: cat?.alias,
        name: topologyName(targetId, customName),
        color: cat?.color || '#3B82F6',
        type
      })
    }

    // 1. Prioritize active & live providers so lines to models in use never get dropped
    for (const r of activeRequests) {
      if (r.provider) addProvider(r.provider, 'active')
    }
    if (pulseProvider) addProvider(pulseProvider, 'active')
    if (lastProvider && fresh(lastProvider)) addProvider(lastProvider, 'recent')
    if (errorProvider && fresh(errorProvider)) addProvider(errorProvider, 'error')

    // 2. Add recent requests — recent in the five-minute sense, not in
    //    the "latest N rows" sense the feed itself uses.
    for (const r of stats.recentRequests || []) {
      if (r.provider && fresh(r.provider)) addProvider(r.provider, 'recent')
    }

    // 3. Add user-configured connections, but only the ones carrying
    //    traffic inside the retention window. Every non-inactive row used
    //    to be drawn here, and since that list is not usage-windowed the
    //    node outlived both the period and the connection itself: a
    //    provider the operator had merely configured sat on the map
    //    indefinitely as if it were serving (#280). The five-minute gate
    //    keeps that protection and tightens it — configured is not
    //    present, and neither is "used earlier today".
    for (const c of connections) {
      if (c.isActive !== 0 && c.provider && fresh(c.provider)) {
        addProvider(c.provider, 'connection', c.name || undefined)
      }
    }

    // 4. Add providers whose latest recorded usage is still fresh
    if (stats.byProvider) {
      for (const prov of Object.keys(stats.byProvider)) {
        if (fresh(prov)) addProvider(prov, 'stats')
      }
    }

    // 5. Free/no-auth defaults, but only once they have actually been used.
    //    Upstream #4615: no-auth providers store no connection, so the old
    //    unconditional pass drew every catalog free provider on a map that
    //    reads as "who is on the bus" — including ones never routed a request.
    //    Providers with real usage already entered above via stats.byProvider,
    //    so this only has to cover the ids whose usage key the map lacks.
    const FREE_DEFAULTS = ['antigravity', 'opencode', 'nvidia', 'openrouter', 'clinepass']
    for (const f of FREE_DEFAULTS) {
      if (fresh(f)) addProvider(f, 'default')
    }

    return list
  })
</script>

<!-- The section picker, defined once here and handed to the sections that put
     it in their own header row beside their own controls (issue #209). -->
{#snippet sectionMenu()}
  <SectionMenu {section} onSectionChange={(next) => (section = next)} />
{/snippet}



<div class="flex min-w-0 flex-col gap-6 px-1 sm:px-0">
  <!-- The section picker and the window selector. Cache Analytics and
       Compression Analytics each put the picker in their own header, beside the
       controls they own (issue #209). Details shares this row's control, which
       windows the request table through ?period; the panels above it keep the
       window they label in place (lifetime is all time, Node Health is 7 days),
       so no number here changes meaning silently. -->
  {#if section === 'overview' || section === 'details'}
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      {@render sectionMenu()}

      {#if section === 'overview'}
        <!-- Axon's period segmented control: exactly its five windows. -->
        <div
          class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm self-start sm:self-auto"
          role="group"
          aria-label="Usage period"
        >
          {#each AXON_PERIODS as p (p.value)}
            <button
              type="button"
              onclick={() => (period = p.value)}
              class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {period === p.value
                ? 'bg-brand-500 text-white shadow-sm'
                : 'text-text-muted hover:text-text-main'}"
            >
              {p.label}
            </button>
          {/each}
        </div>
      {:else}
        <PeriodSelect value={period} busy={isFetching} onChange={(next) => (period = next)} />
      {/if}
    </div>
  {/if}

  <!-- A failed read leaves the previous period's numbers below, so say what
       happened and mark them stale rather than let them read as current. The
       retry is the same load the refresh button already calls. -->
  {#if section === 'overview' && statsError}
    <div
      role="alert"
      class="flex flex-col gap-2 rounded-brand-lg border border-red-500/30 bg-surface px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
    >
      <div class="flex min-w-0 items-start gap-2">
        <span class="material-symbols-outlined mt-px text-[18px] text-red-600 dark:text-red-400" aria-hidden="true">error</span>
        <div class="min-w-0">
          <p class="text-sm font-medium text-text-main">Usage could not be loaded. The figures below are from the last successful read.</p>
          <p class="mt-0.5 break-words text-[11px] text-text-muted">{statsError}</p>
        </div>
      </div>
      <button
        type="button"
        onclick={() => loadStats(period)}
        disabled={isFetching}
        class="shrink-0 self-start rounded-lg border border-border px-3 py-1.5 text-xs font-medium text-text-main transition-colors hover:bg-surface-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/50 disabled:opacity-50 sm:self-auto"
      >
        {isFetching ? 'Retrying…' : 'Retry'}
      </button>
    </div>
  {/if}

  {#if section === 'overview'}
    <!-- AxonRouter's Usage Overview tab, ported: three sub-tabs with a fixed
         layout. Breakdown and Combo Routing fetch their own data; the
         Overview sub-tab draws the stats this view already polls. -->
    <div
      class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm self-start"
      role="group"
      aria-label="Overview sub-tab"
    >
      {#each [['overview', 'Overview'], ['breakdown', 'Breakdown'], ['combo', 'Combo Routing']] as [value, label] (value)}
        <button
          type="button"
          onclick={() => (overviewTab = value as typeof overviewTab)}
          class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {overviewTab === value
            ? 'bg-brand-500 text-white shadow-sm'
            : 'text-text-muted hover:text-text-main'}"
        >
          {label}
        </button>
      {/each}
    </div>

    {#if overviewTab === 'overview'}
      <AxonKpiCards {stats} />

      <!-- Topology + Recent Requests: side by side at lg (2fr/1fr); below lg
           a switch shows one at a time, as Axon's Overview does. -->
      <div class="flex flex-col gap-2">
        <div
          class="inline-flex rounded-xl bg-surface border border-border p-1 shadow-sm self-start lg:hidden"
          role="group"
          aria-label="Live panel"
        >
          {#each [['topology', 'Topology'], ['recent', 'Recent']] as [value, label] (value)}
            <button
              type="button"
              onclick={() => (liveSwitch = value as typeof liveSwitch)}
              class="rounded-lg px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {liveSwitch === value
                ? 'bg-brand-500 text-white shadow-sm'
                : 'text-text-muted hover:text-text-main'}"
            >
              {label}
            </button>
          {/each}
        </div>
        <div class="grid min-w-0 grid-cols-1 items-stretch gap-2 lg:grid-cols-[minmax(0,2fr)_minmax(280px,1fr)]">
          <div class="min-w-0 {liveSwitch === 'recent' ? 'hidden lg:block' : ''}">
            <ProviderTopologyCard
              providers={topologyProviders}
              {activeRequests}
              {pulseProvider}
              {lastProvider}
              {errorProvider}
              onRefresh={() => loadStats(period)}
            />
          </div>
          <div class="min-w-0 {liveSwitch === 'topology' ? 'hidden lg:block' : ''}">
            <AxonRecentRequests recentRequests={stats.recentRequests || []} />
          </div>
        </div>
      </div>

      <AxonRequestStream last10Minutes={stats.last10Minutes} />
      <AxonRealtime {activeRequests} {period} />
    {:else if overviewTab === 'breakdown'}
      <AxonVolumeGrid {period} />
      <AxonBreakdownTable {period} />
    {:else}
      <AxonComboRouting {period} />
    {/if}
  {:else if section === 'cache'}
    <CacheAnalyticsView>
      {#snippet headerLeft()}
        {@render sectionMenu?.()}
      {/snippet}
    </CacheAnalyticsView>
  {:else if section === 'compression'}
    <CompressionAnalyticsView>
      {#snippet headerLeft()}
        {@render sectionMenu?.()}
      {/snippet}
    </CompressionAnalyticsView>
  {:else}
    <RequestDetailsTab
      {details}
      {detailsTotal}
      {detailsPage}
      {detailsLoading}
      {detailsError}
      onPageChange={loadDetails}
      onRefresh={() => loadDetails(detailsPage)}
      {providerNodes}
      {connections}
    />
  {/if}
</div>
