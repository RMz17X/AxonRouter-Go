<script lang="ts">
  // A one-line readout of what the provider's stored proxy strategy
  // actually resolves to, served by GET /api/providers/proxy-resolution
  // (the production resolver's own candidate matching). It exists so a
  // selection that matches no active pool — requests silently going
  // direct — is visible next to the control that set it, instead of
  // being discovered from traffic. Renders nothing when the provider
  // has no proxy binding.
  interface Candidate {
    id: string
    name?: string
    type?: string
    group?: string
  }
  interface Resolution {
    binding: string
    selector: string
    candidateCount: number
    resolves: boolean
    candidates: Candidate[]
  }
  interface Props {
    providerId: string
    refreshKey?: unknown
  }

  let { providerId, refreshKey = '' }: Props = $props()

  let data = $state<Resolution | null>(null)

  $effect(() => {
    const id = providerId
    void refreshKey
    let cancelled = false
    fetch(`/api/providers/proxy-resolution?id=${encodeURIComponent(id)}`, { cache: 'no-store' })
      .then((res) => (res.ok ? res.json() : null))
      .then((json) => {
        if (!cancelled) data = json
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  })

  let poolNames = $derived.by(() => {
    if (!data) return ''
    const names = data.candidates.map((c) => c.name || c.id)
    if (names.length <= 3) return names.join(', ')
    return `${names.slice(0, 3).join(', ')} +${names.length - 3} more`
  })

  // A group/type binding is identified by its selector name, not by the
  // pools it happens to match: the operator picked the group, so the
  // readout names the group (the pool list is noise at group scale —
  // hundreds of relay names). Bare pool parts resolve to the pool's
  // display name via the candidate list.
  let selectorNames = $derived.by(() => {
    if (!data) return ''
    const byId = new Map(data.candidates.map((c) => [c.id, c.name || c.id]))
    return data.selector
      .split(',')
      .map((part) => {
        const p = part.trim()
        if (p.startsWith('group:')) return p.slice('group:'.length)
        if (p.startsWith('type:')) return p.slice('type:'.length)
        return byId.get(p) ?? p
      })
      .join(', ')
  })
</script>

{#if data && data.binding === 'direct'}
  <p class="flex items-center gap-1.5 text-xs text-text-muted">
    <span class="material-symbols-outlined text-[14px]">link_off</span>
    Provider proxy: Direct (explicitly set).
  </p>
{:else if data && data.binding === 'rotation'}
  {#if data.resolves}
    <p class="flex items-center gap-1.5 text-xs text-text-muted">
      <span class="material-symbols-outlined text-[14px]">sync</span>
      Proxy rotation across {data.candidateCount} active pool{data.candidateCount === 1 ? '' : 's'}.
    </p>
  {:else}
    <p class="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
      <span class="material-symbols-outlined text-[14px]">warning</span>
      Proxy rotation is set but no proxy pool is active — requests will go direct.
    </p>
  {/if}
{:else if data && data.binding !== 'none'}
  {#if data.resolves}
    <p class="flex items-center gap-1.5 text-xs text-text-muted">
      <span class="material-symbols-outlined text-[14px]">check_circle</span>
      {#if data.binding === 'group'}
        Resolves to proxy group: {selectorNames}.
      {:else if data.binding === 'type'}
        Resolves to proxy type: {selectorNames}.
      {:else if data.binding === 'multi'}
        Resolves to proxy groups: {selectorNames}.
      {:else}
        Resolves to {data.candidateCount} active pool{data.candidateCount === 1 ? '' : 's'}: {poolNames}.
      {/if}
    </p>
  {:else}
    <p class="flex items-center gap-1.5 text-xs text-amber-600 dark:text-amber-400">
      <span class="material-symbols-outlined text-[14px]">warning</span>
      This selection ({data.selector}) matches no active pool — requests will go direct.
    </p>
  {/if}
{/if}
