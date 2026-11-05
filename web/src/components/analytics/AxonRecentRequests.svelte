<script lang="ts">
  // AxonRouter's Recent Requests card: the latest requests beside the
  // topology map, in a fixed 480px card that scrolls internally. Relative
  // times follow Axon's TimeAgo ("just now", "5m", "2h", "1d") and re-render
  // on a 30 s tick so a row ages without new data arriving.
  import { onMount } from 'svelte'
  import { fmtNumberUS } from '../../lib/axonFormat'
  import type { RecentRequestItem } from './types'

  interface Props {
    recentRequests?: RecentRequestItem[]
  }

  let { recentRequests = [] }: Props = $props()

  let nowMs = $state(Date.now())
  onMount(() => {
    const t = setInterval(() => (nowMs = Date.now()), 30_000)
    return () => clearInterval(t)
  })

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

  // Axon renders "{comboName} -> {model}" when a combo served the request;
  // otherwise the resolved model, falling back to the requested model.
  function modelLabel(req: RecentRequestItem): string {
    const model = req.model || req.requestedModel || '—'
    return req.comboName ? `${req.comboName} -> ${model}` : model
  }

  function isSuccess(req: RecentRequestItem): boolean {
    return req.status === 'ok' || req.status === 'success'
  }

  function isCanceled(req: RecentRequestItem): boolean {
    return req.status === 'canceled' || req.status === 'cancelled'
  }

  function badgeClass(req: RecentRequestItem): string {
    if (isSuccess(req)) return 'bg-success/10 text-success border-success/30'
    if (isCanceled(req)) return 'bg-surface-2 text-text-muted border-border'
    return 'bg-danger/10 text-danger border-danger/30'
  }

  function badgeLabel(req: RecentRequestItem): string {
    if (isSuccess(req)) return 'Success'
    if (isCanceled(req)) return 'Canceled'
    return req.status ? req.status.charAt(0).toUpperCase() + req.status.slice(1) : 'Failed'
  }
</script>

<div
  class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col overflow-hidden p-4"
  style="height: 480px"
>
  <div class="shrink-0 border-b border-border px-1 py-2">
    <span class="text-xs font-semibold uppercase tracking-wide text-text-muted">Recent Requests</span>
  </div>

  {#if recentRequests.length === 0}
    <div class="flex flex-1 items-center justify-center text-xs text-text-muted">
      No requests recorded yet.
    </div>
  {:else}
    <!-- Desktop: compact table -->
    <div class="hidden min-h-0 flex-1 overflow-y-auto sm:block">
      <table class="w-full border-collapse text-xs">
        <thead class="sticky top-0 z-10 bg-surface">
          <tr class="border-b border-border">
            <th class="py-1.5 pl-2 text-left font-semibold text-text-muted">Model</th>
            <th class="py-1.5 text-right font-semibold whitespace-nowrap text-text-muted">In / Out</th>
            <th class="py-1.5 text-center font-semibold whitespace-nowrap text-text-muted">Status</th>
            <th class="py-1.5 pr-2 text-right font-semibold whitespace-nowrap text-text-muted">When</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/50">
          {#each recentRequests as req}
            <tr class="transition-colors hover:bg-surface-2">
              <td class="py-1.5 pr-2 pl-2">
                <span class="inline-flex max-w-full items-start gap-1" title={req.loopWarning ? `${req.loopWarning} · ${modelLabel(req)}` : modelLabel(req)}>
                  {#if req.loopWarning}
                    <span class="material-symbols-outlined shrink-0 text-[13px] text-warning mt-0.5" aria-hidden="true">warning</span>
                  {/if}
                  <span class="break-words font-mono text-[11px] leading-tight">{modelLabel(req)}</span>
                </span>
              </td>
              <td class="py-1.5 text-right whitespace-nowrap tabular-nums text-[11px]">
                <span class="text-text-main">{fmtNumberUS(req.promptTokens)}</span>
                <span class="text-text-muted/60 mx-0.5">/</span>
                <span class="text-text-muted">{fmtNumberUS(req.completionTokens)}</span>
              </td>
              <td class="py-1.5 text-center whitespace-nowrap">
                <span class="inline-block rounded-full border px-1.5 py-0.5 text-[10px] font-medium {badgeClass(req)}">{badgeLabel(req)}</span>
              </td>
              <td class="py-1.5 pr-2 text-right whitespace-nowrap text-[10px] text-text-muted">{relTime(req.timestamp)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Mobile: card list -->
    <div class="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pt-2 sm:hidden">
      {#each recentRequests as req}
        <div class="rounded-brand border border-border bg-surface-2/50 px-3 py-2">
          <div class="flex items-start justify-between gap-2">
            <span class="inline-flex min-w-0 items-start gap-1" title={modelLabel(req)}>
              {#if req.loopWarning}
                <span class="material-symbols-outlined shrink-0 text-[13px] text-warning mt-0.5" aria-hidden="true">warning</span>
              {/if}
              <span class="break-words font-mono text-[11px] leading-tight">{modelLabel(req)}</span>
            </span>
            <span class="shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] font-medium {badgeClass(req)}">{badgeLabel(req)}</span>
          </div>
          <div class="mt-1 flex items-center justify-between text-[11px] text-text-muted">
            <span class="tabular-nums">In {fmtNumberUS(req.promptTokens)} · Out {fmtNumberUS(req.completionTokens)}</span>
            <span>{relTime(req.timestamp)}</span>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>
