<script lang="ts">
  // AxonRouter's ComboRoutingTable: combo-routed requests grouped by combo,
  // one summary row per combo (Provider cell is the literal "auto-switch",
  // latency is a request-weighted average WITH an "s" suffix) followed by the
  // member rows (latency toFixed(2) WITHOUT the suffix — an Axon quirk,
  // replicated exactly). Success/Errors cells use Axon's share badge helper.
  import { api } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import { fmtNumberUS } from '../../lib/axonFormat'
  import { timeAgo } from './types'

  interface Props {
    period: string
  }

  let { period }: Props = $props()

  interface ComboMember {
    comboName: string
    model: string
    provider: string
    total: number
    success: number
    errors: number
    avgLatencyS: number
    lastSeen: string
  }

  interface ComboGroup {
    comboName: string
    members: ComboMember[]
    total: number
    success: number
    errors: number
    avgLatencyS: number
    lastSeen: string
  }

  let members = $state<ComboMember[]>([])
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
      .getCombosAnalytics(wanted)
      .then((data) => {
        if (cancelled) return
        members = Array.isArray(data?.members) ? (data.members as ComboMember[]) : []
        loading = false
      })
      .catch(() => {
        if (cancelled) return
        members = []
        failed = true
        loading = false
      })
    return () => {
      cancelled = true
    }
  })

  const groups = $derived.by((): ComboGroup[] => {
    const byCombo = new Map<string, ComboMember[]>()
    for (const m of members) {
      const list = byCombo.get(m.comboName) || []
      list.push(m)
      byCombo.set(m.comboName, list)
    }
    return [...byCombo.entries()]
      .map(([comboName, list]) => {
        const sorted = [...list].sort((a, b) => (b.total || 0) - (a.total || 0))
        const total = sorted.reduce((s, m) => s + (m.total || 0), 0)
        const success = sorted.reduce((s, m) => s + (m.success || 0), 0)
        const errors = sorted.reduce((s, m) => s + (m.errors || 0), 0)
        const avgLatencyS =
          total > 0 ? sorted.reduce((s, m) => s + (m.avgLatencyS || 0) * (m.total || 0), 0) / total : 0
        const lastSeen = sorted.reduce((max, m) => (m.lastSeen && m.lastSeen > max ? m.lastSeen : max), '')
        return { comboName, members: sorted, total, success, errors, avgLatencyS, lastSeen }
      })
      .sort((a, b) => b.total - a.total)
  })

  // Axon's badge helper: 0 renders as a muted plain "0"; otherwise
  // "n (share%)" colored by share — 100% green, <50% red, else amber.
  function sharePct(n: number, total: number): string {
    const share = total > 0 ? (n / total) * 100 : 0
    return share.toFixed(1).replace(/\.0$/, '')
  }

  function shareBadgeClass(n: number, total: number): string {
    const share = total > 0 ? (n / total) * 100 : 0
    if (share >= 100) return 'bg-success/10 text-success'
    if (share < 50) return 'bg-danger/10 text-danger'
    return 'bg-warning/10 text-warning'
  }
</script>

{#snippet shareCell(n: number, total: number)}
  {#if !n}
    <span class="font-mono text-text-muted">0</span>
  {:else}
    <span class="inline-block rounded-brand px-1.5 py-0.5 font-mono text-[11px] font-semibold {shareBadgeClass(n, total)}">
      {fmtNumberUS(n)} ({sharePct(n, total)}%)
    </span>
  {/if}
{/snippet}

<Card padding="none" class="overflow-hidden">
  {#if loading}
    <div class="flex items-center justify-center py-12 text-xs text-text-muted">
      <span class="material-symbols-outlined animate-spin text-[20px] mr-2" aria-hidden="true">progress_activity</span>
      Loading…
    </div>
  {:else if failed}
    <div class="py-10 text-center">
      <p class="text-xs text-text-muted">Couldn't load combo routing data</p>
      <button
        type="button"
        onclick={() => refreshTick++}
        class="mt-3 inline-flex items-center gap-1.5 rounded-brand border border-border bg-surface-2 px-3 py-1.5 text-xs font-semibold text-text-main hover:border-primary/40 cursor-pointer"
      >
        <span class="material-symbols-outlined text-[16px]" aria-hidden="true">refresh</span>
        Retry
      </button>
    </div>
  {:else if groups.length === 0}
    <div class="p-8 text-center text-sm text-text-muted">No combo-routed requests in this period.</div>
  {:else}
    <!-- Desktop table -->
    <div class="hidden overflow-x-auto sm:block">
      <table class="w-full border-collapse text-left text-xs">
        <thead class="border-b border-border bg-surface-2 uppercase">
          <tr>
            <th class="py-3 px-4 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Combo</th>
            <th class="py-3 px-4 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Resolved Model</th>
            <th class="py-3 px-4 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Provider</th>
            <th class="py-3 px-4 text-right text-[10px] font-semibold uppercase tracking-wider text-text-muted">Requests</th>
            <th class="py-3 px-4 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Success</th>
            <th class="py-3 px-4 text-[10px] font-semibold uppercase tracking-wider text-text-muted">Errors</th>
            <th class="py-3 px-4 text-right text-[10px] font-semibold uppercase tracking-wider text-text-muted">Avg Latency</th>
            <th class="py-3 px-4 text-right text-[10px] font-semibold uppercase tracking-wider text-text-muted">Last Seen</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border/60">
          {#each groups as group (group.comboName)}
            <tr class="bg-surface-2/50 font-medium">
              <td class="py-2.5 px-4 font-mono font-semibold text-text-main">{group.comboName}</td>
              <td class="py-2.5 px-4 text-text-subtle">—</td>
              <td class="py-2.5 px-4 font-mono text-text-muted">auto-switch</td>
              <td class="py-2.5 px-4 text-right font-mono font-semibold text-text-main">{fmtNumberUS(group.total)}</td>
              <td class="py-2.5 px-4">{@render shareCell(group.success, group.total)}</td>
              <td class="py-2.5 px-4">{@render shareCell(group.errors, group.total)}</td>
              <td class="py-2.5 px-4 text-right font-mono text-text-main">{group.avgLatencyS.toFixed(2)}s</td>
              <td class="py-2.5 px-4 text-right whitespace-nowrap text-[11px] text-text-muted">
                {group.lastSeen ? timeAgo(group.lastSeen) : '—'}
              </td>
            </tr>
            {#each group.members as member (member.model + '|' + member.provider)}
              <tr class="hover:bg-surface-2/40 transition-colors">
                <td class="py-2 px-4"></td>
                <td class="py-2 px-4 font-mono text-text-main">{member.model}</td>
                <td class="py-2 px-4 font-mono text-text-muted">{member.provider}</td>
                <td class="py-2 px-4 text-right font-mono text-text-muted">{fmtNumberUS(member.total)}</td>
                <td class="py-2 px-4">{@render shareCell(member.success, member.total)}</td>
                <td class="py-2 px-4">{@render shareCell(member.errors, member.total)}</td>
                <td class="py-2 px-4 text-right font-mono text-text-muted">{(member.avgLatencyS || 0).toFixed(2)}</td>
                <td class="py-2 px-4 text-right whitespace-nowrap text-[11px] text-text-muted">
                  {member.lastSeen ? timeAgo(member.lastSeen) : '—'}
                </td>
              </tr>
            {/each}
          {/each}
        </tbody>
      </table>
    </div>

    <!-- Mobile: one stacked card per combo, members listed inside. -->
    <div class="flex flex-col divide-y divide-border/60 sm:hidden">
      {#each groups as group (group.comboName)}
        <div class="p-3">
          <div class="flex items-center justify-between gap-2">
            <span class="min-w-0 truncate font-mono text-xs font-semibold text-text-main">{group.comboName}</span>
            <span class="shrink-0 font-mono text-xs text-text-main">{fmtNumberUS(group.total)} req</span>
          </div>
          <div class="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-text-muted">
            <span class="font-mono">auto-switch</span>
            <span>avg {group.avgLatencyS.toFixed(2)}s</span>
            <span>{group.lastSeen ? timeAgo(group.lastSeen) : '—'}</span>
          </div>
          <div class="mt-2 flex items-center gap-2 text-[11px]">
            <span class="text-text-subtle">Success</span>
            {@render shareCell(group.success, group.total)}
            <span class="ml-2 text-text-subtle">Errors</span>
            {@render shareCell(group.errors, group.total)}
          </div>
          <div class="mt-2.5 flex flex-col gap-1.5">
            {#each group.members as member (member.model + '|' + member.provider)}
              <div class="rounded-brand bg-surface-2/60 px-2 py-1.5 text-[11px]">
                <div class="flex items-center justify-between gap-2">
                  <span class="min-w-0 truncate font-mono text-text-main">{member.model}</span>
                  <span class="shrink-0 font-mono text-text-muted">{fmtNumberUS(member.total)} req</span>
                </div>
                <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-text-muted">
                  <span class="font-mono">{member.provider}</span>
                  <span>avg {(member.avgLatencyS || 0).toFixed(2)}</span>
                  <span>{member.lastSeen ? timeAgo(member.lastSeen) : '—'}</span>
                </div>
                <div class="mt-1 flex items-center gap-2">
                  <span class="text-text-subtle">Success</span>
                  {@render shareCell(member.success, member.total)}
                  <span class="ml-1 text-text-subtle">Errors</span>
                  {@render shareCell(member.errors, member.total)}
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</Card>
