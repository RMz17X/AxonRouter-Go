<script lang="ts">
  import type { ProviderConnection } from '../../api/client'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import type { ConnectionHealth, ConnectionHealthState } from './connectionHealth'

  interface Props {
    connections: ProviderConnection[]
    healthById: Map<string, ConnectionHealth>
  }

  let { connections, healthById }: Props = $props()

  interface StateBucket {
    state: ConnectionHealthState
    label: string
    chipClass: string
    dotClass?: string
    icon?: string
    names: string[]
  }

  const STATE_ORDER: ConnectionHealthState[] = ['active', 'error', 'cooldown', 'locked', 'disabled']

  function displayName(conn: ProviderConnection): string {
    return formatEmailLabel(conn.name || conn.email || conn.id, $emailPrivacy)
  }

  let buckets = $derived.by(() => {
    const byState = new Map<ConnectionHealthState, string[]>()
    for (const state of STATE_ORDER) byState.set(state, [])
    for (const conn of connections) {
      const health = healthById.get(conn.id)
      if (!health) continue
      byState.get(health.state)?.push(displayName(conn))
    }
    const style: Record<ConnectionHealthState, { label: string; chipClass: string; dotClass?: string; icon?: string }> = {
      active: {
        label: 'Active',
        chipClass: 'bg-green-500/10 text-green-600 dark:text-green-400',
        dotClass: 'bg-green-500',
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
    return STATE_ORDER.map((state): StateBucket => ({ state, names: byState.get(state) ?? [], ...style[state] }))
  })

  function bucketTitle(bucket: StateBucket): string {
    if (bucket.names.length === 0) return `No ${bucket.label.toLowerCase()} accounts`
    const shown = bucket.names.slice(0, 8)
    const extra = bucket.names.length - shown.length
    return `${bucket.label}: ${shown.join(', ')}${extra > 0 ? `, +${extra} more` : ''}`
  }
</script>

<!-- Account health summary: counts by state, derived from the server's
     effectiveStatus plus the locks census (see connectionHealth.ts). The
     badge vocabulary mirrors the connection rows below it. -->
<div class="flex flex-wrap items-center gap-1.5 px-1 py-2 sm:gap-2" role="status" aria-label="Account status summary">
  <span class="text-xs font-medium text-text-muted">
    {connections.length}
    {connections.length === 1 ? 'account' : 'accounts'}
  </span>
  {#each buckets as bucket (bucket.state)}
    <span
      class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-semibold {bucket.chipClass} {bucket.names.length === 0 ? 'opacity-45' : ''}"
      title={bucketTitle(bucket)}
    >
      {#if bucket.icon}
        <span class="material-symbols-outlined text-[12px]">{bucket.icon}</span>
      {:else}
        <span class="size-1.5 rounded-full {bucket.dotClass}"></span>
      {/if}
      {bucket.label}
      {bucket.names.length}
    </span>
  {/each}
</div>
