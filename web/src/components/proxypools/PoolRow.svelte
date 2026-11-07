<script lang="ts">
  import Badge from '../../lib/ui/Badge.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import type { ProxyPool } from '../../api/client'
  import {
    formatDateTime,
    getLatencyBadge,
    getStatusVariant,
    hasMeasuredLatency,
    poolTypeKey,
    poolTypeLabel,
  } from './helpers'

  interface Props {
    pool: ProxyPool
    selected: boolean
    testing: boolean
    toggleSelect: (id: string) => void
    handleToggleActive: (pool: ProxyPool) => void
    handleTest: (id: string) => void
    openEditModal: (pool: ProxyPool) => void
    handleDelete: (pool: ProxyPool) => void
    onSetGroup: (pool: ProxyPool) => void
  }

  let {
    pool,
    selected,
    testing,
    toggleSelect,
    handleToggleActive,
    handleTest,
    openEditModal,
    handleDelete,
    onSetGroup,
  }: Props = $props()

  let latencyBadge = $derived(hasMeasuredLatency(pool) ? getLatencyBadge(pool.latency) : null)
  let typeKey = $derived(poolTypeKey(pool))
  let exitLine = $derived(
    [
      pool.exitIpv4 || pool.exitIpv6,
      [pool.exitCity, pool.exitCountry].filter(Boolean).join(', '),
      [pool.exitAsn, pool.exitIsp || pool.exitOrg].filter(Boolean).join(' · '),
    ]
      .filter(Boolean)
      .join(' · ')
  )
</script>

            
            <div class="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between">
              <div class="flex items-start gap-3 min-w-0 flex-1">
                <input
                  type="checkbox"
                  checked={selected}
                  onchange={() => toggleSelect(pool.id)}
                  class="mt-1 size-4 shrink-0 rounded border-black/20 dark:border-white/20 cursor-pointer"
                />
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2 flex-wrap">
                    <p class="min-w-0 max-w-full truncate text-sm font-medium sm:max-w-[18rem]">
                      {pool.name}
                    </p>
                    <Badge variant={getStatusVariant(pool.testStatus)} size="sm" dot>
                      {pool.testStatus || 'unknown'}
                    </Badge>
                    {#if latencyBadge}
                      <Badge variant={latencyBadge.variant} size="sm">
                        {latencyBadge.text}
                      </Badge>
                    {/if}
                    <Badge variant={pool.isActive ? 'success' : 'default'} size="sm">
                      {pool.isActive ? 'active' : 'inactive'}
                    </Badge>
                    {#if pool.group}
                      <Badge variant="secondary" size="sm">
                        <span class="material-symbols-outlined text-[13px] mr-1">folder</span>
                        {pool.group}
                      </Badge>
                    {/if}
                    {#if pool.inCooldown}
                      <Badge variant="error" size="sm" dot>
                        COOLDOWN ({pool.cooldownRemainingSec || 0}s)
                      </Badge>
                    {:else if typeof pool.fitnessScore === 'number'}
                      <Badge variant={pool.fitnessScore >= 80 ? 'success' : pool.fitnessScore >= 50 ? 'warning' : 'error'} size="sm">
                        fit: {Math.round(pool.fitnessScore)}%
                      </Badge>
                    {/if}
                    {#if pool.consecutiveProbeFails && pool.consecutiveProbeFails > 0}
                      <Badge variant="error" size="sm">
                        probe fails: {pool.consecutiveProbeFails}/10
                      </Badge>
                    {/if}
                    {#if ['vercel', 'cloudflare', 'deno', 'netlify', 'relay'].includes(typeKey)}
                      <Badge size="sm">{poolTypeLabel(typeKey)} relay</Badge>
                    {/if}
                    <span class="text-[11px] text-text-muted">
                      {pool.boundConnectionCount || 0} bound
                    </span>
                  </div>
                  <p class="text-xs text-text-muted truncate mt-1">{pool.proxyUrl}</p>
                  {#if pool.noProxy}
                    <p class="text-xs text-text-muted truncate">No proxy: {pool.noProxy}</p>
                  {/if}
                  <p class="text-[11px] text-text-muted mt-1">
                    Last tested: {formatDateTime(pool.lastTestedAt)}
                    {pool.lastError ? ` · ${pool.lastError}` : ''}
                  </p>

                  {#if exitLine}
                    <p class="mt-1.5 font-mono text-[11px] text-text-muted">{exitLine}</p>
                  {/if}
                </div>
              </div>

              <div class="flex items-center justify-end gap-1">
                <Toggle
                  size="sm"
                  checked={pool.isActive === true}
                  onChange={() => handleToggleActive(pool)}
                  title={pool.isActive ? 'Disable' : 'Enable'}
                />
                <button
                  type="button"
                  onclick={() => handleTest(pool.id)}
                  class="p-2 rounded hover:bg-black/5 dark:hover:bg-white/5 text-text-muted hover:text-primary cursor-pointer disabled:opacity-50"
                  title="Test proxy"
                  disabled={testing}
                >
                  <span
                    class="material-symbols-outlined text-[18px]"
                    style={testing ? 'animation: spin 1s linear infinite' : undefined}
                  >
                    {testing ? 'progress_activity' : 'science'}
                  </span>
                </button>
                <button
                  type="button"
                  onclick={() => onSetGroup(pool)}
                  class="p-2 rounded hover:bg-black/5 dark:hover:bg-white/5 text-text-muted hover:text-primary cursor-pointer"
                  title="Set group"
                >
                  <span class="material-symbols-outlined text-[18px]">folder</span>
                </button>
                <button
                  type="button"
                  onclick={() => openEditModal(pool)}
                  class="p-2 rounded hover:bg-black/5 dark:hover:bg-white/5 text-text-muted hover:text-primary cursor-pointer"
                  title="Edit"
                >
                  <span class="material-symbols-outlined text-[18px]">edit</span>
                </button>
                <button
                  type="button"
                  onclick={() => handleDelete(pool)}
                  class="p-2 rounded hover:bg-red-500/10 text-red-500 cursor-pointer"
                  title="Delete"
                >
                  <span class="material-symbols-outlined text-[18px]">delete</span>
                </button>
              </div>
            </div>
