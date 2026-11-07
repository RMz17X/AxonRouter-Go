<script lang="ts">
  // Port of upstream src/app/(dashboard)/dashboard/proxy-pools/page.js
  import { onMount } from 'svelte'
  import Badge from '../lib/ui/Badge.svelte'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import Input from '../lib/ui/Input.svelte'
  import Modal from '../lib/ui/Modal.svelte'
  import Toggle from '../lib/ui/Toggle.svelte'
  import Menu from '../lib/ui/Menu.svelte'
  import MenuItem from '../lib/ui/MenuItem.svelte'
  import CountedSelect from '../lib/ui/CountedSelect.svelte'
  import { api, getAuthHeaders, type ProxyPool } from '../api/client'
  import GroupPickerModal from './proxypools/GroupPickerModal.svelte'
  import GroupMembersModal from './proxypools/GroupMembersModal.svelte'
  import PoolRow from './proxypools/PoolRow.svelte'
  import RelayModals from './proxypools/RelayModals.svelte'
  import GroupsTab from './proxypools/GroupsTab.svelte'
  import FitnessTab from './proxypools/FitnessTab.svelte'
  import { notifications } from '../lib/notifications'
  import { parseProxyLine } from '../lib/proxy-import'
  import {
    filterProxyPools,
    isFailedStatus,
    poolMatchesSearch,
    poolStatusCounts,
    poolTypeKey,
    poolTypeOptions,
    type GroupSummary,
    sortProxyPools,
    type PoolSortOption,
    type PoolStatusFilter
  } from './proxypools/helpers'

  interface BulkOutcome {
    ok: number
    blocked: number
    failed: number
  }

  interface PoolForm {
    name: string
    group: string
    proxyUrl: string
    noProxy: string
    isActive: boolean
    strictProxy: boolean
  }

  function normalizeFormData(data: Partial<ProxyPool> = {}): PoolForm {
    return {
      name: data.name || '',
      group: data.group || '',
      proxyUrl: data.proxyUrl || '',
      noProxy: data.noProxy || '',
      isActive: data.isActive !== false,
      strictProxy: data.strictProxy === true,
    }
  }

  let proxyPools = $state<ProxyPool[]>([])
  let loading = $state(true)
  let showFormModal = $state(false)
  // Which half of the add dialog is showing. Adding one pool by hand and adding
  // a pasted list were two separate modals opened by two separate buttons; they
  // are one task, so they became one dialog with two tabs (issue #224).
  let formTab = $state<'single' | 'bulk'>('single')
  let editingPool = $state<ProxyPool | null>(null)
  let formData = $state<PoolForm>(normalizeFormData())
  let batchImportText = $state('')
  // Bulk import group targeting: '' = no group, '__new__' = the name typed
  // below, anything else = an existing group name.
  let batchGroupChoice = $state('')
  let batchGroupNew = $state('')
  let batchTargetGroup = $derived(
    batchGroupChoice === '__new__' ? batchGroupNew.trim() : batchGroupChoice
  )

  let saving = $state(false)
  let importing = $state(false)
  let testingId = $state<string | null>(null)
  let selectedIds = $state<string[]>([])
  let healthChecking = $state(false)
  let healthProgress = $state({ current: 0, total: 0 })
  let bulkBusy = $state(false)
  let confirmState = $state<{
    title: string
    message: string
    confirmText?: string
    onConfirm: () => Promise<void> | void
  } | null>(null)

  // Tested pools float to the top (most recently tested first);
  // never-tested pools keep backend order below.
  // Array.prototype.sort is stable, so ties preserve fetch order.
  function sortPools(pools: ProxyPool[]): ProxyPool[] {
    return [...pools].sort((a, b) => {
      const at = a.lastTestedAt ? Date.parse(a.lastTestedAt) || 0 : 0
      const bt = b.lastTestedAt ? Date.parse(b.lastTestedAt) || 0 : 0
      if (at !== 0 || bt !== 0) return bt - at
      return 0
    })
  }

  async function fetchProxyPools() {
    try {
      proxyPools = sortPools(await api.getProxyPools(true))
    } catch (err) {
      console.log('Error fetching proxy pools:', err)
    } finally {
      loading = false
    }
  }

  onMount(() => {
    fetchProxyPools()
  })

  // Drop selections for pools that no longer exist.
  // Guard the write: assigning a fresh array unconditionally would retrigger
  // this effect forever (new identity each run) and freeze the page on loading.
  $effect(() => {
    const ids = new Set(proxyPools.map((p) => p.id))
    if (selectedIds.some((id) => !ids.has(id))) {
      selectedIds = selectedIds.filter((id) => ids.has(id))
    }
  })

  let statusFilter = $state<PoolStatusFilter>('all')
  let groupFilter = $state<string>('all')
  let typeFilter = $state<string>('all')
  let searchQuery = $state('')
  let sortOption = $state<PoolSortOption>('default')
  let activeViewTab = $state<'pools' | 'groups' | 'fitness'>('pools')

  let counts = $derived(poolStatusCounts(proxyPools))
  let activeCount = $derived(counts.active)
  let failedCount = $derived(counts.failed)
  let cooldownCount = $derived(proxyPools.filter((p) => p.inCooldown).length)

  let availableGroups = $derived.by(() => {
    const set = new Set<string>()
    for (const p of proxyPools) {
      if (p.group && p.group.trim()) {
        set.add(p.group.trim())
      }
    }
    return Array.from(set).sort()
  })

  let groupSummaries = $derived.by<GroupSummary[]>(() => {
    const groups = new Map<string, ProxyPool[]>()
    for (const p of proxyPools) {
      const g = (p.group || '').trim()
      if (!g) continue
      if (!groups.has(g)) groups.set(g, [])
      groups.get(g)!.push(p)
    }
    const result: GroupSummary[] = []
    for (const [name, pools] of groups.entries()) {
      const active = pools.filter((p) => p.isActive)
      const inCooldown = pools.filter((p) => p.inCooldown)
      const strict = pools.filter((p) => p.strictProxy)
      const scoredPools = pools.filter((p) => typeof p.fitnessScore === 'number')
      const avgFitness =
        scoredPools.length > 0
          ? Math.round(
              scoredPools.reduce((acc, p) => acc + (p.fitnessScore || 0), 0) / scoredPools.length
            )
          : 100
      const latPools = pools.filter((p) => typeof p.latency === 'number' && (p.latency || 0) > 0)
      const avgLatency =
        latPools.length > 0
          ? Math.round(latPools.reduce((acc, p) => acc + (p.latency || 0), 0) / latPools.length)
          : 0

      result.push({
        name,
        pools,
        totalCount: pools.length,
        activeCount: active.length,
        inCooldownCount: inCooldown.length,
        avgFitness,
        avgLatency,
        strictCount: strict.length,
      })
    }
    return result.sort((a, b) =>
      a.name === 'Default' ? 1 : b.name === 'Default' ? -1 : a.name.localeCompare(b.name)
    )
  })

  let fitnessLeaderboard = $derived.by(() => {
    return [...proxyPools].sort((a, b) => {
      const sa = a.fitnessScore ?? 100
      const sb = b.fitnessScore ?? 100
      return sa - sb
    })
  })

  // Fitness tab filters (the JS fitness tab filters its records by type and
  // search; the pools here get the same treatment) + recovery actions whose
  // backend endpoints existed but were never surfaced.
  let fitnessSearch = $state('')
  let fitnessType = $state<string>('all')
  let recovering = $state(false)
  let clearingFitness = $state(false)

  let filteredFitness = $derived.by(() => {
    let rows = fitnessLeaderboard
    if (fitnessType !== 'all') rows = rows.filter((p) => poolTypeKey(p) === fitnessType)
    if (fitnessSearch.trim()) rows = rows.filter((p) => poolMatchesSearch(p, fitnessSearch))
    return rows
  })
  let filteredCooldown = $derived(filteredFitness.filter((p) => p.inCooldown))

  async function handleRecover() {
    recovering = true
    try {
      const data = await api.recoverProxyPools()
      await fetchProxyPools()
      if (data.error) {
        notifications.warning(`Recovery ran with an error: ${data.error}`)
      } else {
        notifications.success(
          `Auto-recover: ${data.recovered} recovered of ${data.checked} checked (${data.stillFailing} still failing)`
        )
      }
    } catch (err) {
      console.log('Error recovering proxy pools:', err)
      notifications.error('Auto-recover failed')
    } finally {
      recovering = false
    }
  }

  function handleClearAllFitness() {
    confirmState = {
      title: 'Clear All Cooldowns',
      message:
        'Clear every fitness and cooldown mark? Quarantined pools become eligible again immediately; the canary will re-quarantine any that still fail.',
      confirmText: 'Clear All',
      onConfirm: async () => {
        confirmState = null
        clearingFitness = true
        try {
          await api.clearAllProxyFitness()
          await fetchProxyPools()
          notifications.success('All fitness and cooldown marks cleared')
        } catch (err) {
          console.log('Error clearing fitness marks:', err)
          notifications.error('Failed to clear cooldowns')
        } finally {
          clearingFitness = false
        }
      },
    }
  }

  async function testGroup(grpName: string) {
    const poolsInGroup = proxyPools.filter(
      (p) => (p.group || '').trim() === (grpName === 'Default' ? '' : grpName)
    )
    if (poolsInGroup.length === 0) return
    const oldSelected = [...selectedIds]
    selectedIds = poolsInGroup.map((p) => p.id)
    await handleHealthCheck(false)
    selectedIds = oldSelected
  }

  let filteredPools = $derived.by(() => {
    let pools = filterProxyPools(proxyPools, statusFilter)
    if (groupFilter !== 'all') {
      pools = pools.filter((p) => (p.group || '').trim() === groupFilter)
    }
    if (typeFilter !== 'all') {
      pools = pools.filter((p) => poolTypeKey(p) === typeFilter)
    }
    if (searchQuery.trim()) {
      pools = pools.filter((p) => poolMatchesSearch(p, searchQuery))
    }
    return pools
  })
  let displayedPools = $derived(sortProxyPools(filteredPools, sortOption))

  let pageSize = $state<number>(10)
  let currentPage = $state<number>(1)

  let totalPages = $derived(Math.max(1, Math.ceil(displayedPools.length / pageSize)))
  let paginatedPools = $derived(
    displayedPools.slice((currentPage - 1) * pageSize, currentPage * pageSize)
  )

  $effect(() => {
    // Reset to page 1 whenever filter or sort changes
    const _ = statusFilter + sortOption + groupFilter + typeFilter + searchQuery
    currentPage = 1
  })

  $effect(() => {
    if (currentPage > totalPages) {
      currentPage = totalPages
    }
  })

  // The status filter is a dropdown rather than four pills with the count in
  // brackets: beside the sort picker and the two bulk-cleanup buttons that row
  // was the widest fixed thing left on the page, and it wrapped to three lines
  // on a phone (issue #261).
  let statusOptions = $derived([
    { value: 'all', label: 'All', count: proxyPools.length },
    { value: 'active', label: 'Active', count: counts.active },
    { value: 'passed', label: 'Passed', count: counts.passed },
    { value: 'failed', label: 'Failed', count: counts.failed }
  ] satisfies { value: PoolStatusFilter; label: string; count: number }[])

  let typeOptions = $derived([
    { value: 'all', label: 'All', count: proxyPools.length },
    ...poolTypeOptions(proxyPools)
  ])

  let allSelected = $derived(
    paginatedPools.length > 0 && paginatedPools.every((p) => selectedIds.includes(p.id))
  )

  function toggleSelect(id: string) {
    selectedIds = selectedIds.includes(id)
      ? selectedIds.filter((x) => x !== id)
      : [...selectedIds, id]
  }

  function toggleSelectAll() {
    if (allSelected) {
      const pageIdSet = new Set(paginatedPools.map((p) => p.id))
      selectedIds = selectedIds.filter((id) => !pageIdSet.has(id))
    } else {
      const set = new Set([...selectedIds, ...paginatedPools.map((p) => p.id)])
      selectedIds = Array.from(set)
    }
  }

  function clearSelection() {
    selectedIds = []
  }

  function resetForm() {
    editingPool = null
    formData = normalizeFormData()
  }

  function openCreateModal(tab: 'single' | 'bulk' = 'single', presetGroup = '') {
    resetForm()
    batchImportText = ''
    batchGroupChoice = presetGroup
    batchGroupNew = ''
    if (presetGroup) formData = { ...formData, group: presetGroup }
    formTab = tab
    showFormModal = true
  }

  function openEditModal(pool: ProxyPool) {
    editingPool = pool
    formData = normalizeFormData(pool)
    formTab = 'single'
    showFormModal = true
  }

  function closeFormModal() {
    // A save or a bulk import already running writes rows, so dismissing the
    // dialog mid-flight would leave the operator with no view of what landed.
    if (saving || importing) return
    showFormModal = false
    resetForm()
    batchImportText = ''
    batchGroupChoice = ''
    batchGroupNew = ''
    formTab = 'single'
  }

  async function handleSave() {
    const payload = {
      name: formData.name.trim(),
      group: formData.group.trim(),
      proxyUrl: formData.proxyUrl.trim(),
      noProxy: formData.noProxy.trim(),
      isActive: formData.isActive === true,
      strictProxy: formData.strictProxy === true,
    }
    if (!payload.name || !payload.proxyUrl) return
    saving = true
    try {
      if (editingPool) {
        await api.updateProxyPool(editingPool.id, payload)
      } else {
        await api.createProxyPool(payload)
      }
      await fetchProxyPools()
      closeFormModal()
      notifications.success(editingPool ? 'Proxy pool updated' : 'Proxy pool created')
    } catch (err) {
      console.log('Error saving proxy pool:', err)
      notifications.error(err instanceof Error ? err.message : 'Failed to save proxy pool')
    } finally {
      saving = false
    }
  }

  function handleDelete(pool: ProxyPool) {
    confirmState = {
      title: 'Delete Proxy Pool',
      message: `Delete proxy pool "${pool.name}"?`,
      onConfirm: async () => {
        confirmState = null
        try {
          const res = await fetch(`/api/proxy-pools/${encodeURIComponent(pool.id)}`, {
            method: 'DELETE',
            headers: getAuthHeaders(),
          })
          if (res.ok) {
            proxyPools = proxyPools.filter((item) => item.id !== pool.id)
            notifications.success('Proxy pool deleted')
            return
          }
          const data = await res.json().catch(() => ({}))
          if (res.status === 409) {
            notifications.warning(
              `Cannot delete: ${data.boundConnectionCount || 0} connection(s) are still using this pool.`
            )
          } else {
            notifications.error(data.error || data?.error?.message || 'Failed to delete proxy pool')
          }
        } catch (err) {
          console.log('Error deleting proxy pool:', err)
          notifications.error('Failed to delete proxy pool')
        }
      },
    }
  }

  async function handleTest(poolId: string) {
    testingId = poolId
    try {
      const data = await api.testProxyPool(poolId)
      proxyPools = proxyPools.map((p) =>
        p.id === poolId
          ? {
              ...p,
              testStatus: data.status || (data.success ? 'passed' : 'failed'),
              latency: typeof data.latency === 'number' ? data.latency : p.latency,
              lastTestedAt: new Date().toISOString(),
              exitIpv4: data.exitIpv4 ?? p.exitIpv4,
              exitIpv6: data.exitIpv6 ?? p.exitIpv6,
              exitCountry: data.exitCountry ?? p.exitCountry,
              exitCity: data.exitCity ?? p.exitCity,
              exitIsp: data.exitIsp ?? p.exitIsp,
              exitOrg: data.exitOrg ?? p.exitOrg,
              exitAsn: data.exitAsn ?? p.exitAsn,
            }
          : p
      )
      await fetchProxyPools()
      if (data.success) {
        notifications.success('Proxy test passed')
      } else {
        notifications.error(data.error || 'Proxy test failed')
      }
    } catch (err) {
      console.log('Error testing proxy pool:', err)
      notifications.error('Failed to test proxy')
    } finally {
      testingId = null
    }
  }

  async function handleToggleActive(pool: ProxyPool) {
    const next = !pool.isActive
    proxyPools = proxyPools.map((p) => (p.id === pool.id ? { ...p, isActive: next } : p))
    try {
      await api.updateProxyPool(pool.id, { isActive: next })
    } catch (err) {
      console.log('Error toggling active:', err)
      proxyPools = proxyPools.map((p) => (p.id === pool.id ? { ...p, isActive: pool.isActive } : p))
      notifications.error('Failed to update active state')
    }
  }

  // Group assignment: one picker serves the per-row action and the bulk bar.
  // Groups are string tags, so assigning is a group-field write per pool.
  let groupPicker = $state<{ mode: 'single'; pool: ProxyPool } | { mode: 'bulk' } | null>(null)
  let groupAssigning = $state(false)

  let groupCounts = $derived(
    availableGroups.map((name) => ({
      name,
      count: proxyPools.filter((p) => (p.group || '').trim() === name).length,
    }))
  )

  async function assignGroup(ids: string[], group: string | null) {
    groupAssigning = true
    let ok = 0
    let failed = 0
    try {
      for (const id of ids) {
        try {
          await api.updateProxyPool(id, { group: group ?? '' })
          ok += 1
        } catch {
          failed += 1
        }
      }
      await fetchProxyPools()
      const what = group ? `group "${group}"` : 'no group'
      if (failed === 0) notifications.success(`Set ${ok} pool${ok === 1 ? '' : 's'} to ${what}`)
      else notifications.warning(`Set ${ok} to ${what}, ${failed} failed`)
    } finally {
      groupAssigning = false
      groupPicker = null
    }
  }

  // Group member management from the Groups tab: the modal returns the
  // desired member set; only the diff is written.
  let membersModalGroup = $state<string | null>(null)
  let membersSaving = $state(false)

  async function saveGroupMembers(memberIds: string[]) {
    const name = membersModalGroup
    if (!name) return
    const want = new Set(memberIds)
    const joins = proxyPools.filter(
      (p) => want.has(p.id) && (p.group || '').trim() !== name
    )
    const leaves = proxyPools.filter(
      (p) => !want.has(p.id) && (p.group || '').trim() === name
    )
    if (joins.length === 0 && leaves.length === 0) {
      membersModalGroup = null
      return
    }
    membersSaving = true
    let ok = 0
    let failed = 0
    try {
      for (const p of joins) {
        try {
          await api.updateProxyPool(p.id, { group: name })
          ok += 1
        } catch {
          failed += 1
        }
      }
      for (const p of leaves) {
        try {
          await api.updateProxyPool(p.id, { group: '' })
          ok += 1
        } catch {
          failed += 1
        }
      }
      await fetchProxyPools()
      if (failed === 0)
        notifications.success(`Group "${name}" updated (${ok} change${ok === 1 ? '' : 's'})`)
      else notifications.warning(`Group "${name}": ${ok} updated, ${failed} failed`)
    } finally {
      membersSaving = false
      membersModalGroup = null
    }
  }

  function handleGroupPick(group: string | null) {
    if (!groupPicker) return
    const ids =
      groupPicker.mode === 'single' ? [groupPicker.pool.id] : [...selectedIds]
    if (ids.length > 0) void assignGroup(ids, group)
  }

  async function bulkSetActive(isActive: boolean) {
    const targets = selectedIds.length > 0 ? selectedIds : proxyPools.map((p) => p.id)
    if (targets.length === 0) return
    bulkBusy = true
    try {
      let ok = 0
      let failed = 0
      for (const id of targets) {
        try {
          await api.updateProxyPool(id, { isActive })
          ok += 1
        } catch {
          failed += 1
        }
      }
      await fetchProxyPools()
      notifications.success(
        `${isActive ? 'Activated' : 'Deactivated'} ${ok}${failed ? `, failed ${failed}` : ''}`
      )
    } finally {
      bulkBusy = false
    }
  }

  function bulkDelete() {
    if (selectedIds.length === 0) return
    const count = selectedIds.length
    confirmState = {
      title: 'Delete Proxy Pools',
      message: `Delete ${count} proxy pool(s)?`,
      onConfirm: async () => {
        confirmState = null
        bulkBusy = true
        try {
          const outcome = await deletePools(selectedIds)
          await fetchProxyPools()
          clearSelection()
          reportBulk(outcome, 'Deleted')
        } finally {
          bulkBusy = false
        }
      },
    }
  }

  // The raw fetch is deliberate here: the gateway answers 409 while a pool is
  // still bound, and the caller has to tell that apart from a real failure
  // instead of collapsing both into one thrown Error.
  async function deletePools(ids: string[]): Promise<BulkOutcome> {
    const outcome: BulkOutcome = { ok: 0, blocked: 0, failed: 0 }
    for (const id of ids) {
      try {
        const res = await fetch(`/api/proxy-pools/${encodeURIComponent(id)}`, {
          method: 'DELETE',
          headers: getAuthHeaders(),
        })
        if (res.ok) outcome.ok += 1
        else if (res.status === 409) outcome.blocked += 1
        else outcome.failed += 1
      } catch {
        outcome.failed += 1
      }
    }
    return outcome
  }

  async function disablePools(ids: string[]): Promise<BulkOutcome> {
    const outcome: BulkOutcome = { ok: 0, blocked: 0, failed: 0 }
    for (const id of ids) {
      try {
        await api.updateProxyPool(id, { isActive: false })
        outcome.ok += 1
      } catch {
        outcome.failed += 1
      }
    }
    return outcome
  }

  // A bulk action must never claim more than the server confirmed: a pool left
  // active keeps carrying live traffic, and the user needs to see that.
  function reportBulk(outcome: BulkOutcome, verb: string) {
    const parts = [`${verb} ${outcome.ok}`]
    if (outcome.blocked > 0) parts.push(`${outcome.blocked} bound to active connections`)
    if (outcome.failed > 0) parts.push(`${outcome.failed} failed`)
    const summary = parts.join(', ')
    if (outcome.blocked === 0 && outcome.failed === 0) notifications.success(summary)
    else notifications.warning(summary)
  }

  async function handleHealthCheck(forceAll = false) {
    const targets =
      !forceAll && selectedIds.length > 0
        ? proxyPools.filter((p) => selectedIds.includes(p.id))
        : proxyPools
    if (targets.length === 0) return
    healthChecking = true
    healthProgress = { current: 0, total: targets.length }
    let alive = 0
    const deadIds: string[] = []
    let done = 0
    const CONCURRENCY = 10
    const queue = [...targets]

    const worker = async () => {
      while (queue.length > 0) {
        const pool = queue.shift()
        if (!pool) break
        try {
          const data = await api.testProxyPool(pool.id)
          if (data.success) {
            alive += 1
          } else {
            deadIds.push(pool.id)
          }
          proxyPools = proxyPools.map((p) =>
            p.id === pool.id
              ? {
                  ...p,
                  testStatus: data.status || (data.success ? 'passed' : 'failed'),
                  latency: typeof data.latency === 'number' ? data.latency : p.latency,
                  lastTestedAt: new Date().toISOString(),
                  exitIpv4: data.exitIpv4 ?? p.exitIpv4,
                  exitIpv6: data.exitIpv6 ?? p.exitIpv6,
                  exitCountry: data.exitCountry ?? p.exitCountry,
                  exitCity: data.exitCity ?? p.exitCity,
                  exitIsp: data.exitIsp ?? p.exitIsp,
                  exitOrg: data.exitOrg ?? p.exitOrg,
                  exitAsn: data.exitAsn ?? p.exitAsn,
                }
              : p
          )
        } catch {
          deadIds.push(pool.id)
          proxyPools = proxyPools.map((p) =>
            p.id === pool.id
              ? {
                  ...p,
                  testStatus: 'failed',
                  latency: 0,
                  lastTestedAt: new Date().toISOString(),
                }
              : p
          )
        } finally {
          done += 1
          healthProgress = { current: done, total: targets.length }
        }
      }
    }

    await Promise.all(
      Array.from({ length: Math.min(CONCURRENCY, targets.length) }, () => worker())
    )
    await fetchProxyPools()
    healthChecking = false
    healthProgress = { current: 0, total: 0 }

    if (deadIds.length > 0) {
      const dead = deadIds.length
      confirmState = {
        title: 'Disable Dead Proxies',
        message: `Alive: ${alive}, Dead: ${dead}.\n\nDisable ${dead} dead ${dead === 1 ? 'proxy' : 'proxies'}?`,
        confirmText: 'Disable Dead',
        onConfirm: async () => {
          confirmState = null
          bulkBusy = true
          try {
            const outcome = await disablePools(deadIds)
            await fetchProxyPools()
            reportBulk(outcome, 'Disabled')
          } finally {
            bulkBusy = false
          }
        },
      }
    } else {
      notifications.success(`Health check done. Alive: ${alive}, Dead: ${deadIds.length}`)
    }
  }

  async function handleDisableFailed() {
    const failed = proxyPools.filter((p) => isFailedStatus(p) && p.isActive !== false)
    if (failed.length === 0) {
      notifications.info('No active failed proxies found')
      return
    }
    bulkBusy = true
    try {
      const outcome = await disablePools(failed.map((p) => p.id))
      await fetchProxyPools()
      reportBulk(outcome, 'Disabled')
    } finally {
      bulkBusy = false
    }
  }

  function handleDeleteFailed() {
    const failed = proxyPools.filter(isFailedStatus)
    if (failed.length === 0) {
      notifications.info('No failed proxies to delete')
      return
    }
    confirmState = {
      title: 'Delete Failed Proxies',
      message: `Are you sure you want to delete ${failed.length} failed proxy pool${failed.length === 1 ? '' : 's'}? This action cannot be undone.`,
      confirmText: 'Delete Failed',
      onConfirm: async () => {
        confirmState = null
        bulkBusy = true
        try {
          const outcome = await deletePools(failed.map((p) => p.id))
          await fetchProxyPools()
          reportBulk(outcome, 'Deleted')
        } finally {
          bulkBusy = false
        }
      },
    }
  }

  async function handleBatchImport() {
    const lines = batchImportText
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter(Boolean)

    if (lines.length === 0) {
      notifications.warning('Please paste at least one proxy line.')
      return
    }

    // Partial import (upstream behaviour): valid lines are imported, invalid
    // ones are reported and left in the box for fixing instead of aborting
    // the whole paste.
    const parsedEntries: { proxyUrl: string; name: string }[] = []
    const invalidLines: string[] = []
    const invalidRaw: string[] = []

    lines.forEach((line, index) => {
      try {
        const parsed = parseProxyLine(line)
        if (parsed) parsedEntries.push(parsed)
      } catch (err) {
        invalidLines.push(`Line ${index + 1}: ${err instanceof Error ? err.message : 'Invalid'}`)
        invalidRaw.push(line)
      }
    })

    importing = true
    try {
      const existingKeys = new Set(
        proxyPools.map((pool) => `${(pool.proxyUrl || '').trim()}|||${(pool.noProxy || '').trim()}`)
      )

      let created = 0
      let skipped = 0
      let failed = 0

      // Dedupe against the database AND within the paste itself, then create
      // with bounded concurrency — a 200-line paste used to be 200 serial
      // round-trips.
      const fresh = parsedEntries.filter((entry) => {
        const dedupeKey = `${entry.proxyUrl}|||`
        if (existingKeys.has(dedupeKey)) {
          skipped += 1
          return false
        }
        existingKeys.add(dedupeKey)
        return true
      })

      const CHUNK = 8
      for (let i = 0; i < fresh.length; i += CHUNK) {
        const chunk = fresh.slice(i, i + CHUNK)
        const results = await Promise.allSettled(
          chunk.map((entry) =>
            api.createProxyPool({
              name: entry.name,
              proxyUrl: entry.proxyUrl,
              noProxy: '',
              group: batchTargetGroup,
              isActive: true,
            })
          )
        )
        for (const r of results) {
          if (r.status === 'fulfilled') created += 1
          else failed += 1
        }
      }

      await fetchProxyPools()
      const summary = `Created ${created}, Skipped ${skipped}, Failed ${failed}`
      if (invalidRaw.length > 0) {
        // Keep the dialog open with only the rejected lines left to fix.
        batchImportText = invalidRaw.join('\n')
        notifications.warning(
          `Batch import: ${summary}. ${invalidLines.length} invalid line${invalidLines.length === 1 ? '' : 's'} left in the box:\n${invalidLines.slice(0, 5).join('\n')}`
        )
      } else {
        // closeFormModal refuses while `importing` is still true, which is
        // why this clears the draft directly instead of going through it.
        showFormModal = false
        batchImportText = ''
        batchGroupChoice = ''
        batchGroupNew = ''
        formTab = 'single'
        if (failed > 0) notifications.warning(`Batch import completed: ${summary}`)
        else notifications.success(`Batch import completed: ${summary}`)
      }
    } catch (err) {
      console.log('Error batch importing proxies:', err)
      notifications.error('Batch import failed')
    } finally {
      importing = false
    }
  }

</script>

<!-- The one-pool form is shared by the Single tab and the edit dialog, so the
     fields live in one snippet rather than being written twice. -->
{#snippet singleFields()}
  <Input label="Name" bind:value={formData.name} placeholder="Office Proxy" />
  <div class="flex flex-col gap-1.5">
    <label class="text-sm font-medium text-text-main" for="pool-form-group">
      Group / Tag (optional)
    </label>
    <input
      id="pool-form-group"
      type="text"
      bind:value={formData.group}
      placeholder="e.g. premium, dedicated-asia"
      list="pool-group-names"
      class="w-full rounded-sm border border-border bg-surface px-3 py-2 text-[16px] text-text-main outline-none transition-all placeholder:text-text-muted/60 focus:border-primary/60 focus:ring-2 focus:ring-primary/15 sm:text-sm"
    />
    <datalist id="pool-group-names">
      {#each availableGroups as grp}
        <option value={grp}></option>
      {/each}
    </datalist>
    <p class="text-xs text-text-muted">
      Assign to a group for provider smart load balancing and failover
    </p>
  </div>
  <Input label="Proxy URL" bind:value={formData.proxyUrl} placeholder="http://127.0.0.1:7897" />
  <Input
    label="No Proxy"
    bind:value={formData.noProxy}
    placeholder="localhost,127.0.0.1,.internal"
    hint="Comma-separated hosts/domains to bypass proxy"
  />

  <div
    class="flex flex-col gap-3 rounded-lg border border-border/50 p-3 sm:flex-row sm:items-center sm:justify-between"
  >
    <div>
      <p class="font-medium text-sm">Active</p>
      <p class="text-xs text-text-muted">Inactive pools are ignored by runtime resolution.</p>
    </div>
    <Toggle
      checked={formData.isActive === true}
      onChange={() => (formData.isActive = !formData.isActive)}
      disabled={saving}
    />
  </div>

  <div
    class="flex flex-col gap-3 rounded-lg border border-border/50 p-3 sm:flex-row sm:items-center sm:justify-between"
  >
    <div>
      <p class="font-medium text-sm">Strict Proxy</p>
      <p class="text-xs text-text-muted">
        Fail request if proxy is unreachable instead of falling back to direct.
      </p>
    </div>
    <Toggle
      checked={formData.strictProxy === true}
      onChange={() => (formData.strictProxy = !formData.strictProxy)}
      disabled={saving}
    />
  </div>
{/snippet}

{#if loading}
  <div class="flex w-full flex-col gap-4 px-1 sm:gap-6 sm:px-0">
    <div class="h-20 rounded-brand-lg bg-surface border border-border-subtle animate-pulse"></div>
    <div class="h-64 rounded-brand-lg bg-surface border border-border-subtle animate-pulse"></div>
  </div>
{:else}
  <div class="flex w-full flex-col gap-4 px-1 sm:gap-6 sm:px-0">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold sm:text-2xl">Proxy Pools</h1>
      </div>

      <!-- Test All, Batch Import and Add Proxy Pool moved into one menu: the
           header held four controls and wrapped on a phone (issue #261). The
           relay deployer keeps its own trigger — it is a separate task with
           its own submenu, not one more verb in this list. -->
      <div class="flex flex-wrap items-center gap-2">

        <RelayModals onDeployed={fetchProxyPools} />
        <Menu label="Proxy pool actions" triggerIcon="menu" minWidth="15rem">
          <MenuItem
            label={healthChecking
              ? `Testing ${healthProgress.current}/${healthProgress.total}`
              : 'Test All'}
            icon={healthChecking ? 'progress_activity' : 'speed'}
            disabled={healthChecking || proxyPools.length === 0}
            onSelect={() => handleHealthCheck(true)}
          />

          <div class="my-1 border-t border-border-subtle" role="separator"></div>

          <MenuItem label="Add Proxy Pool" icon="add" onSelect={() => openCreateModal('single')} />
          <MenuItem label="Batch Import" icon="upload" onSelect={() => openCreateModal('bulk')} />
        </Menu>
      </div>
    </div>

    <!-- View Switcher Tabs: All Pools | Proxy Groups | Smart Fitness & Canary -->
    <div class="flex items-center gap-1.5 overflow-x-auto pb-1 sm:pb-0">
      <button
        type="button"
        onclick={() => (activeViewTab = 'pools')}
        class="flex items-center gap-2 rounded-lg px-3.5 py-2 text-xs font-medium transition-all cursor-pointer {activeViewTab === 'pools' ? 'bg-surface text-text-main shadow-sm border border-border font-semibold' : 'bg-surface-2 text-text-muted hover:text-text-main border border-transparent'}"
      >
        <span class="material-symbols-outlined text-[16px] {activeViewTab === 'pools' ? 'text-primary' : ''}">lan</span>
        <span>Proxy Pools</span>
        <span class="rounded-full px-2 py-0.5 text-[10px] {activeViewTab === 'pools' ? 'bg-primary/10 text-primary font-semibold' : 'bg-surface-3 text-text-muted'}">{proxyPools.length}</span>
      </button>

      <button
        type="button"
        onclick={() => (activeViewTab = 'groups')}
        class="flex items-center gap-2 rounded-lg px-3.5 py-2 text-xs font-medium transition-all cursor-pointer {activeViewTab === 'groups' ? 'bg-surface text-text-main shadow-sm border border-border font-semibold' : 'bg-surface-2 text-text-muted hover:text-text-main border border-transparent'}"
      >
        <span class="material-symbols-outlined text-[16px] {activeViewTab === 'groups' ? 'text-primary' : ''}">folder_special</span>
        <span>Proxy Groups</span>
        <span class="rounded-full px-2 py-0.5 text-[10px] {activeViewTab === 'groups' ? 'bg-primary/10 text-primary font-semibold' : 'bg-surface-3 text-text-muted'}">{groupSummaries.length}</span>
      </button>

      <button
        type="button"
        onclick={() => (activeViewTab = 'fitness')}
        class="flex items-center gap-2 rounded-lg px-3.5 py-2 text-xs font-medium transition-all cursor-pointer {activeViewTab === 'fitness' ? 'bg-surface text-text-main shadow-sm border border-border font-semibold' : 'bg-surface-2 text-text-muted hover:text-text-main border border-transparent'}"
      >
        <span class="material-symbols-outlined text-[16px] {activeViewTab === 'fitness' ? 'text-primary' : ''}">monitoring</span>
        <span>Fitness & Canary</span>
        {#if cooldownCount > 0}
          <span class="rounded-full bg-red-500/15 text-red-500 border border-red-500/30 px-2 py-0.5 text-[10px] font-semibold animate-pulse">{cooldownCount} cooldown</span>
        {:else}
          <span class="rounded-full bg-emerald-500/15 text-emerald-500 px-2 py-0.5 text-[10px] font-medium">Healthy</span>
        {/if}
      </button>
    </div>

    {#if activeViewTab === 'pools'}
    <Card>
      <!-- Filter & Sort Controls & Quick Cleanup -->
      <div class="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <div class="flex items-center gap-2 flex-wrap">
          <div class="relative">
            <span class="material-symbols-outlined pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[16px] text-text-muted">search</span>
            <input
              type="text"
              bind:value={searchQuery}
              placeholder="Search name, URL, group…"
              aria-label="Search proxy pools"
              class="h-8 w-48 rounded-lg border border-border-subtle bg-surface-2 pl-7 pr-2 text-xs text-text-main outline-none transition-colors placeholder:text-text-muted focus:border-primary/50"
            />
          </div>

          <div class="flex items-center gap-1.5">
            <span class="text-xs text-text-muted shrink-0">Filter:</span>
            <CountedSelect
              value={statusFilter}
              options={statusOptions}
              ariaLabel="Filter proxy pools by status"
              onChange={(next) => (statusFilter = next as PoolStatusFilter)}
            />
          </div>

          <div class="flex items-center gap-1.5">
            <span class="text-xs text-text-muted shrink-0">Group:</span>
            <CountedSelect
              value={groupFilter}
              options={[{ value: 'all', label: 'All Groups', count: proxyPools.length }, ...groupCounts.map((g) => ({ value: g.name, label: g.name, count: g.count }))]}
              ariaLabel="Filter proxy pools by group"
              onChange={(next) => (groupFilter = next)}
            />
          </div>

          <div class="flex items-center gap-1.5">
            <span class="text-xs text-text-muted shrink-0">Type:</span>
            <CountedSelect
              value={typeFilter}
              options={typeOptions}
              ariaLabel="Filter proxy pools by type"
              onChange={(next) => (typeFilter = next)}
            />
          </div>
        </div>

        <!-- Sort & Quick Cleanup -->
        <div class="flex items-center gap-2 flex-wrap">
          <div class="flex items-center gap-1.5">
            <span class="text-xs text-text-muted shrink-0">Sort:</span>
            <select
              bind:value={sortOption}
              class="h-8 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3 cursor-pointer"
              aria-label="Sort proxy pools"
            >
              <option value="default">Default</option>
              <option value="fastest">Fastest (Latency asc)</option>
              <option value="recently_tested">Recently Tested</option>
              <option value="name">Name (A-Z)</option>
            </select>
          </div>

          <Button
            size="sm"
            variant="secondary"
            onclick={handleDisableFailed}
            disabled={failedCount === 0 || bulkBusy || healthChecking}
          >
            <span class="material-symbols-outlined text-[16px]">pause_circle</span>
            Disable Failed
          </Button>

          <Button
            size="sm"
            variant="danger"
            onclick={handleDeleteFailed}
            disabled={failedCount === 0 || bulkBusy || healthChecking}
          >
            <span class="material-symbols-outlined text-[16px]">delete_sweep</span>
            Delete Failed
          </Button>
        </div>
      </div>

      <div class="mb-4 flex flex-wrap items-center justify-between gap-2 border-b border-black/[0.06] pb-3 dark:border-white/[0.06]">
        {#if displayedPools.length > 0}
          <label class="flex items-center gap-1.5 text-xs text-text-muted cursor-pointer">
            <input
              type="checkbox"
              checked={allSelected}
              onchange={toggleSelectAll}
              class="size-4 rounded border-black/20 dark:border-white/20 cursor-pointer"
            />
            {allSelected ? 'Unselect all' : 'Select all'}
          </label>
        {:else}
          <div></div>
        {/if}
        <div class="flex items-center gap-2 flex-wrap">
          <Badge>Total: {proxyPools.length}</Badge>
          <Badge variant="success">Active: {activeCount}</Badge>
          {#if cooldownCount > 0}
            <Badge variant="error" dot>Cooldown: {cooldownCount}</Badge>
          {/if}
          {#if failedCount > 0}
            <Badge variant="error">Failed: {failedCount}</Badge>
          {/if}
        </div>
      </div>

      {#if selectedIds.length > 0 || healthChecking}
        <div
          class="mb-4 flex flex-wrap items-center gap-2 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2"
        >
          <span class="material-symbols-outlined text-[18px] text-primary">checklist</span>
          <span class="text-xs font-medium text-primary">
            {selectedIds.length > 0 ? `${selectedIds.length} selected` : 'All pools'}
          </span>
          <div class="ml-auto flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              onclick={() => handleHealthCheck(false)}
              disabled={healthChecking || bulkBusy || proxyPools.length === 0}
            >
              <span
                class="material-symbols-outlined text-[18px]"
                style={healthChecking ? 'animation: spin 1s linear infinite' : undefined}
              >
                {healthChecking ? 'progress_activity' : 'health_and_safety'}
              </span>
              {healthChecking
                ? `Testing ${healthProgress.current}/${healthProgress.total}`
                : selectedIds.length > 0
                  ? 'Test Selected'
                  : 'Test All'}
            </Button>
            {#if selectedIds.length > 0}
              <Button
                size="sm"
                variant="secondary"
                onclick={() => bulkSetActive(true)}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">toggle_on</span>
                Activate
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onclick={() => bulkSetActive(false)}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">toggle_off</span>
                Deactivate
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onclick={() => (groupPicker = { mode: 'bulk' })}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">folder</span>
                Set Group
              </Button>
              <Button
                size="sm"
                variant="danger"
                onclick={bulkDelete}
                disabled={bulkBusy || healthChecking}
              >
                <span class="material-symbols-outlined text-[18px]">delete</span>
                Delete
              </Button>
              <Button size="sm" variant="ghost" onclick={clearSelection} disabled={bulkBusy || healthChecking}>
                Clear
              </Button>
            {/if}
          </div>
        </div>
      {/if}

      {#if proxyPools.length === 0}
        <div class="text-center py-10">
          <p class="text-text-main font-medium mb-1">No proxy pool entries yet</p>
          <p class="text-sm text-text-muted mb-4">
            Create a proxy pool entry, then assign it to connections.
          </p>
          <Button onclick={() => openCreateModal('single')}>
            <span class="material-symbols-outlined text-[18px]">add</span>
            Add Proxy Pool
          </Button>
        </div>
      {:else if displayedPools.length === 0}
        <div class="text-center py-10">
          <p class="text-text-main font-medium mb-1">No proxy pools match current filter</p>
          <p class="text-sm text-text-muted mb-4">
            Try selecting a different filter option or reset filters.
          </p>
          <Button variant="secondary" onclick={() => { statusFilter = 'all'; groupFilter = 'all'; typeFilter = 'all'; searchQuery = ''; }}>
            Reset Filter
          </Button>
        </div>
      {:else}
        <div class="flex flex-col divide-y divide-black/[0.04] dark:divide-white/[0.05]">
          {#each paginatedPools as pool (pool.id)}
            <PoolRow
              {pool}
              selected={selectedIds.includes(pool.id)}
              testing={testingId === pool.id}
              {toggleSelect}
              {handleToggleActive}
              {handleTest}
              {openEditModal}
              {handleDelete}
              onSetGroup={(p) => (groupPicker = { mode: 'single', pool: p })}
            />
          {/each}
        </div>

        {#if totalPages > 1 || displayedPools.length > 10}
          <div class="flex flex-col sm:flex-row items-center justify-between gap-3 pt-4 mt-2 border-t border-black/[0.06] dark:border-white/[0.08] text-xs text-text-muted">
            <div class="flex items-center gap-2">
              <span>Showing {(currentPage - 1) * pageSize + 1}–{Math.min(currentPage * pageSize, displayedPools.length)} of {displayedPools.length} pools</span>
              <span class="opacity-40">|</span>
              <div class="flex items-center gap-1.5">
                <span>Per page:</span>
                <select
                  bind:value={pageSize}
                  class="bg-transparent border border-black/10 dark:border-white/10 rounded px-1.5 py-0.5 text-xs text-text-main focus:outline-none focus:border-primary cursor-pointer"
                >
                  <option value={10}>10</option>
                  <option value={25}>25</option>
                  <option value={50}>50</option>
                  <option value={100}>100</option>
                </select>
              </div>
            </div>

            <div class="flex items-center gap-1">
              <Button
                variant="secondary"
                size="sm"
                disabled={currentPage <= 1}
                onclick={() => (currentPage = Math.max(1, currentPage - 1))}
              >
                Previous
              </Button>
              <div class="flex items-center px-2 font-medium text-text-main">
                {currentPage} / {totalPages}
              </div>
              <Button
                variant="secondary"
                size="sm"
                disabled={currentPage >= totalPages}
                onclick={() => (currentPage = Math.min(totalPages, currentPage + 1))}
              >
                Next
              </Button>
            </div>
          </div>
        {/if}
      {/if}
    </Card>
    {:else if activeViewTab === 'groups'}
      <GroupsTab
        {groupSummaries}
        totalPools={proxyPools.length}
        {healthChecking}
        {testingId}
        {openCreateModal}
        {testGroup}
        {handleTest}
        onManage={(name) => (membersModalGroup = name)}
        onViewGroup={(name) => {
          groupFilter = name
          activeViewTab = 'pools'
        }}
      />
    {:else if activeViewTab === 'fitness'}
      <FitnessTab
        pools={proxyPools}
        {counts}
        {failedCount}
        {cooldownCount}
        {filteredFitness}
        {filteredCooldown}
        {typeOptions}
        bind:fitnessSearch
        bind:fitnessType
        {recovering}
        {clearingFitness}
        {healthChecking}
        {testingId}
        {handleRecover}
        {handleClearAllFitness}
        {handleHealthCheck}
        {handleTest}
      />
    {/if}

    <Modal
      isOpen={showFormModal}
      title={editingPool ? 'Edit Proxy Pool' : 'Add Proxy Pools'}
      onClose={closeFormModal}
    >
      <div class="flex flex-col gap-4">
        {#if editingPool}
          {@render singleFields()}
          <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <Button
              fullWidth
              onclick={handleSave}
              disabled={!formData.name.trim() || !formData.proxyUrl.trim() || saving}
            >
              {saving ? 'Saving...' : 'Save'}
            </Button>
            <Button fullWidth variant="ghost" onclick={closeFormModal} disabled={saving}>
              Cancel
            </Button>
          </div>
        {:else}
          <!-- Adding one pool by hand and adding a pasted list were two modals
               behind two buttons (issue #224). They are one task, so they are
               one dialog with two tabs: the Batch Import button opens this same
               dialog on the Bulk Add tab rather than a second window. -->
          <div class="flex gap-1 rounded-lg border border-border/50 bg-surface-2 p-1">
            {#each [{ value: 'single', label: 'Single' }, { value: 'bulk', label: 'Bulk Add' }] as tab (tab.value)}
              <button
                type="button"
                role="tab"
                aria-selected={formTab === tab.value}
                onclick={() => (formTab = tab.value as 'single' | 'bulk')}
                class="flex-1 rounded-md px-3 py-1.5 text-xs font-medium transition-colors {formTab ===
                tab.value
                  ? 'bg-brand-500 text-white shadow-sm'
                  : 'text-text-muted hover:text-text-main'}"
              >
                {tab.label}
              </button>
            {/each}
          </div>

          {#if formTab === 'single'}
            {@render singleFields()}
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <Button
                fullWidth
                onclick={handleSave}
                disabled={!formData.name.trim() || !formData.proxyUrl.trim() || saving}
              >
                {saving ? 'Saving...' : 'Save'}
              </Button>
              <Button fullWidth variant="ghost" onclick={closeFormModal} disabled={saving}>
                Cancel
              </Button>
            </div>
          {:else}
            <div>
              <label for="bulk-group" class="text-sm font-medium text-text-main mb-1 block">
                Import into Group (optional)
              </label>
              <div class="flex flex-col gap-2 sm:flex-row">
                <select
                  id="bulk-group"
                  bind:value={batchGroupChoice}
                  class="h-9 rounded-lg border border-border-subtle bg-surface-2 px-2 text-sm text-text-main outline-none transition-colors hover:bg-surface-3 cursor-pointer"
                >
                  <option value="">No group</option>
                  {#each availableGroups as grp}
                    <option value={grp}>{grp}</option>
                  {/each}
                  <option value="__new__">New group…</option>
                </select>
                {#if batchGroupChoice === '__new__'}
                  <input
                    type="text"
                    bind:value={batchGroupNew}
                    placeholder="New group name"
                    aria-label="New group name"
                    class="h-9 min-w-0 flex-1 rounded-lg border border-border-subtle bg-surface-2 px-2 text-sm text-text-main outline-none placeholder:text-text-muted focus:border-primary/50"
                  />
                {/if}
              </div>
            </div>

            <div>
              <label for="bulk-proxies" class="text-sm font-medium text-text-main mb-1 block">
                Paste Proxy List (One per line)
              </label>
              <textarea
                id="bulk-proxies"
                bind:value={batchImportText}
                placeholder={'http://user:pass@127.0.0.1:7897\n127.0.0.1:7897:user:pass\nuser:pass@127.0.0.1:7897\n127.0.0.1:7897'}
                class="w-full min-h-[180px] py-2 px-3 text-sm text-text-main bg-surface-2 border border-transparent rounded-brand focus:ring-1 focus:ring-primary/30 focus:border-primary/50 focus:outline-none transition-all font-mono"
              ></textarea>
              <p class="text-xs text-text-muted mt-1">
                Supported formats: protocol://user:pass@host:port, host:port:user:pass,
                user:pass@host:port, host:port. Invalid lines are reported and left in the
                box — valid lines still import.
              </p>
            </div>

            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <Button fullWidth onclick={handleBatchImport} disabled={!batchImportText.trim() || importing}>
                {importing ? 'Importing...' : 'Import'}
              </Button>
              <Button fullWidth variant="ghost" onclick={closeFormModal} disabled={importing}>
                Cancel
              </Button>
            </div>
          {/if}
        {/if}
      </div>
    </Modal>

    <GroupMembersModal
      isOpen={!!membersModalGroup}
      groupName={membersModalGroup || ''}
      pools={proxyPools}
      busy={membersSaving}
      onSave={saveGroupMembers}
      onClose={() => (membersModalGroup = null)}
    />

    <GroupPickerModal
      isOpen={!!groupPicker}
      title={groupPicker?.mode === 'single'
        ? `Set group — ${groupPicker.pool.name}`
        : `Set group for ${selectedIds.length} selected pool${selectedIds.length === 1 ? '' : 's'}`}
      groups={groupCounts}
      showUngroup={groupPicker?.mode === 'bulk' ||
        (groupPicker?.mode === 'single' && !!(groupPicker.pool.group || '').trim())}
      busy={groupAssigning}
      onPick={handleGroupPick}
      onClose={() => (groupPicker = null)}
    />

    <Modal
      isOpen={!!confirmState}
      title={confirmState?.title || 'Confirm'}
      size="sm"
      onClose={() => (confirmState = null)}
    >
      {#snippet footer()}
        <Button variant="ghost" onclick={() => (confirmState = null)}>Cancel</Button>
        <Button variant="danger" onclick={() => confirmState?.onConfirm()}>
          {confirmState?.confirmText || 'Confirm'}
        </Button>
      {/snippet}
      <p class="text-text-muted whitespace-pre-wrap">{confirmState?.message}</p>
    </Modal>
  </div>
{/if}
