<script lang="ts">
  // AxonRouter's Breakdown UsageTable: one dimension at a time (Model /
  // Account / API Key / Endpoint — Axon has no Provider dimension here),
  // expandable group rows over per-model detail rows, Costs/Tokens modes with
  // token-share cost allocation (cached tokens peeled out of input first).
  // Owner-ordered addition (Ricki, 2026-11-04): a Provider dimension on top
  // of Axon's four — it regroups the byModel leaf map by each leaf's provider,
  // with the models that ran on it as the detail rows.
  //
  // Grouping follows the stats payload's own shape, the same one the previous
  // breakdown table consumed: every map entry is one (dimension value × model
  // × provider) leaf, keyed composites like "rawModel (provider)". A group is
  // the dimension value (rawModel / accountName / keyName / endpoint); its
  // detail rows are the leaves under it.
  import { api } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import { fmtNumberUS, fmtCost } from '../../lib/axonFormat'
  import type { StatsData, UsageItem } from './types'

  interface Props {
    period: string
  }

  let { period }: Props = $props()

  type Dimension = 'model' | 'provider' | 'account' | 'apiKey' | 'endpoint'
  type Mode = 'tokens' | 'costs'
  type SortKey = 'name' | 'requests' | 'in' | 'cached' | 'out' | 'total'

  const DIMENSIONS: { value: Dimension; label: string; view: string; empty: string }[] = [
    { value: 'model', label: 'Model', view: 'model', empty: 'No usage recorded yet.' },
    // Provider reuses the model view: its groups are derived from the same
    // leaf map, keyed by each leaf's provider instead of its model.
    { value: 'provider', label: 'Provider', view: 'model', empty: 'No provider usage recorded yet.' },
    { value: 'account', label: 'Account', view: 'account', empty: 'No account-specific usage recorded yet.' },
    { value: 'apiKey', label: 'API Key', view: 'apiKey', empty: 'No API key usage recorded yet.' },
    { value: 'endpoint', label: 'Endpoint', view: 'endpoint', empty: 'No endpoint usage recorded yet.' },
  ]
  const DETAIL_CAP = 100

  let dimension = $state<Dimension>('model')
  let mode = $state<Mode>('tokens')
  let sortKey = $state<SortKey>('requests')
  let sortDir = $state<'asc' | 'desc'>('desc')

  // The last successfully loaded payload and the dimension it belongs to. A
  // dimension or period switch keeps those rows on screen (dimmed, aria-busy)
  // until the replacement lands, instead of flashing an empty table.
  let stats = $state<StatsData>({})
  let statsDim = $state<Dimension>('model')
  let hasLoaded = $state(false)
  let loading = $state(true)
  let failed = $state(false)
  let refreshTick = $state(0)

  $effect(() => {
    const wantedPeriod = period
    const wantedDim = dimension
    void refreshTick
    let cancelled = false
    loading = true
    failed = false
    const view = DIMENSIONS.find((d) => d.value === wantedDim)?.view ?? 'model'
    api
      .getUsageStats(wantedPeriod, view)
      .then((data) => {
        if (cancelled) return
        stats = (data ?? {}) as StatsData
        statsDim = wantedDim
        hasLoaded = true
        loading = false
      })
      .catch(() => {
        if (cancelled) return
        failed = true
        loading = false
      })
    return () => {
      cancelled = true
    }
  })

  interface DetailRow {
    key: string
    item: UsageItem
    label: string
    sub?: string
  }

  interface Group {
    key: string
    label: string
    rows: DetailRow[]
    requests: number
    promptTokens: number
    completionTokens: number
    cachedTokens: number
    cost: number
  }

  function groupKeyOf(dim: Dimension, key: string, item: UsageItem): string {
    switch (dim) {
      case 'model':
        return item.rawModel || key
      case 'provider':
        return item.provider || 'unknown'
      case 'account':
        return item.accountName || key
      case 'apiKey':
        return item.keyName || key
      case 'endpoint':
        return item.endpoint || key
    }
  }

  function detailLabelOf(dim: Dimension, key: string, item: UsageItem): string {
    // Model groups break down by provider; every other dimension breaks down
    // by the model that produced the leaf.
    if (dim === 'model') return item.provider || 'unknown'
    return item.rawModel || key
  }

  function detailSubOf(dim: Dimension, item: UsageItem): string | undefined {
    // Provider groups already name the provider; repeating it as every
    // row's sub would be noise. Model rows carry no sub either.
    if (dim === 'model' || dim === 'provider') return undefined
    return item.provider || undefined
  }

  const groups = $derived.by((): Group[] => {
    const sourceMap =
      statsDim === 'model' || statsDim === 'provider'
        ? stats.byModel
        : statsDim === 'account'
          ? stats.byAccount
          : statsDim === 'apiKey'
            ? stats.byApiKey
            : stats.byEndpoint
    const byGroup = new Map<string, Group>()
    for (const [key, item] of Object.entries(sourceMap || {})) {
      const gk = groupKeyOf(statsDim, key, item)
      let group = byGroup.get(gk)
      if (!group) {
        group = {
          key: gk,
          label: gk,
          rows: [],
          requests: 0,
          promptTokens: 0,
          completionTokens: 0,
          cachedTokens: 0,
          cost: 0,
        }
        byGroup.set(gk, group)
      }
      group.rows.push({ key, item, label: detailLabelOf(statsDim, key, item), sub: detailSubOf(statsDim, item) })
      group.requests += item.requests || 0
      group.promptTokens += item.promptTokens || 0
      group.completionTokens += item.completionTokens || 0
      group.cachedTokens += item.cachedTokens || 0
      group.cost += item.cost || 0
    }
    for (const group of byGroup.values()) {
      group.rows.sort((a, b) => (b.item.requests || 0) - (a.item.requests || 0))
    }
    const dir = sortDir === 'asc' ? 1 : -1
    return [...byGroup.values()].sort((a, b) => {
      if (sortKey === 'name') return dir * a.label.localeCompare(b.label)
      return dir * (groupSortValue(a) - groupSortValue(b))
    })
  })

  // Axon's cost split: cached tokens are peeled OUT of input first, then the
  // row's cost is allocated across the three parts by token share.
  function costSplit(row: { promptTokens?: number; completionTokens?: number; cachedTokens?: number; cost?: number }) {
    const cached = row.cachedTokens || 0
    const effIn = Math.max(0, (row.promptTokens || 0) - cached)
    const out = row.completionTokens || 0
    const total = effIn + cached + out
    const cost = row.cost || 0
    if (total <= 0 || cost === 0) return { inCost: 0, cachedCost: 0, outCost: 0 }
    return {
      inCost: (cost * effIn) / total,
      cachedCost: (cost * cached) / total,
      outCost: (cost * out) / total,
    }
  }

  function groupSortValue(g: Group): number {
    switch (sortKey) {
      case 'requests':
        return g.requests
      case 'in':
        return mode === 'tokens' ? g.promptTokens : costSplit(g).inCost
      case 'cached':
        return mode === 'tokens' ? g.cachedTokens : costSplit(g).cachedCost
      case 'out':
        return mode === 'tokens' ? g.completionTokens : costSplit(g).outCost
      case 'total':
        return mode === 'tokens' ? g.promptTokens + g.completionTokens : g.cost
      default:
        return g.requests
    }
  }

  function toggleSort(key: SortKey) {
    if (sortKey === key) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc'
      return
    }
    sortKey = key
    sortDir = key === 'name' ? 'asc' : 'desc'
  }

  function sortIcon(key: SortKey): string {
    if (sortKey !== key) return 'unfold_more'
    return sortDir === 'asc' ? 'arrow_upward' : 'arrow_downward'
  }

  function ariaSort(key: SortKey): 'ascending' | 'descending' | 'none' {
    if (sortKey !== key) return 'none'
    return sortDir === 'asc' ? 'ascending' : 'descending'
  }

  // Tokens mode: input/output render "—" when the field is absent, cached
  // renders "—" whenever it is falsy (Axon's UsageTable summary-row rules).
  function tokenCell(value: number | undefined, dashWhenFalsy = false): string {
    if (value === undefined) return '—'
    if (dashWhenFalsy && !value) return '—'
    return fmtNumberUS(value)
  }

  // Expanded groups persist per dimension, keyed like Axon's UsageTable
  // storageKey behaviour.
  const storageKey = $derived(`axon-breakdown-expanded-${statsDim}`)
  let expanded = $state<Set<string>>(new Set())

  $effect(() => {
    const key = storageKey
    try {
      const raw = localStorage.getItem(key)
      expanded = new Set(raw ? (JSON.parse(raw) as string[]) : [])
    } catch {
      expanded = new Set()
    }
  })

  function toggleGroup(key: string) {
    const next = new Set(expanded)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    expanded = next
    try {
      localStorage.setItem(storageKey, JSON.stringify([...next]))
    } catch {}
  }

  const activeDimMeta = $derived(DIMENSIONS.find((d) => d.value === dimension) ?? DIMENSIONS[0])
  const shownDimMeta = $derived(DIMENSIONS.find((d) => d.value === statsDim) ?? DIMENSIONS[0])
  const showEmpty = $derived(hasLoaded && !loading && statsDim === dimension && groups.length === 0 && !failed)
</script>

<div class="flex flex-col gap-3">
  <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
    <div class="flex items-center gap-2">
      <select
        bind:value={dimension}
        aria-label="Breakdown dimension"
        class="w-full sm:w-auto rounded-brand border border-border bg-surface px-3 py-1.5 text-sm font-semibold text-text-main focus:outline-none focus:ring-2 focus:ring-primary/50 cursor-pointer"
      >
        {#each DIMENSIONS as opt (opt.value)}
          <option value={opt.value}>Usage by {opt.label}</option>
        {/each}
      </select>
      {#if loading && hasLoaded}
        <span class="inline-flex items-center gap-1 text-[11px] text-text-muted">
          <span class="material-symbols-outlined animate-spin text-[14px]" aria-hidden="true">progress_activity</span>
          Loading…
        </span>
      {/if}
    </div>

    <div class="inline-flex rounded-brand border border-border bg-surface-2 p-0.5 self-start sm:self-auto">
      <button
        type="button"
        onclick={() => (mode = 'costs')}
        class="rounded-brand px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {mode === 'costs'
          ? 'bg-primary text-white'
          : 'text-text-muted hover:text-text-main'}"
      >
        Costs
      </button>
      <button
        type="button"
        onclick={() => (mode = 'tokens')}
        class="rounded-brand px-3 py-1 text-xs font-semibold transition-colors cursor-pointer {mode === 'tokens'
          ? 'bg-primary text-white'
          : 'text-text-muted hover:text-text-main'}"
      >
        Tokens
      </button>
    </div>
  </div>

  {#if failed}
    <div class="flex items-center justify-between gap-3 rounded-brand border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger" role="alert">
      <span>Couldn't load usage data.</span>
      <button
        type="button"
        onclick={() => refreshTick++}
        class="inline-flex items-center gap-1 font-semibold hover:underline cursor-pointer"
      >
        <span class="material-symbols-outlined text-[16px]" aria-hidden="true">refresh</span>
        Retry
      </button>
    </div>
  {/if}

  <Card padding="none" class="overflow-hidden">
    <div aria-busy={loading} class={loading && hasLoaded ? 'opacity-60 transition-opacity' : ''}>
      {#if !hasLoaded && loading}
        <div class="flex items-center justify-center py-12 text-xs text-text-muted">
          <span class="material-symbols-outlined animate-spin text-[20px] mr-2" aria-hidden="true">progress_activity</span>
          Loading…
        </div>
      {:else if showEmpty}
        <div class="p-8 text-center text-sm text-text-muted">{activeDimMeta.empty}</div>
      {:else if groups.length === 0}
        <div class="p-8 text-center text-sm text-text-muted">{shownDimMeta.empty}</div>
      {:else}
        <!-- Desktop table -->
        <div class="hidden overflow-x-auto sm:block">
          <table class="w-full border-collapse text-left text-xs">
            <thead class="border-b border-border bg-surface-2 uppercase">
              <tr>
                <th class="py-3 px-4" aria-sort={ariaSort('name')}>
                  <button type="button" class="inline-flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider cursor-pointer select-none hover:text-text-main {sortKey === 'name' ? 'text-text-main' : 'text-text-muted'}" onclick={() => toggleSort('name')}>
                    {shownDimMeta.label}
                    <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{sortIcon('name')}</span>
                  </button>
                </th>
                <th class="py-3 px-4" aria-sort={ariaSort('requests')}>
                  <button type="button" class="inline-flex w-full items-center justify-end gap-1 text-[10px] font-semibold uppercase tracking-wider cursor-pointer select-none hover:text-text-main {sortKey === 'requests' ? 'text-text-main' : 'text-text-muted'}" onclick={() => toggleSort('requests')}>
                    Requests
                    <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{sortIcon('requests')}</span>
                  </button>
                </th>
                <th class="py-3 px-4" aria-sort={ariaSort('in')}>
                  <button type="button" class="inline-flex w-full items-center justify-end gap-1 text-[10px] font-semibold uppercase tracking-wider cursor-pointer select-none hover:text-text-main {sortKey === 'in' ? 'text-text-main' : 'text-text-muted'}" onclick={() => toggleSort('in')}>
                    {mode === 'tokens' ? 'Input Tokens' : 'Input Cost'}
                    <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{sortIcon('in')}</span>
                  </button>
                </th>
                <th class="py-3 px-4" aria-sort={ariaSort('cached')}>
                  <button type="button" class="inline-flex w-full items-center justify-end gap-1 text-[10px] font-semibold uppercase tracking-wider cursor-pointer select-none hover:text-text-main {sortKey === 'cached' ? 'text-text-main' : 'text-text-muted'}" onclick={() => toggleSort('cached')}>
                    {mode === 'tokens' ? 'Cached' : 'Cached Cost'}
                    <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{sortIcon('cached')}</span>
                  </button>
                </th>
                <th class="py-3 px-4" aria-sort={ariaSort('out')}>
                  <button type="button" class="inline-flex w-full items-center justify-end gap-1 text-[10px] font-semibold uppercase tracking-wider cursor-pointer select-none hover:text-text-main {sortKey === 'out' ? 'text-text-main' : 'text-text-muted'}" onclick={() => toggleSort('out')}>
                    {mode === 'tokens' ? 'Output Tokens' : 'Output Cost'}
                    <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{sortIcon('out')}</span>
                  </button>
                </th>
                <th class="py-3 px-4" aria-sort={ariaSort('total')}>
                  <button type="button" class="inline-flex w-full items-center justify-end gap-1 text-[10px] font-semibold uppercase tracking-wider cursor-pointer select-none hover:text-text-main {sortKey === 'total' ? 'text-text-main' : 'text-text-muted'}" onclick={() => toggleSort('total')}>
                    {mode === 'tokens' ? 'Total Tokens' : 'Total Cost'}
                    <span class="material-symbols-outlined text-[14px]" aria-hidden="true">{sortIcon('total')}</span>
                  </button>
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border/60">
              {#each groups as group (group.key)}
                {@const split = costSplit(group)}
                <tr class="bg-surface-2/50 font-medium">
                  <td class="py-2.5 px-4 text-text-main">
                    <button
                      type="button"
                      onclick={() => toggleGroup(group.key)}
                      aria-expanded={expanded.has(group.key)}
                      class="inline-flex max-w-full items-center gap-1.5 cursor-pointer hover:text-primary"
                    >
                      <span class="material-symbols-outlined text-[18px] shrink-0" aria-hidden="true">
                        {expanded.has(group.key) ? 'expand_more' : 'chevron_right'}
                      </span>
                      <span class="truncate font-mono">{group.label}</span>
                    </button>
                  </td>
                  <td class="py-2.5 px-4 text-right font-mono font-semibold text-text-main">{fmtNumberUS(group.requests)}</td>
                  {#if mode === 'tokens'}
                    <td class="py-2.5 px-4 text-right font-mono">{tokenCell(group.promptTokens)}</td>
                    <td class="py-2.5 px-4 text-right font-mono">{tokenCell(group.cachedTokens, true)}</td>
                    <td class="py-2.5 px-4 text-right font-mono">{tokenCell(group.completionTokens)}</td>
                    <td class="py-2.5 px-4 text-right font-mono font-semibold">{fmtNumberUS(group.promptTokens + group.completionTokens)}</td>
                  {:else}
                    <td class="py-2.5 px-4 text-right font-mono">{fmtCost(split.inCost)}</td>
                    <td class="py-2.5 px-4 text-right font-mono">{fmtCost(split.cachedCost)}</td>
                    <td class="py-2.5 px-4 text-right font-mono">{fmtCost(split.outCost)}</td>
                    <td class="py-2.5 px-4 text-right font-mono font-semibold">{fmtCost(group.cost)}</td>
                  {/if}
                </tr>
                {#if expanded.has(group.key)}
                  {#each group.rows.slice(0, DETAIL_CAP) as row (row.key)}
                    {@const rSplit = costSplit(row.item)}
                    <tr class="hover:bg-surface-2/40 transition-colors">
                      <td class="py-2 px-4 pl-11 text-text-muted">
                        <span class="font-mono">{row.label}</span>
                        {#if row.sub}<span class="ml-1.5 text-[11px] text-text-subtle">{row.sub}</span>{/if}
                      </td>
                      <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtNumberUS(row.item.requests)}</td>
                      {#if mode === 'tokens'}
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{tokenCell(row.item.promptTokens)}</td>
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{tokenCell(row.item.cachedTokens, true)}</td>
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{tokenCell(row.item.completionTokens)}</td>
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtNumberUS((row.item.promptTokens || 0) + (row.item.completionTokens || 0))}</td>
                      {:else}
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtCost(rSplit.inCost)}</td>
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtCost(rSplit.cachedCost)}</td>
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtCost(rSplit.outCost)}</td>
                        <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtCost(row.item.cost)}</td>
                      {/if}
                    </tr>
                  {/each}
                  {#if group.rows.length > DETAIL_CAP}
                    <tr>
                      <td colspan="6" class="py-2 px-4 pl-11 text-[11px] text-text-subtle">Showing first 100 — refine filters</td>
                    </tr>
                  {/if}
                {/if}
              {/each}
            </tbody>
          </table>
        </div>

        <!-- Mobile card list -->
        <div class="flex flex-col divide-y divide-border/60 sm:hidden">
          {#each groups as group (group.key)}
            {@const split = costSplit(group)}
            <div class="p-3">
              <button
                type="button"
                onclick={() => toggleGroup(group.key)}
                aria-expanded={expanded.has(group.key)}
                class="flex w-full items-center gap-1.5 text-left cursor-pointer"
              >
                <span class="material-symbols-outlined text-[18px] shrink-0 text-text-muted" aria-hidden="true">
                  {expanded.has(group.key) ? 'expand_more' : 'chevron_right'}
                </span>
                <span class="min-w-0 flex-1 truncate font-mono text-xs font-semibold text-text-main">{group.label}</span>
                <span class="font-mono text-xs text-text-main">{fmtNumberUS(group.requests)} req</span>
              </button>
              <div class="mt-2 grid grid-cols-4 gap-2 pl-6 text-[11px]">
                <div>
                  <div class="text-text-subtle">{mode === 'tokens' ? 'Input' : 'In cost'}</div>
                  <div class="font-mono text-text-main">{mode === 'tokens' ? tokenCell(group.promptTokens) : fmtCost(split.inCost)}</div>
                </div>
                <div>
                  <div class="text-text-subtle">Cached</div>
                  <div class="font-mono text-text-main">{mode === 'tokens' ? tokenCell(group.cachedTokens, true) : fmtCost(split.cachedCost)}</div>
                </div>
                <div>
                  <div class="text-text-subtle">{mode === 'tokens' ? 'Output' : 'Out cost'}</div>
                  <div class="font-mono text-text-main">{mode === 'tokens' ? tokenCell(group.completionTokens) : fmtCost(split.outCost)}</div>
                </div>
                <div>
                  <div class="text-text-subtle">Total</div>
                  <div class="font-mono font-semibold text-text-main">
                    {mode === 'tokens' ? fmtNumberUS(group.promptTokens + group.completionTokens) : fmtCost(group.cost)}
                  </div>
                </div>
              </div>
              {#if expanded.has(group.key)}
                <div class="mt-2 flex flex-col gap-1.5 pl-6">
                  {#each group.rows.slice(0, DETAIL_CAP) as row (row.key)}
                    <div class="flex items-center justify-between gap-2 rounded-brand bg-surface-2/60 px-2 py-1.5 text-[11px]">
                      <span class="min-w-0 truncate">
                        <span class="font-mono text-text-main">{row.label}</span>
                        {#if row.sub}<span class="ml-1 text-text-subtle">{row.sub}</span>{/if}
                      </span>
                      <span class="shrink-0 font-mono text-text-muted">
                        {fmtNumberUS(row.item.requests)} req ·
                        {mode === 'tokens'
                          ? fmtNumberUS((row.item.promptTokens || 0) + (row.item.completionTokens || 0))
                          : fmtCost(row.item.cost)}
                        {#if mode === 'costs'}<span class="sr-only">total cost</span>{/if}
                        {#if mode === 'tokens'}<span class="sr-only">total tokens</span>{/if}
                      </span>
                    </div>
                  {/each}
                  {#if group.rows.length > DETAIL_CAP}
                    <p class="text-[11px] text-text-subtle">Showing first 100 — refine filters</p>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </Card>
</div>
