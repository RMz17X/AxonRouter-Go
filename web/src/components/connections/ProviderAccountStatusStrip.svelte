<script lang="ts">
  import type { ProviderConnection } from '../../api/client'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import type { ConnectionHealth, ConnectionHealthState } from './connectionHealth'
  import type { ProviderStats } from './types'

  interface Props {
    connections: ProviderConnection[]
    healthById: Map<string, ConnectionHealth>
    stats?: ProviderStats
    /** Hide the "N accounts" label (the provider header already counts
     * connections next to where the strip is placed there). */
    showCount?: boolean
  }

  let { connections, healthById, stats, showCount = true }: Props = $props()

  interface StateBucket {
    state: ConnectionHealthState
    label: string
    chipClass: string
    dotClass?: string
    icon?: string
    count: number
    names: string[]
  }

  const STATE_ORDER: ConnectionHealthState[] = ['active', 'validation', 'error', 'cooldown', 'locked', 'disabled']

  function displayName(conn: ProviderConnection): string {
    return formatEmailLabel(conn.name || conn.email || conn.id, $emailPrivacy)
  }

  let buckets = $derived.by(() => {
    const counts = new Map<ConnectionHealthState, number>()
    const byState = new Map<ConnectionHealthState, string[]>()
    for (const state of STATE_ORDER) {
      counts.set(state, 0)
      byState.set(state, [])
    }

    if (stats && stats.total > connections.length) {
      // Fast path: the loaded page is only a slice of this provider, so
      // the counts come from the server summary stats. When the payload
      // carries the per-state census, every bucket reads its own count;
      // the total/connected/error derivation below is only the fallback
      // for payloads that predate the census.
      if (stats.states) {
        for (const state of STATE_ORDER) {
          counts.set(state, stats.states[state] ?? 0)
        }
      } else {
        counts.set('active', stats.connected)
        counts.set('error', stats.errorCount)
        const disabledCount = stats.allDisabled
          ? stats.total
          : Math.max(0, stats.total - stats.connected - stats.errorCount)
        counts.set('disabled', disabledCount)
      }

      // Sample names from currently visible connections for tooltip
      for (const conn of connections) {
        const health = healthById.get(conn.id)
        if (!health) continue
        const list = byState.get(health.state)
        if (list && list.length < 8) {
          list.push(displayName(conn))
        }
      }
    } else {
      for (const conn of connections) {
        const health = healthById.get(conn.id)
        if (!health) continue
        const currentCount = counts.get(health.state) || 0
        counts.set(health.state, currentCount + 1)
        const list = byState.get(health.state)
        if (list && list.length < 8) {
          list.push(displayName(conn))
        }
      }
    }

    const style: Record<ConnectionHealthState, { label: string; chipClass: string; dotClass?: string; icon?: string }> = {
      active: {
        label: 'Active',
        chipClass: 'bg-green-500/10 text-green-600 dark:text-green-400',
        dotClass: 'bg-green-500',
      },
      validation: {
        label: 'Needs verification',
        chipClass: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/30',
        icon: 'gpp_maybe',
      },
      error: {
        label: 'Error',
        chipClass: 'bg-red-500/10 text-red-600 dark:text-red-400',
        dotClass: 'bg-red-500',
      },
      cooldown: {
        label: 'Cooldown',
        chipClass: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border border-amber-500/30',
        icon: 'hourglass_top',
      },
      locked: {
        label: 'Locked',
        chipClass: 'bg-orange-500/10 text-orange-600 dark:text-orange-400 border border-orange-500/30',
        icon: 'lock',
      },
      disabled: {
        label: 'Disabled',
        chipClass: 'bg-surface-3 text-text-muted',
        dotClass: 'bg-text-muted/60',
      },
    }
    return STATE_ORDER.map((state): StateBucket => ({
      state,
      count: counts.get(state) ?? 0,
      names: byState.get(state) ?? [],
      ...style[state]
    }))
  })

  function bucketTitle(bucket: StateBucket): string {
    if (bucket.count === 0) return `No ${bucket.label.toLowerCase()} accounts`
    const shown = bucket.names.slice(0, 8)
    const extra = bucket.count - shown.length
    if (shown.length === 0) return `${bucket.label}: ${bucket.count} accounts`
    return `${bucket.label}: ${shown.join(', ')}${extra > 0 ? `, +${extra} more` : ''}`
  }
</script>

<!-- Account health summary: counts by state, derived from the server's
     effectiveStatus plus the locks census (see connectionHealth.ts). The
     badge vocabulary mirrors the connection rows below it. -->
<div class="flex flex-wrap items-center gap-1.5 sm:gap-2 {showCount ? 'px-1 py-2' : ''}" role="status" aria-label="Account status summary">
  {#if showCount}
    <span class="text-xs font-medium text-text-muted">
      {stats?.total ?? connections.length}
      {(stats?.total ?? connections.length) === 1 ? 'account' : 'accounts'}
    </span>
  {/if}
  {#each buckets as bucket (bucket.state)}
    <span
      class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-semibold {bucket.chipClass} {bucket.count === 0 ? 'opacity-45' : ''}"
      title={bucketTitle(bucket)}
    >
      {#if bucket.icon}
        <span class="material-symbols-outlined text-[12px]">{bucket.icon}</span>
      {:else}
        <span class="size-1.5 rounded-full {bucket.dotClass}"></span>
      {/if}
      {bucket.label}
      {bucket.count}
    </span>
  {/each}
</div>
