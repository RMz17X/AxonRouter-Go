<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../../api/client'
  import Badge from '../../lib/ui/Badge.svelte'
  import Card from '../../lib/ui/Card.svelte'

  interface Props {
    providerId: string
  }

  let { providerId }: Props = $props()

  const NONE_PROXY_POOL_VALUE = '__none__'
  const STRATEGIES = [
    { value: 'none', label: 'None (single pool)' },
    { value: 'round-robin', label: 'Round-robin' },
    { value: 'random', label: 'Random' },
  ]

  let proxyGroups = $state<Array<{ id: string; name: string; key?: string; activeCount?: number; poolCount?: number }>>([])
  let proxyPoolId = $state(NONE_PROXY_POOL_VALUE)
  let rotateStrategy = $state('none')
  let saving = $state(false)
  let savedFlash = $state(false)

  onMount(() => {
    let cancelled = false
    Promise.all([
      fetch('/api/proxy-groups').then((r) => r.ok ? r.json() : { groups: [] }),
      api.getSettings(),
    ]).then(([groupData, settingsData]) => {
      if (cancelled) return
      proxyGroups = (groupData as any)?.groups || []
      const override = (settingsData?.providerStrategies || {})[providerId] || {}
      proxyPoolId = override.proxyPoolId || NONE_PROXY_POOL_VALUE
      rotateStrategy = override.rotateStrategy || 'none'
    }).catch(() => {})

    return () => { cancelled = true }
  })

  async function save(poolId: string, strategy: string) {
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
        override.proxyPoolId = poolId
      }
      if (strategy === 'none') delete override.rotateStrategy
      else override.rotateStrategy = strategy
      const updated = { ...current }
      if (Object.keys(override).length === 0) delete updated[providerId]
      else updated[providerId] = override
      await fetch('/api/settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ providerStrategies: updated }),
      })
      savedFlash = true
      setTimeout(() => { savedFlash = false }, 1500)
    } catch (e) {
      console.error('Save proxy config error:', e)
    } finally {
      saving = false
    }
  }

  function handlePoolChange(newPoolId: string) {
    proxyPoolId = newPoolId
    save(newPoolId, rotateStrategy)
  }

  function handleStrategyChange(newStrategy: string) {
    rotateStrategy = newStrategy
    save(proxyPoolId, newStrategy)
  }

  let allProxyGroups = $derived.by(() => {
    const list: Array<{ id: string; label: string; activeCount: number }> = []
    const seen = new Set<string>()
    for (const g of proxyGroups) {
      const gid = g.key || g.id || g.name
      if (!gid || seen.has(gid)) continue
      seen.add(gid)
      list.push({
        id: `group:${gid}`,
        label: g.name || gid,
        activeCount: g.activeCount ?? 0,
      })
    }
    return list
  })

  let canRotate = $derived(allProxyGroups.length >= 2)
  let isRotation = $derived(rotateStrategy !== 'none')
</script>

<Card>
  <div class="flex items-center gap-3 mb-4">
    <div class="inline-flex items-center justify-center w-10 h-10 rounded-full bg-green-500/10 text-green-500">
      <span class="material-symbols-outlined text-[20px]">lock_open</span>
    </div>
    <div class="flex-1">
      <p class="text-sm font-medium text-text-main">No authentication required</p>
      <p class="text-xs text-text-muted">This provider is ready to use. Optionally route requests through a proxy group to bypass IP-based limits.</p>
    </div>
    {#if savedFlash}
      <Badge variant="success" size="sm">Saved</Badge>
    {/if}
  </div>

  <div class="flex flex-col gap-2 mb-4">
    <div class="flex items-center justify-between">
      <label class="text-sm font-medium text-text-main">Proxy Group</label>
      <span class="text-xs text-text-muted">Routes through selected Proxy Group</span>
    </div>
    <div class="flex items-center gap-2 flex-wrap">
      <button
        type="button"
        onclick={() => handlePoolChange(NONE_PROXY_POOL_VALUE)}
        disabled={saving || isRotation}
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
          onclick={() => handlePoolChange(grp.id)}
          disabled={saving || isRotation}
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
    </div>
    {#if isRotation}
      <p class="text-xs text-text-muted">Group selector is disabled when rotation is active — all active pools are used.</p>
    {/if}
  </div>

  <div class="flex flex-col gap-2">
    <label class="text-sm font-medium text-text-main">Rotation Strategy</label>
    <select
      value={rotateStrategy}
      onchange={(e) => handleStrategyChange(e.currentTarget.value)}
      disabled={saving}
      class="py-2 px-3 text-sm text-text-main bg-white dark:bg-white/5 border border-black/10 dark:border-white/10 rounded-md focus:ring-1 focus:ring-primary/30 focus:border-primary/50 focus:outline-none transition-all disabled:opacity-50"
    >
      {#each STRATEGIES as s}
        <option value={s.value} disabled={s.value !== 'none' && !canRotate}>
          {s.label}
        </option>
      {/each}
    </select>
    <p class="text-xs text-text-muted">
      {#if !canRotate}
        Need at least 2 active proxy groups for rotation.
      {:else if isRotation}
        {#if rotateStrategy === 'round-robin'}
          Rotating through all {allProxyGroups.length} active groups in order. State is in-memory (resets on restart).
        {:else}
          Picking a random group from {allProxyGroups.length} active groups each request.
        {/if}
      {:else}
        Uses the selected group above. Set to Round-robin or Random to rotate across all active groups.
      {/if}
    </p>
  </div>
</Card>
