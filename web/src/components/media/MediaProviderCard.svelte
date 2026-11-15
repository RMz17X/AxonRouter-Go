<script lang="ts">
  import type { ProviderConnection } from '../../api/client'
  import type { ProviderCatalogItem } from '../../lib/providers'
  import Badge from '../../lib/ui/Badge.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import { getMediaProviderStats, type MediaSummaryStats } from './mediaTypes'

  interface Props {
    provider: ProviderCatalogItem
    kind: string
    connections: ProviderConnection[]
    /** Per-provider counters from /api/providers/stats. The connection list
     *  is no longer loaded globally, so this is where a tile's counts come
     *  from — without it every provider reads "No connections". */
    providerStats?: MediaSummaryStats
    isCustom?: boolean
    onToggle?: (providerId: string, newActive: boolean) => void
    onSelect?: () => void
  }

  let {
    provider,
    kind,
    connections = [],
    providerStats = {},
    isCustom = false,
    onToggle,
    onSelect,
  }: Props = $props()

  let isNoAuth = $derived(!!provider.noAuth)
  let stats = $derived(getMediaProviderStats(connections, provider.id, isNoAuth, providerStats))
  let total = $derived(stats.total)
  let connected = $derived(stats.active)
  let error = $derived(stats.errorCount)
  let allDisabled = $derived(stats.status === 'disabled')

  let bgColor = $derived(
    provider.color && provider.color.length > 7
      ? provider.color
      : (provider.color ?? '#888888') + '15'
  )

  function handleToggleClick(e: MouseEvent | KeyboardEvent) {
    e.preventDefault()
    e.stopPropagation()
    if (onToggle) onToggle(provider.id, allDisabled)
  }

  function handleClick(e: MouseEvent) {
    if (onSelect) {
      e.preventDefault()
      onSelect()
    }
  }
</script>

<a
  href={`/dashboard/media-providers/${kind}/${provider.id}`}
  onclick={handleClick}
  class="group block text-left focus:outline-none"
>
  <Card
    padding="xs"
    class="h-full hover:bg-black/[0.01] dark:hover:bg-white/[0.01] transition-colors cursor-pointer {allDisabled ? 'opacity-50' : ''}"
  >
    <div class="flex min-w-0 items-center justify-between gap-3">
      <div class="flex min-w-0 items-center gap-3">
        <div
          class="size-8 rounded-lg flex items-center justify-center shrink-0"
          style="background-color: {bgColor}"
        >
          <ProviderArtwork
            id={provider.id}
            class="object-contain rounded-lg max-w-[30px] max-h-[30px]"
          />
        </div>
        <div class="min-w-0">
          <h3 class="font-semibold text-sm text-text-main truncate">{provider.name}</h3>
          <div class="flex items-center gap-2 mt-0.5 flex-wrap">
            {#if isCustom}
              <Badge variant="default" size="sm">Custom</Badge>
            {/if}
            {#if isNoAuth}
              <Badge variant="success" size="sm">Ready</Badge>
            {:else if allDisabled}
              <Badge variant="default" size="sm">Disabled</Badge>
            {:else if total === 0}
              <span class="text-xs text-text-muted">No connections</span>
            {:else}
              {#if connected > 0}
                <Badge variant="success" size="sm" dot>{connected} Connected</Badge>
              {/if}
              {#if error > 0}
                <Badge variant="error" size="sm" dot>{error} Error</Badge>
              {/if}
              {#if connected === 0 && error === 0}
                <Badge variant="default" size="sm">{total} Added</Badge>
              {/if}
            {/if}
          </div>
        </div>
      </div>
      {#if total > 0}
        <div
          class="shrink-0 opacity-100 transition-opacity sm:opacity-0 sm:group-hover:opacity-100"
          onclick={handleToggleClick}
          onkeydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') handleToggleClick(e)
          }}
          role="button"
          tabindex="0"
          title={allDisabled ? 'Enable provider' : 'Disable provider'}
        >
          <Toggle
            size="sm"
            checked={!allDisabled}
            onChange={() => {}}
          />
        </div>
      {/if}
    </div>
  </Card>
</a>
