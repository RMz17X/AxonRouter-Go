<script lang="ts">
  // AxonRouter's "Volume Grid" (its UsageChart): a block/waffle grid, not a
  // line chart. One block is a fixed share of the peak bucket in the period;
  // columns are time buckets, folded contiguously when there are more than 31.
  import { api } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import { formatTokensID, fmtCost } from '../../lib/axonFormat'

  interface Props {
    period: string
  }

  let { period }: Props = $props()

  interface ChartBucket {
    label: string
    tokens: number
    cost: number
  }

  const ROWS = 12
  const MAX_COLS = 31

  let mode = $state<'tokens' | 'costs'>('tokens')
  let buckets = $state<ChartBucket[]>([])
  let loading = $state(true)
  let failed = $state(false)
  let refreshTick = $state(0)

  $effect(() => {
    const wanted = period
    void refreshTick
    let cancelled = false
    loading = true
    failed = false
    api
      .getUsageChart(wanted)
      .then((data) => {
        if (cancelled) return
        buckets = (Array.isArray(data) ? data : []).map((b) => ({
          label: b.label,
          tokens: b.tokens || 0,
          cost: b.cost || 0,
        }))
        loading = false
      })
      .catch(() => {
        if (cancelled) return
        buckets = []
        failed = true
        loading = false
      })
    return () => {
      cancelled = true
    }
  })

  function valueOf(b: ChartBucket): number {
    return mode === 'tokens' ? b.tokens : b.cost
  }

  function fmtValue(v: number): string {
    return mode === 'tokens' ? formatTokensID(v) : fmtCost(v)
  }

  // Fold contiguous buckets into at most MAX_COLS columns by summing, so a
  // long period still draws one column per visible block stack.
  const columns = $derived.by((): ChartBucket[] => {
    if (buckets.length <= MAX_COLS) return buckets
    const size = Math.ceil(buckets.length / MAX_COLS)
    const out: ChartBucket[] = []
    for (let i = 0; i < buckets.length; i += size) {
      const slice = buckets.slice(i, i + size)
      out.push({
        label: slice[0].label,
        tokens: slice.reduce((s, b) => s + b.tokens, 0),
        cost: slice.reduce((s, b) => s + b.cost, 0),
      })
    }
    return out
  })

  const peak = $derived(columns.reduce((max, b) => Math.max(max, valueOf(b)), 0))
  const step = $derived(peak > 0 ? peak / ROWS : 0)
  const hasData = $derived(peak > 0)

  function filledCount(v: number): number {
    if (v <= 0 || step <= 0) return 0
    return Math.min(ROWS, Math.max(1, Math.ceil(v / step)))
  }
</script>

<Card title="Volume Grid" subtitle="One block equals a fixed share of the peak bucket in this period" padding="md">
  {#snippet action()}
    <div class="inline-flex rounded-brand border border-border bg-surface-2 p-0.5">
      <button
        type="button"
        onclick={() => (mode = 'tokens')}
        class="rounded-brand px-2.5 py-1 text-[11px] font-semibold transition-colors cursor-pointer {mode === 'tokens'
          ? 'bg-primary text-white'
          : 'text-text-muted hover:text-text-main'}"
      >
        Tokens
      </button>
      <button
        type="button"
        onclick={() => (mode = 'costs')}
        class="rounded-brand px-2.5 py-1 text-[11px] font-semibold transition-colors cursor-pointer {mode === 'costs'
          ? 'bg-primary text-white'
          : 'text-text-muted hover:text-text-main'}"
      >
        Cost
      </button>
    </div>
  {/snippet}

  {#if loading}
    <div class="flex items-center justify-center py-12 text-xs text-text-muted">
      <span class="material-symbols-outlined animate-spin text-[20px] mr-2" aria-hidden="true">progress_activity</span>
      Loading…
    </div>
  {:else if failed}
    <div class="py-10 text-center">
      <p class="text-xs text-text-muted">Couldn't load chart data</p>
      <button
        type="button"
        onclick={() => refreshTick++}
        class="mt-3 inline-flex items-center gap-1.5 rounded-brand border border-border bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text-main hover:border-primary/40 cursor-pointer"
      >
        <span class="material-symbols-outlined text-[16px]" aria-hidden="true">refresh</span>
        Retry
      </button>
    </div>
  {:else if !hasData}
    <div class="py-10 text-center text-xs text-text-muted">No usage recorded for this period</div>
  {:else}
    <!-- Desktop / tablet: the block grid. Columns fill from the bottom; the
         Y axis labels every 3rd row (the top row is a multiple of 3 too). -->
    <div class="hidden sm:flex items-stretch gap-3" role="img" aria-label="Volume grid across {columns.length} buckets">
      <div class="flex w-14 shrink-0 flex-col-reverse gap-[3px]">
        {#each { length: ROWS } as _, r (r)}
          <div class="flex h-2.5 items-center justify-end whitespace-nowrap font-mono text-[10px] text-text-subtle">
            {#if (r + 1) % 3 === 0}{fmtValue(step * (r + 1))}{/if}
          </div>
        {/each}
      </div>
      <div class="flex flex-1 gap-[3px]">
        {#each columns as col, ci (ci)}
          {@const filled = filledCount(valueOf(col))}
          <div class="flex flex-1 flex-col-reverse gap-[3px]">
            {#each { length: ROWS } as _, r (r)}
              <div
                title="{col.label}: {fmtValue(valueOf(col))}"
                class="h-2.5 w-full rounded-[2px] {r < filled
                  ? mode === 'tokens'
                    ? 'bg-primary'
                    : 'bg-amber-500'
                  : 'bg-surface-2'}"
              ></div>
            {/each}
          </div>
        {/each}
      </div>
    </div>

    <!-- Mobile fallback: labelled proportional bars instead of the grid. -->
    <div class="flex flex-col gap-2.5 sm:hidden">
      {#each columns as col, ci (ci)}
        <div>
          <div class="mb-1 flex items-center justify-between text-[11px]">
            <span class="text-text-muted">{col.label}</span>
            <span class="font-mono font-semibold text-text-main">{fmtValue(valueOf(col))}</span>
          </div>
          <div class="h-2 w-full rounded-brand bg-surface-2">
            <div
              class="h-full rounded-brand {mode === 'tokens' ? 'bg-primary' : 'bg-amber-500'}"
              style="width: {peak > 0 ? (valueOf(col) / peak) * 100 : 0}%"
            ></div>
          </div>
        </div>
      {/each}
    </div>

    <div class="mt-4 flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-border-subtle pt-3 text-[11px] text-text-muted">
      <span>1 block ≈ {fmtValue(step)}</span>
      <span>Peak {fmtValue(peak)}</span>
      {#if mode === 'costs'}
        <span>Estimated: not billed</span>
      {/if}
      <span class="ml-auto font-mono">{columns.length} buckets · {period}</span>
    </div>
  {/if}
</Card>
