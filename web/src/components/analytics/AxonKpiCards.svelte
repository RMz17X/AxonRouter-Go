<script lang="ts">
  // AxonRouter Overview KPI row: exactly Axon's five cards. 9router-go's old
  // six-card row carried a separate Success Rate card; Axon folds the failure
  // count into the Requests card's note instead, so that card is gone here.
  import { fmtCost, fmtCostApprox, fmtNumberUS, formatTokensID } from '../../lib/axonFormat'
  import { type StatsData } from './types'

  let { stats = {} }: { stats?: StatsData } = $props()

  let totalRequests = $derived(stats.totalRequests ?? 0)
  let totalErrors = $derived(stats.totalErrors ?? 0)
  let prompt = $derived(stats.totalPromptTokens ?? 0)
  let cached = $derived(stats.totalCachedTokens ?? 0)
  let completion = $derived(stats.totalCompletionTokens ?? 0)
  let totalCost = $derived(stats.totalCost ?? 0)

  let nonCached = $derived(Math.max(0, prompt - cached))
  let inputNote = $derived(
    nonCached === 0 && cached > 0 ? 'All cached' : `${formatTokensID(nonCached)} direct`
  )
  let cacheHitPct = $derived(prompt > 0 ? (cached / prompt) * 100 : 0)
  let outputPct = $derived(prompt + completion > 0 ? (completion / (prompt + completion)) * 100 : 0)

  // Axon splits the estimated cost by token share: input tokens own the
  // prompt fraction of the bill, output tokens own the rest. With no tokens
  // there is no share to allocate, so both sides read $0.00.
  let costSplit = $derived.by(() => {
    const totalTokens = prompt + completion
    if (totalTokens <= 0) return { inCost: 0, outCost: 0 }
    const inCost = totalCost * (prompt / totalTokens)
    return { inCost, outCost: totalCost - inCost }
  })
</script>

<div class="grid min-w-0 grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-5">
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Requests</span>
    <span class="truncate text-2xl font-bold text-text-main" title={fmtNumberUS(totalRequests)}>{fmtNumberUS(totalRequests)}</span>
    <span class="text-[10px] text-text-muted truncate">{fmtNumberUS(totalErrors)} failed</span>
  </div>
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Input Tokens</span>
    <span class="truncate text-2xl font-bold text-primary" title={formatTokensID(prompt)}>{formatTokensID(prompt)}</span>
    <span class="text-[10px] text-text-muted truncate">{inputNote}</span>
  </div>
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Cached Tokens</span>
    <span class="truncate text-2xl font-bold text-cyan-400" title={formatTokensID(cached)}>{formatTokensID(cached)}</span>
    <span class="text-[10px] text-text-muted truncate">{cacheHitPct.toFixed(1)}% cache hit</span>
  </div>
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Output Tokens</span>
    <span class="truncate text-2xl font-bold text-emerald-400" title={formatTokensID(completion)}>{formatTokensID(completion)}</span>
    <span class="text-[10px] text-text-muted truncate">{outputPct.toFixed(1)}% output</span>
  </div>
  <div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-1 px-4 py-3">
    <span class="text-text-muted text-sm uppercase font-semibold">Estimated Cost</span>
    <span class="truncate text-2xl font-bold text-amber-400" title={fmtCostApprox(totalCost)}>{fmtCostApprox(totalCost)}</span>
    <!-- Axon links this note to a pricing settings page ("Rates").
         9router-go has no pricing page, so the link is deliberately omitted:
         a link to nowhere would be a dead control. -->
    <span class="text-[10px] text-text-muted truncate">In {fmtCost(costSplit.inCost)} · Out {fmtCost(costSplit.outCost)}</span>
  </div>
</div>
