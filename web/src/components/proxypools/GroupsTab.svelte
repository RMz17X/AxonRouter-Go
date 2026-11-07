<script lang="ts">
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import type { GroupSummary } from './helpers'

  interface Props {
    groupSummaries: GroupSummary[]
    totalPools: number
    healthChecking: boolean
    testingId: string | null
    openCreateModal: (tab: 'single' | 'bulk', presetGroup?: string) => void
    testGroup: (name: string) => void
    handleTest: (id: string) => void
    onManage: (name: string) => void
    onViewGroup: (name: string) => void
  }

  let {
    groupSummaries,
    totalPools,
    healthChecking,
    testingId,
    openCreateModal,
    testGroup,
    handleTest,
    onManage,
    onViewGroup,
  }: Props = $props()
</script>

      <div class="flex flex-col gap-4">
        <!-- Group Overview Card -->
        <Card>
          <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 class="text-sm font-semibold text-text-main flex items-center gap-2">
                <span class="material-symbols-outlined text-primary text-[18px]">folder_special</span>
                Proxy Group Balancing & Smart Fitness
              </h2>
              <p class="text-xs text-text-muted mt-1 max-w-2xl">
                Grouping proxy nodes enables smart dynamic balancing. When a provider connection binds to a group (e.g. <span class="font-mono text-primary font-medium">group:dc</span>), the router selects the healthiest node based on latency EWMA and fitness score, and automatically falls over if a node fails.
              </p>
            </div>
            <Button
              size="sm"
              variant="secondary"
              onclick={() => openCreateModal('single')}
            >
              <span class="material-symbols-outlined text-[16px]">add</span>
              New Pool in Group
            </Button>
          </div>
        </Card>

        {#if groupSummaries.length === 0}
          <div class="rounded-xl border border-dashed border-border bg-surface p-12 text-center flex flex-col items-center justify-center gap-3">
            <div class="size-12 rounded-full bg-primary/10 flex items-center justify-center text-primary">
              <span class="material-symbols-outlined text-[24px]">folder_off</span>
            </div>
            <div>
              <h3 class="text-sm font-semibold text-text-main">No Custom Proxy Groups</h3>
              <p class="text-xs text-text-muted mt-1 max-w-sm mx-auto">
                All {totalPools} proxies are currently ungrouped. Group proxy nodes (e.g. "dc", "residential", "fast") to enable smart dynamic balancing and automatic failover in connection bindings.
              </p>
            </div>
            <Button size="sm" onclick={() => openCreateModal('single')}>
              <span class="material-symbols-outlined text-[16px]">add</span>
              Create Proxy in Group
            </Button>
          </div>
        {:else}
          <!-- Group Cards Grid -->
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            {#each groupSummaries as grp (grp.name)}
              <div class="rounded-xl border border-border bg-surface p-4 flex flex-col justify-between gap-4 shadow-sm hover:border-primary/40 transition-colors">
                <div class="space-y-3">
                  <!-- Header -->
                  <div class="flex items-start justify-between gap-2">
                    <div class="flex items-center gap-2 min-w-0">
                      <span class="material-symbols-outlined text-primary text-[22px]">folder_open</span>
                      <div>
                        <h3 class="text-sm font-semibold text-text-main truncate">
                          {grp.name}
                        </h3>
                        <p class="text-[11px] text-text-muted">Target ID: <span class="font-mono text-text-main font-medium">group:{grp.name}</span></p>
                      </div>
                    </div>
                    <div class="flex items-center gap-1.5 shrink-0">
                      {#if grp.inCooldownCount > 0}
                        <span class="rounded bg-red-500/15 border border-red-500/30 text-red-500 px-2 py-0.5 text-[11px] font-semibold animate-pulse">
                          {grp.inCooldownCount} in cooldown
                        </span>
                      {:else}
                        <span class="rounded bg-emerald-500/10 text-emerald-500 px-2 py-0.5 text-[11px] font-medium">
                          All Healthy
                        </span>
                      {/if}
                    </div>
                  </div>

                <!-- Metrics bar -->
                <div class="grid grid-cols-3 gap-2 py-2 px-3 rounded-lg bg-surface-2 border border-border/50 text-center">
                  <div>
                    <div class="text-[10px] text-text-muted uppercase font-medium">Active Nodes</div>
                    <div class="text-xs font-semibold text-text-main mt-0.5">{grp.activeCount} / {grp.totalCount}</div>
                  </div>
                  <div>
                    <div class="text-[10px] text-text-muted uppercase font-medium">Avg Fitness</div>
                    <div class="text-xs font-semibold mt-0.5 {grp.avgFitness >= 80 ? 'text-emerald-500' : grp.avgFitness >= 50 ? 'text-amber-500' : 'text-red-500'}">
                      {grp.avgFitness}%
                    </div>
                  </div>
                  <div>
                    <div class="text-[10px] text-text-muted uppercase font-medium">Avg Latency</div>
                    <div class="text-xs font-semibold text-text-main mt-0.5">{grp.avgLatency > 0 ? `${grp.avgLatency}ms` : 'N/A'}</div>
                  </div>
                </div>

                <!-- Member Pools List -->
                <div>
                  <div class="text-[11px] font-medium text-text-muted mb-1.5 flex items-center justify-between">
                    <span>Member Proxies ({grp.pools.length})</span>
                    {#if grp.strictCount > 0}
                      <span class="text-[10px] text-amber-500">{grp.strictCount} strict mode</span>
                    {/if}
                  </div>
                  <div class="space-y-1.5 max-h-48 overflow-y-auto custom-scrollbar pr-1">
                    {#each grp.pools as pool (pool.id)}
                      <div class="flex items-center justify-between gap-2 p-2 rounded-lg bg-surface-2 border border-border/40 text-xs">
                        <div class="min-w-0 flex-1">
                          <div class="flex items-center gap-1.5">
                            <span class="font-medium text-text-main truncate">{pool.name}</span>
                            {#if !pool.isActive}
                              <span class="text-[10px] text-text-muted">(off)</span>
                            {/if}
                            {#if pool.inCooldown}
                              <span class="text-[10px] text-red-500 font-semibold animate-pulse">COOLDOWN ({pool.cooldownRemainingSec || 0}s)</span>
                            {/if}
                          </div>
                          <div class="text-[10px] text-text-muted font-mono truncate">{pool.proxyUrl}</div>
                        </div>
                        <div class="flex items-center gap-1.5 shrink-0">
                          {#if typeof pool.fitnessScore === 'number'}
                            <span class="font-mono text-[10px] px-1.5 py-0.5 rounded {pool.fitnessScore >= 80 ? 'bg-emerald-500/10 text-emerald-500' : pool.fitnessScore >= 50 ? 'bg-amber-500/10 text-amber-500' : 'bg-red-500/10 text-red-500'}">
                              {pool.fitnessScore}%
                            </span>
                          {/if}
                          {#if pool.latency}
                            <span class="text-[10px] text-text-muted font-mono">{pool.latency}ms</span>
                          {/if}
                          <button
                            type="button"
                            onclick={() => handleTest(pool.id)}
                            disabled={testingId === pool.id}
                            class="p-1 rounded text-text-muted hover:text-text-main hover:bg-surface-3 transition-colors cursor-pointer"
                            title="Test proxy"
                          >
                            <span class="material-symbols-outlined text-[14px] {testingId === pool.id ? 'animate-spin' : ''}">science</span>
                          </button>
                        </div>
                      </div>
                    {/each}
                  </div>
                </div>
              </div>

              <!-- Actions -->
              <div class="flex flex-wrap items-center gap-2 pt-2 border-t border-border/40">
                <Button
                  size="sm"
                  variant="secondary"
onclick={() => onViewGroup(grp.name)}
                >
                  <span class="material-symbols-outlined text-[16px]">filter_list</span>
                  View in Table
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  onclick={() => onManage(grp.name)}
                >
                  <span class="material-symbols-outlined text-[16px]">group_add</span>
                  Manage Members
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  onclick={() => openCreateModal('single', grp.name)}
                >
                  <span class="material-symbols-outlined text-[16px]">add</span>
                  Add Pool
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  onclick={() => testGroup(grp.name)}
                  disabled={healthChecking}
                >
                  <span class="material-symbols-outlined text-[16px]">speed</span>
                  Test Group
                </Button>
              </div>
            </div>
          {/each}
        </div>
        {/if}
      </div>
