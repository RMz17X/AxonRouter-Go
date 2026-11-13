<script lang="ts">
  // AxonRouter's Realtime Requests card: live in-flight banner on top of the
  // most recent completed requests. Axon reads its completed rows from the
  // lightweight recent feed, but AxonRouter-Go's feed carries no id and no cost,
  // and the error modal needs an id to fetch the trace — so completed rows
  // here come from the request-details ledger (spec §4.3), polled on the
  // card's own 5 s cycle. Active rows still come from the SSE-fed prop.
  import { onMount } from 'svelte'
  import { api } from '../../api/client'
  import { fmtCost, fmtNumberUS } from '../../lib/axonFormat'
  import { copyToClipboard } from '../../lib/clipboard'
  import Modal from '../../lib/ui/Modal.svelte'
  import {
    formatDetailStatus,
    formatDuration,
    formatLocalTimestamp,
    providerAlias,
    providerDisplayName,
    type ActiveRequestItem,
    type RequestDetailItem,
  } from './types'
  import {
    smartDecisionClasses,
    smartDecisionLabel,
    smartDecisionTitle,
  } from './smartDecision'

  interface Props {
    activeRequests?: ActiveRequestItem[]
    period?: string
  }

  let { activeRequests = [], period = 'today' }: Props = $props()

  let rows = $state<RequestDetailItem[]>([])
  let statusFilter = $state<'all' | 'success' | 'failed' | 'stream' | 'json'>('all')
  let accountFilter = $state('')
  let search = $state('')
  let showAllMobile = $state(false)

  let activeModalOpen = $state(false)
  let errorRow = $state<RequestDetailItem | null>(null)
  let errorDetail = $state<RequestDetailItem | null>(null)
  let errorLoading = $state(false)
  let copied = $state(false)
  let copyTimer: ReturnType<typeof setTimeout> | null = null

  // Relative times re-render on a 30 s tick, like Axon's TimeAgo.
  let nowMs = $state(Date.now())

  let inFlight = false
  async function load() {
    if (inFlight) return
    inFlight = true
    try {
      const res = await api.getRequestDetails(20, 0, period)
      if (res && Array.isArray(res.details)) rows = res.details as RequestDetailItem[]
    } catch {
      // A failed poll keeps the previous rows on screen; the next cycle retries.
    } finally {
      inFlight = false
    }
  }

  $effect(() => {
    void period
    load()
  })

  onMount(() => {
    const poll = setInterval(() => {
      if (!document.hidden) load()
    }, 10000)
    const tick = setInterval(() => (nowMs = Date.now()), 30_000)
    return () => {
      clearInterval(poll)
      clearInterval(tick)
    }
  })

  function isSuccess(row: RequestDetailItem): boolean {
    return row.status === 'ok' || row.status === 'success'
  }
  function isFailed(row: RequestDetailItem): boolean {
    return !isSuccess(row) && Boolean(row.status) && row.status !== 'pending'
  }
  function modelLabel(row: RequestDetailItem): string {
    const model = row.model || row.requestedModel || '—'
    return row.combo ? `${row.combo} -> ${model}` : model
  }
  function activeLabel(r: ActiveRequestItem): string {
    const model = r.model || r.requestedModel || '—'
    return r.comboName ? `${r.comboName} -> ${model}` : model
  }
  // The in-flight card draws the provider the way upstream's
  // formatProviderModel does: a coloured alias prefix on the model line
  // ("ag/gemini-…"), not a detached grey pill. A model that already carries
  // the alias (or the raw provider id) as its own prefix loses the
  // duplicate, exactly as upstream strips it.
  function activeModelParts(r: ActiveRequestItem): { alias: string; label: string; combo: string } {
    const alias = providerAlias(r.provider)
    let model = r.model || r.requestedModel || '—'
    const lowerModel = model.toLowerCase()
    if (alias && lowerModel.startsWith(`${alias.toLowerCase()}/`)) {
      model = model.slice(alias.length + 1)
    } else {
      const p = (r.provider || '').toLowerCase()
      if (p && lowerModel.startsWith(`${p}/`)) model = model.slice(p.length + 1)
    }
    // The combo name rides separately so the card can show it as a small
    // "via" line and give the resolved model the full width — embedding it
    // in the label truncated the model name away in narrow grid cells.
    return { alias, label: model, combo: r.comboName || '' }
  }
  function proxyLabel(row: RequestDetailItem): string {
    const p = row.proxy
    if (typeof p === 'string' && p.trim() && p !== 'direct') {
      return p.trim()
    }
    const egress = (row as any).egress
    if (egress) {
      if (typeof egress === 'string' && egress !== 'direct') return egress
      if (typeof egress === 'object') {
        const pool = egress.poolId || egress.poolName || egress.target
        if (pool && pool !== 'direct') return String(pool)
      }
    }
    return 'direct'
  }
  // Axon's realtime rows carry no "ago" suffix.
  function relTime(timestamp?: string): string {
    if (!timestamp) return 'just now'
    const t = new Date(timestamp).getTime()
    if (!Number.isFinite(t)) return 'just now'
    const diffSec = Math.max(0, Math.floor((nowMs - t) / 1000))
    if (diffSec < 60) return 'just now'
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
    return `${Math.floor(diffSec / 86400)}d`
  }

  let activeTotal = $derived(activeRequests.reduce((sum, r) => sum + (r.count || 1), 0))
  // In-flight modal: longest-running first (the requests most worth
  // watching), undated rows last in their original order.
  let sortedActiveRequests = $derived(
    [...activeRequests].sort((a, b) => {
      const sa = activeStartedAt(a)
      const sb = activeStartedAt(b)
      const ta = sa ? new Date(sa).getTime() : Number.POSITIVE_INFINITY
      const tb = sb ? new Date(sb).getTime() : Number.POSITIVE_INFINITY
      return ta - tb
    })
  )
  // Keyed-each rows for the in-flight modal. The tracker splits ONE
  // (model, provider, account) aggregate by response mode and by combo, so
  // two live rows can share provider|model|account while differing only in
  // isStream or comboName. Keying the each on those three fields alone
  // collided whenever that happened (busy moments — exactly when the modal
  // is opened), Svelte threw each_key_duplicate, and the modal never
  // rendered. The key therefore carries every splitting dimension, plus an
  // occurrence suffix so even a fully identical pair cannot collide.
  let keyedActiveRequests = $derived.by(() => {
    const seen = new Map<string, number>()
    return sortedActiveRequests.map((r) => {
      const base = [
        r.provider || '',
        r.model || '',
        r.account || '',
        r.comboName || '',
        r.isStream === true ? 'stream' : 'json',
      ].join('|')
      const n = (seen.get(base) ?? 0) + 1
      seen.set(base, n)
      return { r, key: n === 1 ? base : `${base}#${n}` }
    })
  })
  let activeStreamTotal = $derived(
    activeRequests.reduce((sum, r) => sum + (r.isStream === true ? (r.count || 1) : 0), 0)
  )
  let longestActiveMs = $derived.by(() => {
    let earliest = Number.POSITIVE_INFINITY
    for (const r of activeRequests) {
      const s = activeStartedAt(r)
      if (s) earliest = Math.min(earliest, new Date(s).getTime())
    }
    return earliest === Number.POSITIVE_INFINITY ? null : nowMs - earliest
  })
  let successCount = $derived(rows.filter(isSuccess).length)
  let failedCount = $derived(rows.filter(isFailed).length)
  // Detail rows record the mode the client asked for; rows stored before
  // the flag existed arrive without it and read as streamed (the API's
  // default, upstream's own), so "stream" is everything not marked JSON.
  let streamCount = $derived(rows.filter((r) => r.isStream !== false).length)
  let jsonCount = $derived(rows.filter((r) => r.isStream === false).length)
  let accounts = $derived(
    [...new Set(rows.map((r) => r.account).filter((a): a is string => Boolean(a)))].sort()
  )

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    return rows.filter((row) => {
      if (statusFilter === 'success' && !isSuccess(row)) return false
      if (statusFilter === 'failed' && !isFailed(row)) return false
      if (statusFilter === 'stream' && row.isStream === false) return false
      if (statusFilter === 'json' && row.isStream !== false) return false
      if (accountFilter && (row.account || '') !== accountFilter) return false
      if (q) {
        const hay = `${row.model || ''} ${row.provider || ''} ${row.account || ''} ${row.apiKey || ''} ${row.apiKeyName || ''} ${row.clientApiKey || ''} ${row.clientIp || ''}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      return true
    })
  })
  let mobileRows = $derived(showAllMobile ? filtered : filtered.slice(0, 10))

  async function openError(row: RequestDetailItem) {
    errorRow = row
    errorDetail = null
    copied = false
    if (!row.id) return
    errorLoading = true
    try {
      const res = await api.getRequestDetail(row.id)
      if (errorRow?.id === row.id && res?.detail) {
        errorDetail = { ...row, ...(res.detail as RequestDetailItem) }
      }
    } catch {
      // The modal falls back to the list row's own fields.
    } finally {
      if (errorRow?.id === row.id) errorLoading = false
    }
  }

  function closeError() {
    errorRow = null
    errorDetail = null
    errorLoading = false
    if (copyTimer) clearTimeout(copyTimer)
    copied = false
  }

  let shownDetail = $derived(errorDetail ?? errorRow)
  let errorPayload = $derived.by(() => {
    const d = shownDetail
    if (!d) return ''
    const payload: unknown = d.response ?? (d.error ? { error: d.error } : {})
    return typeof payload === 'string' ? payload : JSON.stringify(payload, null, 2)
  })

  async function copyPayload() {
    if (!errorPayload) return
    const ok = await copyToClipboard(errorPayload)
    if (!ok) return
    copied = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => (copied = false), 2000)
  }

  // The Details section is a state inside AnalyticsView on this same path —
  // a plain link to /dashboard/usage would land back on Overview. Dispatch an
  // event the view can answer by switching sections instead.
  function openRequestDetails() {
    window.dispatchEvent(
      new CustomEvent('open-request-details', { detail: { id: errorRow?.id } })
    )
    closeError()
  }

  // In-flight rows carry the tracked response mode (the tracker splits its
  // aggregates by it), so the card draws Axon's STREAM/JSON chip beside the
  // provider alias prefix. Elapsed renders only when the row happens to
  // carry a start time.
  function activeStartedAt(r: ActiveRequestItem): string | undefined {
    return (r as unknown as { startedAt?: string }).startedAt
  }
</script>

{#snippet modeBadge(isStream: boolean)}
  <span
    class="inline-flex shrink-0 items-center gap-0.5 rounded-brand bg-primary/10 px-1 py-0 text-[9px] leading-[1.5] font-semibold text-primary"
    title={isStream ? 'Streaming response' : 'Non-streaming JSON response'}
  >
    <span class="material-symbols-outlined text-[9px]" aria-hidden="true">{isStream ? 'wifi_tethering' : 'code'}</span>
    {isStream ? 'STREAM' : 'JSON'}
  </span>
{/snippet}

<div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-3 p-4">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h3 class="text-sm font-semibold text-text-main">Realtime Request Stream &amp; Live Activity</h3>
    <span class="inline-flex items-center gap-1.5 text-[11px] font-medium text-success">
      <span class="relative flex h-2 w-2">
        <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-success opacity-75"></span>
        <span class="relative inline-flex h-2 w-2 rounded-full bg-success"></span>
      </span>
      Live Stream
    </span>
  </div>

  {#if activeRequests.length > 0}
    <div class="flex flex-wrap items-center justify-between gap-2 rounded-brand border border-primary/30 bg-primary/10 px-3 py-2">
      <span class="text-xs font-medium text-text-main">Streaming ({activeTotal} active)</span>
      <button
        type="button"
        onclick={() => (activeModalOpen = true)}
        class="cursor-pointer rounded-brand border border-border bg-surface px-2.5 py-1 text-xs font-medium text-text-main transition-colors hover:bg-surface-2"
      >
        View Details
      </button>
    </div>
  {/if}

  <!-- Filters: one single-select pill group, Axon's set — status and the
       recorded response mode share the dimension, as upstream's filterType
       does. -->
  <div class="flex flex-wrap items-center gap-2">
    <div class="inline-flex items-center gap-1" role="group" aria-label="Status filter">
      {#each [{ v: 'all', label: 'All', n: rows.length }, { v: 'stream', label: 'Stream', n: streamCount }, { v: 'json', label: 'JSON', n: jsonCount }, { v: 'success', label: 'Success', n: successCount }, { v: 'failed', label: 'Failed', n: failedCount }] as pill}
        <button
          type="button"
          onclick={() => (statusFilter = pill.v as typeof statusFilter)}
          class="cursor-pointer rounded-full border px-2.5 py-1 text-[11px] font-medium transition-colors {statusFilter === pill.v
            ? 'border-primary/40 bg-primary/10 text-primary'
            : 'border-border text-text-muted hover:bg-surface-2 hover:text-text-main'}"
        >
          {pill.label} ({pill.n})
        </button>
      {/each}
    </div>

    <select
      bind:value={accountFilter}
      aria-label="Filter by account"
      class="cursor-pointer rounded-brand border border-border bg-surface px-2 py-1.5 text-xs text-text-main"
    >
      <option value="">All accounts</option>
      {#each accounts as account (account)}
        <option value={account}>{account}</option>
      {/each}
    </select>

    <label class="relative min-w-0 flex-1 sm:max-w-64">
      <span class="material-symbols-outlined pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-[16px] text-text-muted" aria-hidden="true">search</span>
      <input
        type="text"
        bind:value={search}
        placeholder="Search model, provider, apikey..."
        class="w-full rounded-brand border border-border bg-surface py-1.5 pr-2 pl-8 text-xs text-text-main placeholder:text-text-muted"
      />
    </label>
  </div>

  {#if rows.length === 0}
    <div class="flex items-center justify-center py-8 text-xs text-text-muted">
      Waiting for incoming requests... SSE live listener active.
    </div>
  {:else if filtered.length === 0}
    <div class="flex items-center justify-center py-8 text-xs text-text-muted">
      No requests match the selected filter.
    </div>
  {:else}
    <!-- Desktop table -->
    <div class="hidden overflow-x-auto sm:block">
      <table class="w-full min-w-[700px] border-collapse text-xs">
        <thead>
          <tr class="border-b border-border">
            <th class="py-2 pr-2 text-left font-semibold text-text-muted">Model</th>
            <th class="py-2 pr-2 text-left font-semibold text-text-muted">Upstream Account</th>
            <th class="py-2 pr-2 text-left font-semibold text-text-muted">API Key</th>
            <th class="py-2 pr-2 text-right font-semibold whitespace-nowrap text-text-muted">Tokens In/Out</th>
            <th class="py-2 pr-2 text-right font-semibold text-text-muted">Cost</th>
            <th class="py-2 pr-2 text-right font-semibold text-text-muted">When</th>
            <th class="py-2 text-center font-semibold text-text-muted">Action</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/50">
          {#each filtered as row (row.id ?? `${row.timestamp}-${row.model}`)}
            <tr class="transition-colors hover:bg-surface-2">
              <td class="max-w-[240px] py-2 pr-2">
                <div class="truncate font-mono text-[11px]" title={modelLabel(row)}>{modelLabel(row)}</div>
                <div class="mt-1 flex items-center gap-1.5">
                  {@render modeBadge(row.isStream !== false)}
                  <span class="inline-block rounded-full border border-border bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium text-text-main">{providerDisplayName(row.provider)}</span>
                  {#if row.smartDecision}
                    <span class="inline-flex items-center rounded-full border px-1 py-0 font-sans text-[9px] leading-[1.5] font-semibold {smartDecisionClasses(row.smartDecision)}" title={smartDecisionTitle(row.smartDecision)}>
                      {smartDecisionLabel(row.smartDecision)}
                    </span>
                  {/if}
                </div>
              </td>
              <td class="max-w-[160px] py-2 pr-2">
                <div class="truncate" title={row.account || 'Direct (Free)'}>{row.account || 'Direct (Free)'}</div>
                <div class="mt-1">
                  {#if proxyLabel(row) !== 'direct'}
                    <span class="inline-block rounded-full border border-primary/40 bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary" title={proxyLabel(row)}>{proxyLabel(row)}</span>
                  {:else}
                    <span class="inline-block rounded-full border border-border bg-surface-2 px-1.5 py-0.5 text-[10px] text-text-muted">direct</span>
                  {/if}
                </div>
              </td>
              <td class="max-w-[180px] py-2 pr-2">
                <div class="truncate font-medium text-text-main" title={row.clientApiKey || row.apiKeyName || undefined}>{row.apiKeyName || row.clientApiKey || '—'}</div>
                <div class="mt-0.5 truncate font-mono text-[10px] text-text-muted" title={row.clientIp || undefined}>{row.clientIp || '—'}</div>
              </td>
              <td class="py-2 pr-2 text-right whitespace-nowrap tabular-nums">{fmtNumberUS(row.tokens?.prompt_tokens)} / {fmtNumberUS(row.tokens?.completion_tokens)}</td>
              <td class="py-2 pr-2 text-right whitespace-nowrap tabular-nums">{row.cost != null ? fmtCost(row.cost) : '—'}</td>
              <td class="py-2 pr-2 text-right whitespace-nowrap text-text-muted">{relTime(row.timestamp)}</td>
              <td class="py-2 text-center whitespace-nowrap">
                {#if isFailed(row)}
                  <button
                    type="button"
                    onclick={() => openError(row)}
                    class="cursor-pointer rounded-brand border border-danger/40 px-2 py-1 text-[11px] font-medium text-danger transition-colors hover:bg-danger/10"
                  >
                    Show Error
                  </button>
                {:else}
                  <span class="text-text-muted">—</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Mobile cards, capped at 10 until expanded -->
    <div class="flex flex-col gap-2 sm:hidden">
      {#each mobileRows as row (row.id ?? `${row.timestamp}-${row.model}`)}
        <div class="rounded-brand border border-border bg-surface-2/50 px-3 py-2">
          <div class="truncate font-mono text-[11px]" title={modelLabel(row)}>{modelLabel(row)}</div>
          <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-muted">
            {@render modeBadge(row.isStream !== false)}
            <span class="inline-block rounded-full border border-border bg-surface px-1.5 py-0.5 text-[10px] font-medium text-text-main">{providerDisplayName(row.provider)}</span>
            {#if row.smartDecision}
              <span class="inline-flex items-center rounded-full border px-1 py-0 font-sans text-[9px] leading-[1.5] font-semibold {smartDecisionClasses(row.smartDecision)}" title={smartDecisionTitle(row.smartDecision)}>
                {smartDecisionLabel(row.smartDecision)}
              </span>
            {/if}
            <span>{row.account || 'Direct (Free)'}</span>
            {#if proxyLabel(row) !== 'direct'}
              <span class="inline-block rounded-full border border-primary/40 bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary" title={proxyLabel(row)}>{proxyLabel(row)}</span>
            {:else}
              <span class="inline-block rounded-full border border-border bg-surface px-1.5 py-0.5 text-[10px] text-text-muted">direct</span>
            {/if}
            <span class="tabular-nums">{fmtNumberUS(row.tokens?.prompt_tokens)} / {fmtNumberUS(row.tokens?.completion_tokens)}</span>
            <span>{row.cost != null ? fmtCost(row.cost) : '—'}</span>
            <span>{relTime(row.timestamp)}</span>
          </div>
          {#if row.apiKeyName || row.clientApiKey || row.clientIp}
            <div class="mt-1 truncate text-[10px] text-text-muted" title={row.clientApiKey || undefined}>
              Key: {row.apiKeyName || row.clientApiKey}{#if row.clientIp} · {row.clientIp}{/if}
            </div>
          {/if}
          {#if isFailed(row)}
            <button
              type="button"
              onclick={() => openError(row)}
              class="mt-2 cursor-pointer rounded-brand border border-danger/40 px-2 py-1 text-[11px] font-medium text-danger transition-colors hover:bg-danger/10"
            >
              Show Error
            </button>
          {/if}
        </div>
      {/each}
      {#if filtered.length > 10}
        <button
          type="button"
          onclick={() => (showAllMobile = !showAllMobile)}
          class="cursor-pointer self-center px-2 py-1 text-[11px] font-medium text-primary hover:underline"
        >
          {showAllMobile ? 'Show fewer' : `Show ${filtered.length - 10} more of ${filtered.length}`}
        </button>
      {/if}
    </div>
  {/if}
</div>

<!-- Active in-flight requests -->
<Modal isOpen={activeModalOpen} onClose={() => (activeModalOpen = false)} title="Active In-Flight Requests" icon="stream" size="full">
  {#if activeRequests.length === 0}
    <div class="py-8 text-center text-xs text-text-muted">No active in-flight requests</div>
  {:else}
    <div class="flex flex-wrap items-center gap-x-3 gap-y-1 px-1 pb-2 text-[11px] text-text-muted">
      <span class="inline-flex items-center gap-1.5">
        <span class="relative flex h-2 w-2">
          <span class="absolute inline-flex h-full w-full animate-ping rounded-full bg-success opacity-75"></span>
          <span class="relative inline-flex h-2 w-2 rounded-full bg-success"></span>
        </span>
        <span><span class="font-semibold text-text-main">{activeTotal}</span> in-flight</span>
      </span>
      <span>{activeRequests.length} groups</span>
      <span>{activeStreamTotal} streaming</span>
      {#if longestActiveMs !== null}
        <span>longest <span class="font-mono text-text-main tabular-nums">{formatDuration(longestActiveMs)}</span></span>
      {/if}
    </div>
    <div class="grid grid-cols-1 gap-2.5 p-1 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
      {#each keyedActiveRequests as entry (entry.key)}
        {@const r = entry.r}
        {@const parts = activeModelParts(r)}
        <div class="flex min-w-0 flex-col gap-2 rounded-brand border border-border bg-surface-2/30 px-3 py-2.5 transition-colors hover:bg-surface-2/50">
          <div class="flex items-center justify-between gap-2">
            <span class="truncate text-[11px] text-text-muted" title={providerDisplayName(r.provider)}>
              {providerDisplayName(r.provider)}
            </span>
            {@render modeBadge(r.isStream === true)}
          </div>
          <div class="min-w-0">
            {#if parts.combo}
              <div class="flex items-center gap-1 text-[10px] text-text-muted">
                <span class="material-symbols-outlined text-[11px]" aria-hidden="true">arrow_forward</span>
                <span class="truncate">via <span class="font-medium text-text-main">{parts.combo}</span></span>
              </div>
            {/if}
            <div
              class="mt-0.5 font-mono text-xs font-medium break-all text-text-main"
              title="{providerDisplayName(r.provider)} · {activeLabel(r)}"
            >
              {#if parts.alias}<span class="font-semibold text-primary">{parts.alias}/</span>{/if}<span>{parts.label}</span>
            </div>
          </div>
          <div class="mt-auto flex items-center justify-between gap-2 border-t border-border/50 pt-2">
            <div class="flex min-w-0 flex-1 items-center gap-1.5" title={r.account || 'Direct (Free)'}>
              <span class="material-symbols-outlined shrink-0 text-[13px] text-text-muted/70" aria-hidden="true">account_circle</span>
              <span class="truncate text-[11px] text-text-main">{r.account || 'Direct (Free)'}</span>
            </div>
            <div class="flex shrink-0 items-center gap-1.5">
              {#if (r.count || 1) > 1}
                <span class="rounded-full border border-border bg-surface px-1.5 py-0.5 text-[10px] font-semibold text-text-main tabular-nums" title="{r.count} concurrent requests in this group">×{r.count}</span>
              {/if}
              {#if activeStartedAt(r)}
                <span class="inline-flex items-center gap-1 font-mono text-[10px] text-text-muted tabular-nums" title="Elapsed">
                  <span class="h-1.5 w-1.5 animate-pulse rounded-full bg-success"></span>
                  {formatDuration(nowMs - new Date(activeStartedAt(r)!).getTime())}
                </span>
              {/if}
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</Modal>

<!-- Request error details -->
<Modal isOpen={errorRow !== null} onClose={closeError} title="Request Error Details" icon="error" size="lg">
  {#if shownDetail}
    <div class="flex flex-col gap-3 p-1">
      <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
        <dt class="font-semibold text-text-muted">Status:</dt>
        <dd class="text-text-main">{formatDetailStatus(shownDetail)}</dd>
        <dt class="font-semibold text-text-muted">Timestamp:</dt>
        <dd class="text-text-main">{formatLocalTimestamp(shownDetail.timestamp)}</dd>
        <dt class="font-semibold text-text-muted">Model:</dt>
        <dd class="truncate font-mono text-[11px] text-text-main" title={modelLabel(shownDetail)}>{modelLabel(shownDetail)}</dd>
        <dt class="font-semibold text-text-muted">Provider:</dt>
        <dd class="text-text-main">{providerDisplayName(shownDetail.provider)}</dd>
        <dt class="font-semibold text-text-muted">Account:</dt>
        <dd class="text-text-main">{shownDetail.account || shownDetail.connectionId || 'Direct'}</dd>
        {#if shownDetail.apiKey}
          <dt class="font-semibold text-text-muted">API Key:</dt>
          <dd class="truncate font-mono text-[11px] text-text-main" title={shownDetail.apiKey}>{shownDetail.apiKey}</dd>
        {/if}
      </dl>

      <div class="flex items-center justify-between">
        <span class="text-xs font-semibold text-text-main">Error Response Payload</span>
        <button
          type="button"
          onclick={copyPayload}
          class="inline-flex cursor-pointer items-center gap-1 rounded-brand border border-border px-2 py-1 text-[11px] font-medium text-text-main transition-colors hover:bg-surface-2"
        >
          <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{copied ? 'check' : 'content_copy'}</span>
          {copied ? 'Copied!' : 'Copy'}
        </button>
      </div>
      {#if errorLoading}
        <div class="py-4 text-center text-xs text-text-muted">Loading error trace...</div>
      {:else}
        <pre class="max-h-[300px] overflow-auto rounded-brand border border-border bg-surface-2 p-3 font-mono text-[11px] break-all whitespace-pre-wrap text-text-main">{errorPayload}</pre>
      {/if}

      <div class="flex items-center justify-end gap-2">
        <button
          type="button"
          onclick={openRequestDetails}
          class="cursor-pointer px-2 py-1 text-[11px] font-medium text-primary hover:underline"
        >
          Open Request Details
        </button>
        <button
          type="button"
          onclick={closeError}
          class="cursor-pointer rounded-brand border border-border px-2.5 py-1 text-xs font-medium text-text-main transition-colors hover:bg-surface-2"
        >
          Close
        </button>
      </div>
    </div>
  {/if}
</Modal>
