<script lang="ts">
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import CountedSelect, { type CountedOption } from '../../lib/ui/CountedSelect.svelte'
  import type { ProxyPool } from '../../api/client'

  interface Props {
    pools: ProxyPool[]
    counts: { active: number; passed: number; failed: number }
    failedCount: number
    cooldownCount: number
    filteredFitness: ProxyPool[]
    filteredCooldown: ProxyPool[]
    typeOptions: CountedOption[]
    fitnessSearch: string
    fitnessType: string
    recovering: boolean
    clearingFitness: boolean
    healthChecking: boolean
    testingId: string | null
    handleRecover: () => void
    handleClearAllFitness: () => void
    handleHealthCheck: (forceAll?: boolean) => void
    handleTest: (id: string) => void
  }

  let {
    pools,
    counts,
    failedCount,
    cooldownCount,
    filteredFitness,
    filteredCooldown,
    typeOptions,
    fitnessSearch = $bindable(),
    fitnessType = $bindable(),
    recovering,
    clearingFitness,
    healthChecking,
    testingId,
    handleRecover,
    handleClearAllFitness,
    handleHealthCheck,
    handleTest,
  }: Props = $props()

  let page = $state(1)
  let pageSize = $state(15)

  $effect(() => {
    // Reset page on search or type change. The bare reads are what
    // $effect tracks; `void` states that intent to the linter.
    void fitnessSearch
    void fitnessType
    page = 1
  })

  let totalPages = $derived(Math.max(1, Math.ceil(filteredFitness.length / pageSize)))
  let paginatedFitness = $derived(
    filteredFitness.slice((page - 1) * pageSize, page * pageSize)
  )
</script>

      <div class="flex flex-col gap-4">
        <!-- Fitness filters -->
        <div class="flex flex-wrap items-center gap-2">
          <div class="relative">
            <span class="material-symbols-outlined pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[16px] text-text-muted">search</span>
            <input
              type="text"
              bind:value={fitnessSearch}
              placeholder="Search name, URL, group…"
              aria-label="Search fitness records"
              class="h-8 w-48 rounded-lg border border-border-subtle bg-surface-2 pl-7 pr-2 text-xs text-text-main outline-none placeholder:text-text-muted focus:border-primary/50"
            />
          </div>
          <div class="flex items-center gap-1.5">
            <span class="text-xs text-text-muted shrink-0">Type:</span>
            <CountedSelect
              value={fitnessType}
              options={typeOptions}
              ariaLabel="Filter fitness records by type"
              onChange={(next) => (fitnessType = next)}
            />
          </div>
          {#if fitnessSearch.trim() || fitnessType !== 'all'}
            <span class="text-xs text-text-muted">
              Showing {filteredFitness.length} of {pools.length} pools
            </span>
          {/if}
          <div class="ml-auto flex items-center gap-2">
            <Button
              size="sm"
              variant="secondary"
              onclick={handleRecover}
              disabled={recovering || healthChecking || (cooldownCount === 0 && failedCount === 0)}
            >
              <span class="material-symbols-outlined text-[16px] {recovering ? 'animate-spin' : ''}">healing</span>
              {recovering ? 'Recovering…' : 'Auto-Recover'}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onclick={handleClearAllFitness}
              disabled={clearingFitness || cooldownCount === 0}
            >
              <span class="material-symbols-outlined text-[16px]">lock_reset</span>
              Clear Cooldowns
            </Button>
          </div>
        </div>

        <!-- Telemetry Stats Cards -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div class="rounded-xl border border-border bg-surface p-3.5">
            <div class="text-[11px] font-medium text-text-muted">Total Monitored</div>
            <div class="text-xl font-bold text-text-main mt-1">{pools.length}</div>
            <div class="text-[11px] text-text-muted mt-0.5">{counts.active} active in cluster</div>
          </div>
          <div class="rounded-xl border border-border bg-surface p-3.5">
            <div class="text-[11px] font-medium text-text-muted">In Quarantine / Cooldown</div>
            <div class="text-xl font-bold mt-1 {cooldownCount > 0 ? 'text-red-500' : 'text-emerald-500'}">
              {cooldownCount}
            </div>
            <div class="text-[11px] text-text-muted mt-0.5">{cooldownCount > 0 ? 'Temporary penalty active' : 'Zero nodes quarantined'}</div>
          </div>
          <div class="rounded-xl border border-border bg-surface p-3.5">
            <div class="text-[11px] font-medium text-text-muted">Canary Probe Interval</div>
            <div class="text-xl font-bold text-text-main mt-1">10 min</div>
            <div class="text-[11px] text-text-muted mt-0.5">Cloudflare trace endpoint</div>
          </div>
          <div class="rounded-xl border border-border bg-surface p-3.5">
            <div class="text-[11px] font-medium text-text-muted">Quarantine Threshold</div>
            <div class="text-xl font-bold text-text-main mt-1">10 fails</div>
            <div class="text-[11px] text-text-muted mt-0.5">Consecutive canary drops</div>
          </div>
        </div>

        <!-- Active Quarantine Table (if any) -->
        {#if cooldownCount > 0}
          <Card>
            <div class="flex items-center justify-between mb-3">
              <div class="flex items-center gap-2">
                <span class="material-symbols-outlined text-red-500 text-[20px]">warning</span>
                <h2 class="text-sm font-semibold text-text-main">Nodes Currently in Cooldown</h2>
              </div>
              <span class="text-xs text-text-muted">Automatic rate-limit / canary quarantine</span>
            </div>
            <div class="overflow-x-auto">
              <table class="w-full text-left text-xs">
                <thead>
                  <tr class="border-b border-border/60 text-text-muted">
                    <th class="py-2 px-3 font-medium">Proxy Name</th>
                    <th class="py-2 px-3 font-medium">Group</th>
                    <th class="py-2 px-3 font-medium">Cooldown Remaining</th>
                    <th class="py-2 px-3 font-medium">Fitness Score</th>
                    <th class="py-2 px-3 font-medium">Canary Fails</th>
                    <th class="py-2 px-3 font-medium text-right">Action</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-border/30">
                  {#each filteredCooldown as pool (pool.id)}
                    <tr>
                      <td class="py-2.5 px-3">
                        <div class="font-medium text-text-main">{pool.name}</div>
                        <div class="text-[10px] text-text-muted font-mono">{pool.proxyUrl}</div>
                      </td>
                      <td class="py-2.5 px-3">
                        <span class="px-1.5 py-0.5 rounded bg-surface-2 text-text-muted border border-border/40 text-[10px]">
                          {pool.group || 'None'}
                        </span>
                      </td>
                      <td class="py-2.5 px-3">
                        <span class="font-mono text-red-500 font-semibold px-2 py-0.5 rounded bg-red-500/10 border border-red-500/20">
                          {pool.cooldownRemainingSec || 0} seconds
                        </span>
                      </td>
                      <td class="py-2.5 px-3">
                        <span class="font-mono text-red-500 font-medium">
                          {pool.fitnessScore ?? 0}%
                        </span>
                      </td>
                      <td class="py-2.5 px-3">
                        <span class="font-mono text-amber-500 font-medium">{pool.consecutiveProbeFails || 0} / 10</span>
                      </td>
                      <td class="py-2.5 px-3 text-right">
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={testingId === pool.id}
                          onclick={() => handleTest(pool.id)}
                        >
                          <span class="material-symbols-outlined text-[14px] {testingId === pool.id ? 'animate-spin' : ''}">science</span>
                          Retest & Clear
                        </Button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </Card>
        {/if}

        <!-- Leaderboard / Telemetry Table -->
        <Card>
          <div class="flex items-center justify-between mb-3">
            <div>
              <h2 class="text-sm font-semibold text-text-main">Fitness Score Telemetry</h2>
              <p class="text-xs text-text-muted">Sorted from lowest to highest score to easily identify degraded or blocked proxies.</p>
            </div>
            <Button
              size="sm"
              variant="secondary"
              disabled={healthChecking}
              onclick={() => handleHealthCheck(true)}
            >
              <span class="material-symbols-outlined text-[16px]">speed</span>
              Test All
            </Button>
          </div>

          <div class="overflow-x-auto">
            <table class="w-full text-left text-xs">
              <thead>
                <tr class="border-b border-border/60 text-text-muted">
                  <th class="py-2 px-3 font-medium">Node</th>
                  <th class="py-2 px-3 font-medium">Group</th>
                  <th class="py-2 px-3 font-medium">Fitness Score</th>
                  <th class="py-2 px-3 font-medium">EWMA Latency</th>
                  <th class="py-2 px-3 font-medium">Canary Probes</th>
                  <th class="py-2 px-3 font-medium">Status</th>
                  <th class="py-2 px-3 font-medium text-right">Test</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-border/30">
                {#each paginatedFitness as pool (pool.id)}
                  {@const score = pool.fitnessScore ?? 100}
                  <tr>
                    <td class="py-2.5 px-3">
                      <div class="font-medium text-text-main">{pool.name}</div>
                      <div class="text-[10px] text-text-muted font-mono">{pool.proxyUrl}</div>
                    </td>
                    <td class="py-2.5 px-3">
                      <span class="px-1.5 py-0.5 rounded bg-surface-2 text-text-muted border border-border/40 text-[10px]">
                        {pool.group || 'None'}
                      </span>
                    </td>
                    <td class="py-2.5 px-3">
                      <div class="flex items-center gap-2 min-w-[120px]">
                        <div class="flex-1 h-2 rounded-full bg-surface-3 overflow-hidden">
                          <div
                            class="h-full rounded-full transition-all {score >= 80 ? 'bg-emerald-500' : score >= 50 ? 'bg-amber-500' : 'bg-red-500'}"
                            style="width: {score}%"
                          ></div>
                        </div>
                        <span class="font-mono text-[11px] font-semibold {score >= 80 ? 'text-emerald-500' : score >= 50 ? 'text-amber-500' : 'text-red-500'}">
                          {score}%
                        </span>
                      </div>
                    </td>
                    <td class="py-2.5 px-3 font-mono">
                      {pool.ewmaLatencyMs ? `${Math.round(pool.ewmaLatencyMs)}ms` : pool.latency ? `${pool.latency}ms` : 'N/A'}
                    </td>
                    <td class="py-2.5 px-3">
                      {#if (pool.consecutiveProbeFails || 0) > 0}
                        <span class="text-amber-500 font-medium font-mono">{pool.consecutiveProbeFails}/10 fails</span>
                      {:else}
                        <span class="text-emerald-500 text-[11px]">Passing (10m)</span>
                      {/if}
                    </td>
                    <td class="py-2.5 px-3">
                      {#if pool.inCooldown}
                        <span class="text-red-500 font-semibold text-[10px] px-1.5 py-0.5 rounded bg-red-500/10 border border-red-500/20 animate-pulse">
                          COOLDOWN ({pool.cooldownRemainingSec || 0}s)
                        </span>
                      {:else if !pool.isActive}
                        <span class="text-text-muted text-[10px]">Disabled</span>
                      {:else}
                        <span class="text-emerald-500 text-[10px]">Healthy</span>
                      {/if}
                    </td>
                    <td class="py-2.5 px-3 text-right">
                      <button
                        type="button"
                        onclick={() => handleTest(pool.id)}
                        disabled={testingId === pool.id}
                        class="p-1 rounded text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer"
                        title="Run probe test"
                      >
                        <span class="material-symbols-outlined text-[15px] {testingId === pool.id ? 'animate-spin' : ''}">science</span>
                      </button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>

          {#if totalPages > 1 || filteredFitness.length > 15}
            <div class="flex flex-col sm:flex-row items-center justify-between gap-3 pt-4 mt-2 border-t border-border/40 text-xs text-text-muted">
              <div class="flex items-center gap-2">
                <span>Showing {(page - 1) * pageSize + 1}–{Math.min(page * pageSize, filteredFitness.length)} of {filteredFitness.length} nodes</span>
                <span class="opacity-40">|</span>
                <div class="flex items-center gap-1.5">
                  <span>Per page:</span>
                  <select
                    bind:value={pageSize}
                    class="bg-surface-2 border border-border/40 rounded px-1.5 py-0.5 text-xs text-text-main focus:outline-none focus:border-primary cursor-pointer"
                  >
                    <option value={15}>15</option>
                    <option value={30}>30</option>
                    <option value={50}>50</option>
                    <option value={100}>100</option>
                  </select>
                </div>
              </div>

              <div class="flex items-center gap-1">
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={page <= 1}
                  onclick={() => { page = Math.max(1, page - 1) }}
                >
                  Previous
                </Button>
                <span class="px-2 font-mono text-[11px] text-text-muted">{page} / {totalPages}</span>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={page >= totalPages}
                  onclick={() => { page = Math.min(totalPages, page + 1) }}
                >
                  Next
                </Button>
              </div>
            </div>
          {/if}
        </Card>

        <!-- Technical Scoring Info -->
        <div class="rounded-xl border border-border/50 bg-surface-2 p-4 text-xs text-text-muted space-y-2">
          <div class="font-semibold text-text-main flex items-center gap-1.5">
            <span class="material-symbols-outlined text-[16px] text-primary">info</span>
            Smart Fitness & Cooldown Mechanics
          </div>
          <p>
            The fitness engine continuously computes composite health: baseline starts at 100%, latency over 1500ms applies proportional penalties, while HTTP 429 / 403 / connection drops deduct 25 points. Nodes failing 10 consecutive canary probes enter automatic quarantine with exponential cooldown backoff (30s to 600s).
          </p>
        </div>
      </div>
