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
    providerDisplayName,
    type ActiveRequestItem,
    type RequestDetailItem,
  } from './types'

  interface Props {
    activeRequests?: ActiveRequestItem[]
    period?: string
  }

  let { activeRequests = [], period = 'today' }: Props = $props()

  let rows = $state<RequestDetailItem[]>([])
  let statusFilter = $state<'all' | 'success' | 'failed'>('all')
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

  async function load() {
    try {
      const res = await api.getRequestDetails(20, 0, period)
      if (res && Array.isArray(res.details)) rows = res.details as RequestDetailItem[]
    } catch {
      // A failed poll keeps the previous rows on screen; the next cycle retries.
    }
  }

  $effect(() => {
    void period
    load()
  })

  onMount(() => {
    const poll = setInterval(() => {
      if (!document.hidden) load()
    }, 5000)
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
  let successCount = $derived(rows.filter(isSuccess).length)
  let failedCount = $derived(rows.filter(isFailed).length)
  let accounts = $derived(
    [...new Set(rows.map((r) => r.account).filter((a): a is string => Boolean(a)))].sort()
  )

  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    return rows.filter((row) => {
      if (statusFilter === 'success' && !isSuccess(row)) return false
      if (statusFilter === 'failed' && !isFailed(row)) return false
      if (accountFilter && (row.account || '') !== accountFilter) return false
      if (q) {
        const hay = `${row.model || ''} ${row.provider || ''} ${row.account || ''} ${row.apiKey || ''}`.toLowerCase()
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

  // ActiveRequestItem carries no stream flag, so Axon's STREAM/JSON mode chip
  // cannot be drawn honestly for in-flight rows; the modal shows provider,
  // account and count instead. Elapsed renders only when the row happens to
  // carry a start time.
  function activeStartedAt(r: ActiveRequestItem): string | undefined {
    return (r as unknown as { startedAt?: string }).startedAt
  }
</script>

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

  <!-- Filters. Axon also offers Stream/JSON pills; completed detail rows
       carry no stream flag, so those pills would filter on nothing and are
       deliberately not rendered (no dead controls). -->
  <div class="flex flex-wrap items-center gap-2">
    <div class="inline-flex items-center gap-1" role="group" aria-label="Status filter">
      {#each [{ v: 'all', label: 'All', n: rows.length }, { v: 'success', label: 'Success', n: successCount }, { v: 'failed', label: 'Failed', n: failedCount }] as pill}
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
      <table class="w-full min-w-[760px] border-collapse text-xs">
        <thead>
          <tr class="border-b border-border">
            <th class="py-2 pr-2 text-left font-semibold text-text-muted">Model</th>
            <th class="py-2 pr-2 text-left font-semibold text-text-muted">Provider</th>
            <th class="py-2 pr-2 text-left font-semibold text-text-muted">Upstream Account</th>
            <th class="py-2 pr-2 text-right font-semibold whitespace-nowrap text-text-muted">Tokens In/Out</th>
            <th class="py-2 pr-2 text-right font-semibold text-text-muted">Cost</th>
            <th class="py-2 pr-2 text-right font-semibold text-text-muted">When</th>
            <th class="py-2 text-center font-semibold text-text-muted">Action</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/50">
          {#each filtered as row (row.id ?? `${row.timestamp}-${row.model}`)}
            <tr class="transition-colors hover:bg-surface-2">
              <td class="max-w-[240px] truncate py-2 pr-2 font-mono text-[11px]" title={modelLabel(row)}>{modelLabel(row)}</td>
              <td class="py-2 pr-2 whitespace-nowrap">
                <span class="inline-block rounded-full border border-border bg-surface-2 px-1.5 py-0.5 text-[10px] font-medium text-text-main">{providerDisplayName(row.provider)}</span>
              </td>
              <td class="max-w-[160px] truncate py-2 pr-2" title={row.account || 'Direct (Free)'}>{row.account || 'Direct (Free)'}</td>
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
            <span class="inline-block rounded-full border border-border bg-surface px-1.5 py-0.5 text-[10px] font-medium text-text-main">{providerDisplayName(row.provider)}</span>
            <span>{row.account || 'Direct (Free)'}</span>
            <span class="tabular-nums">{fmtNumberUS(row.tokens?.prompt_tokens)} / {fmtNumberUS(row.tokens?.completion_tokens)}</span>
            <span>{row.cost != null ? fmtCost(row.cost) : '—'}</span>
            <span>{relTime(row.timestamp)}</span>
          </div>
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
    <div class="grid max-h-[72vh] grid-cols-1 gap-2 overflow-y-auto p-1 sm:grid-cols-2 lg:grid-cols-3">
      {#each activeRequests as r, i (i)}
        <div class="rounded-brand border border-border bg-surface-2/50 px-3 py-2">
          <div class="truncate font-mono text-[11px] font-medium" title={activeLabel(r)}>{activeLabel(r)}</div>
          <div class="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-muted">
            <span class="inline-block rounded-full border border-border bg-surface px-1.5 py-0.5 text-[10px] font-medium text-text-main">{providerDisplayName(r.provider)}</span>
            {#if (r.count || 1) > 1}<span>×{r.count}</span>{/if}
            {#if activeStartedAt(r)}<span>{formatDuration(nowMs - new Date(activeStartedAt(r)!).getTime())}</span>{/if}
          </div>
          <div class="mt-1 truncate text-[11px] text-text-muted" title={r.account || 'Direct (Free)'}>
            Account: {r.account || 'Direct (Free)'}
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
