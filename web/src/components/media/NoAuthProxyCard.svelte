<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../api/client'
  import Badge from '../../lib/ui/Badge.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import ProxyResolutionNote from './ProxyResolutionNote.svelte'

  interface Props {
    providerId: string
  }

  let { providerId }: Props = $props()

  const NONE_PROXY_POOL_VALUE = '__none__'

  let proxyPools = $state<Array<{ type?: string; group?: string; isActive?: boolean }>>([])
  let proxyPoolId = $state(NONE_PROXY_POOL_VALUE)
  let saving = $state(false)
  let savedFlash = $state(false)
  let resolutionKey = $state(0)

  onMount(() => {
    let cancelled = false
    Promise.all([
      api.getProxyPools().catch(() => []),
      api.getSettings(),
    ]).then(([poolsData, settingsData]) => {
      if (cancelled) return
      proxyPools = (poolsData as any) || []
      const override = (settingsData?.providerStrategies || {})[providerId] || {}
      proxyPoolId = override.proxyPoolId || (override.proxyGroup ? `group:${override.proxyGroup}` : NONE_PROXY_POOL_VALUE)
    }).catch(() => {})

    return () => { cancelled = true }
  })

  async function save(poolId: string) {
    saving = true
    try {
      const res = await fetch('/api/settings', { cache: 'no-store' })
      const data = res.ok ? await res.json() : {}
      const current = data.providerStrategies || {}
      const override = { ...(current[providerId] || {}) }
      if (poolId === NONE_PROXY_POOL_VALUE) {
        delete override.proxyPoolId
        delete override.proxyGroup
      } else if (poolId.startsWith('group:')) {
        override.proxyPoolId = poolId
        override.proxyGroup = poolId.slice(6)
      } else {
        // type:<type> — the resolver reads the prefix natively; there is
        // no group name to mirror into proxyGroup.
        override.proxyPoolId = poolId
        delete override.proxyGroup
      }
      // Rotation is no longer offered for NoAuth providers (owner rule:
      // the choice is a proxy group or a proxy type, nothing else), so a
      // strategy stored by the old UI must not survive a save from this
      // card and keep rotating invisibly. Both spellings go: the stored
      // rotateStrategy key is the live one (the settings parser maps it
      // onto the resolver's ProxyRotateStrategy field), and a stray
      // proxyRotateStrategy key is never parsed back — dead weight that
      // only pretends to be a setting.
      delete override.rotateStrategy
      delete override.proxyRotateStrategy
      const updated = { ...current }
      if (Object.keys(override).length === 0) delete updated[providerId]
      else updated[providerId] = override
      await fetch('/api/settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ providerStrategies: updated }),
      })
      resolutionKey += 1
      savedFlash = true
      setTimeout(() => { savedFlash = false }, 1500)
    } catch (e) {
      console.error('Save proxy config error:', e)
    } finally {
      saving = false
    }
  }

  function handleTargetChange(newPoolId: string) {
    proxyPoolId = newPoolId
    save(newPoolId)
  }

  // Group chips are derived from the pools' own group field — the only
  // thing the resolver's "group:<name>" selector matches. The
  // /api/proxy-groups list is NOT used here: its default groups are
  // type-backed (key = the type name, counts by type) and its custom
  // groups are membership-backed (pool ID lists), so encoding either as
  // "group:<key>" produced chips whose counts promised pools the
  // selector could not resolve. Type-backed selection lives on the
  // Type chips below, which is also how the rest of the dashboard
  // derives group options.
  let allProxyGroups = $derived.by(() => {
    const counts = new Map<string, number>()
    for (const p of proxyPools) {
      const g = (p.group || '').trim()
      if (!g) continue
      if (!counts.has(g)) counts.set(g, 0)
      if (p.isActive) counts.set(g, (counts.get(g) ?? 0) + 1)
    }
    return Array.from(counts.entries())
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([name, activeCount]) => ({ id: `group:${name}`, label: name, activeCount }))
  })

  let allProxyTypes = $derived.by(() => {
    const counts = new Map<string, number>()
    for (const p of proxyPools) {
      const t = (p.type || '').trim()
      if (!t) continue
      if (!counts.has(t)) counts.set(t, 0)
      if (p.isActive) counts.set(t, (counts.get(t) ?? 0) + 1)
    }
    return Array.from(counts.entries())
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([type, activeCount]) => ({ id: `type:${type}`, label: type, activeCount }))
  })
</script>

<Card>
  <div class="flex items-center gap-3 mb-4">
    <div class="inline-flex items-center justify-center w-10 h-10 rounded-full bg-green-500/10 text-green-500">
      <span class="material-symbols-outlined text-[20px]">lock_open</span>
    </div>
    <div class="flex-1">
      <p class="text-sm font-medium text-text-main">No authentication required</p>
      <p class="text-xs text-text-muted">This provider is ready to use. Optionally route requests through a proxy group or type to bypass IP-based limits.</p>
    </div>
    {#if savedFlash}
      <Badge variant="success" size="sm">Saved</Badge>
    {/if}
  </div>

  <div class="flex flex-col gap-2">
    <div class="flex items-center justify-between">
      <label class="text-sm font-medium text-text-main">Proxy</label>
      <span class="text-xs text-text-muted">Routes through the selected proxy group or type</span>
    </div>
    <div class="flex items-center gap-2 flex-wrap">
      <button
        type="button"
        onclick={() => handleTargetChange(NONE_PROXY_POOL_VALUE)}
        disabled={saving}
        class="px-3.5 py-2 rounded-lg text-xs font-medium border transition-all cursor-pointer flex items-center gap-1.5 {proxyPoolId === NONE_PROXY_POOL_VALUE
          ? 'bg-primary text-white border-primary shadow-sm'
          : 'bg-surface-2 text-text-main border-border hover:bg-surface-3 disabled:opacity-50'}"
      >
        <span class="material-symbols-outlined text-[16px]">link_off</span>
        <span>Direct (None)</span>
      </button>
      {#each allProxyGroups as grp}
        {@const isSelected = proxyPoolId === grp.id || proxyPoolId === grp.id.replace('group:', '')}
        <button
          type="button"
          onclick={() => handleTargetChange(grp.id)}
          disabled={saving}
          class="px-3.5 py-2 rounded-lg text-xs font-medium border transition-all cursor-pointer flex items-center gap-1.5 {isSelected
            ? 'bg-primary text-white border-primary shadow-sm'
            : 'bg-surface-2 text-text-main border-border hover:bg-surface-3 disabled:opacity-50'}"
        >
          <span class="material-symbols-outlined text-[16px]">hub</span>
          <span>{grp.label}</span>
          <span class="text-[10px] px-1.5 py-0.5 rounded-full {isSelected ? 'bg-white/20 text-white' : 'bg-black/5 dark:bg-white/10 text-text-muted'}">
            {grp.activeCount}
          </span>
        </button>
      {/each}
      {#each allProxyTypes as typ}
        {@const isSelected = proxyPoolId === typ.id}
        <button
          type="button"
          onclick={() => handleTargetChange(typ.id)}
          disabled={saving}
          class="px-3.5 py-2 rounded-lg text-xs font-medium border transition-all cursor-pointer flex items-center gap-1.5 {isSelected
            ? 'bg-primary text-white border-primary shadow-sm'
            : 'bg-surface-2 text-text-main border-border hover:bg-surface-3 disabled:opacity-50'}"
        >
          <span class="material-symbols-outlined text-[16px]">category</span>
          <span>Type: {typ.label}</span>
          <span class="text-[10px] px-1.5 py-0.5 rounded-full {isSelected ? 'bg-white/20 text-white' : 'bg-black/5 dark:bg-white/10 text-text-muted'}">
            {typ.activeCount}
          </span>
        </button>
      {/each}
    </div>
    <ProxyResolutionNote {providerId} refreshKey={resolutionKey} />
  </div>
</Card>
