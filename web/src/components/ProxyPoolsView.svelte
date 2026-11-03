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
  import { notifications } from '../lib/notifications'
  import { parseProxyLine } from '../lib/proxy-import'
  import {
    filterProxyPools,
    getLatencyBadge,
    getStatusVariant,
    hasMeasuredLatency,
    isFailedStatus,
    poolStatusCounts,
    sortProxyPools,
    type PoolSortOption,
    type PoolStatusFilter
  } from './proxypools/helpers'

  interface BulkOutcome {
    ok: number
    blocked: number
    failed: number
  }

  function formatDateTime(value: string | null | undefined): string {
    if (!value) return 'Never'
    const date = new Date(value)
    if (Number.isNaN(date.getTime())) return 'Never'
    return date.toLocaleString()
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
  let showVercelModal = $state(false)
  let showCloudflareModal = $state(false)
  let showDenoModal = $state(false)
  let showNetlifyModal = $state(false)
  let showRelayMenu = $state(false)
  let editingPool = $state<ProxyPool | null>(null)
  let formData = $state<PoolForm>(normalizeFormData())
  let batchImportText = $state('')
  let vercelForm = $state({ vercelToken: '', projectName: 'vercel-relay' })
  let cloudflareForm = $state({ accountId: '', apiToken: '', projectName: 'cloudflare-relay' })
  let denoForm = $state({ denoToken: '', orgDomain: '', projectName: '' })
  let netlifyForm = $state({ netlifyToken: '', projectName: 'netlify-relay' })

  interface CloudflareBulkResult {
    lineNumber: number
    label: string
    accountId: string
    projectName: string
    status: 'pending' | 'running' | 'success' | 'failed'
    deployUrl?: string
    error?: string
  }

  let showCloudflareBulkModal = $state(false)
  let cloudflareBulkPoolName = $state('cloudflare-relay')
  let cloudflareBulkText = $state('')
  let cloudflareBulkResults = $state<CloudflareBulkResult[]>([])
  let cloudflareBulkProgress = $state({
    total: 0,
    completed: 0,
    success: 0,
    failed: 0,
    currentBatch: 0,
    totalBatches: 0
  })
  let bulkCloudflareDeploying = $state(false)

  interface VercelBulkResult {
    lineNumber: number
    label: string
    tokenMasked: string
    projectName: string
    status: 'pending' | 'running' | 'success' | 'failed'
    deployUrl?: string
    error?: string
  }

  let showVercelBulkModal = $state(false)
  let vercelBulkPoolName = $state('vercel-relay')
  let vercelBulkText = $state('')
  let vercelBulkResults = $state<VercelBulkResult[]>([])
  let vercelBulkProgress = $state({
    total: 0,
    completed: 0,
    success: 0,
    failed: 0,
    currentBatch: 0,
    totalBatches: 0
  })
  let bulkVercelDeploying = $state(false)
  let saving = $state(false)
  let importing = $state(false)
  let deploying = $state(false)
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
  let relayMenuRef = $state<HTMLDivElement | null>(null)

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

  // Close the Deploy Relay menu on outside click (upstream parity).
  $effect(() => {
    if (!showRelayMenu || typeof document === 'undefined') return
    const onDown = (e: MouseEvent) => {
      if (relayMenuRef && !relayMenuRef.contains(e.target as Node)) showRelayMenu = false
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
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

  interface GroupSummary {
    name: string
    pools: ProxyPool[]
    totalCount: number
    activeCount: number
    inCooldownCount: number
    avgFitness: number
    avgLatency: number
    strictCount: number
  }

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
    const _ = statusFilter + sortOption + groupFilter
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

  function openCreateModal(tab: 'single' | 'bulk' = 'single') {
    resetForm()
    batchImportText = ''
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
    formTab = 'single'
  }

  function openVercelModal() {
    vercelForm = { vercelToken: '', projectName: 'vercel-relay' }
    showVercelModal = true
  }

  function closeVercelModal() {
    if (deploying) return
    showVercelModal = false
  }

  function openCloudflareModal() {
    cloudflareForm = { accountId: '', apiToken: '', projectName: 'cloudflare-relay' }
    showCloudflareModal = true
  }

  function closeCloudflareModal() {
    if (deploying) return
    showCloudflareModal = false
  }

  function openCloudflareBulkModal() {
    cloudflareBulkPoolName = 'cloudflare-relay'
    cloudflareBulkText = ''
    cloudflareBulkResults = []
    cloudflareBulkProgress = {
      total: 0,
      completed: 0,
      success: 0,
      failed: 0,
      currentBatch: 0,
      totalBatches: 0
    }
    showCloudflareBulkModal = true
  }

  function closeCloudflareBulkModal() {
    if (bulkCloudflareDeploying) return
    showCloudflareBulkModal = false
  }

  function openVercelBulkModal() {
    vercelBulkPoolName = 'vercel-relay'
    vercelBulkText = ''
    vercelBulkResults = []
    vercelBulkProgress = {
      total: 0,
      completed: 0,
      success: 0,
      failed: 0,
      currentBatch: 0,
      totalBatches: 0
    }
    showVercelBulkModal = true
  }

  function closeVercelBulkModal() {
    if (bulkVercelDeploying) return
    showVercelBulkModal = false
  }

  function openDenoModal() {
    denoForm = { denoToken: '', orgDomain: '', projectName: '' }
    showDenoModal = true
  }

  function closeDenoModal() {
    if (deploying) return
    showDenoModal = false
  }

  function openNetlifyModal() {
    netlifyForm = { netlifyToken: '', projectName: 'netlify-relay' }
    showNetlifyModal = true
  }

  function closeNetlifyModal() {
    if (deploying) return
    showNetlifyModal = false
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

    const parsedEntries: { proxyUrl: string; name: string }[] = []
    const invalidLines: string[] = []

    lines.forEach((line, index) => {
      try {
        const parsed = parseProxyLine(line)
        if (parsed) parsedEntries.push(parsed)
      } catch (err) {
        invalidLines.push(`Line ${index + 1}: ${err instanceof Error ? err.message : 'Invalid'}`)
      }
    })

    if (invalidLines.length > 0) {
      notifications.error(`Invalid proxy format:\n${invalidLines.join('\n')}`)
      return
    }

    importing = true
    try {
      const existingKeys = new Set(
        proxyPools.map((pool) => `${(pool.proxyUrl || '').trim()}|||${(pool.noProxy || '').trim()}`)
      )

      let created = 0
      let skipped = 0
      let failed = 0

      for (const entry of parsedEntries) {
        const dedupeKey = `${entry.proxyUrl}|||`
        if (existingKeys.has(dedupeKey)) {
          skipped += 1
          continue
        }
        try {
          await api.createProxyPool({
            name: entry.name,
            proxyUrl: entry.proxyUrl,
            noProxy: '',
            isActive: true,
          })
          created += 1
          existingKeys.add(dedupeKey)
        } catch {
          failed += 1
        }
      }

      await fetchProxyPools()
      // The import ran to completion, so the dialog can close now. closeFormModal
      // refuses while `importing` is still true, which is why this clears the
      // draft directly instead of going through it.
      showFormModal = false
      batchImportText = ''
      formTab = 'single'
      notifications.success(
        `Batch import completed: Created ${created}, Skipped ${skipped}, Failed ${failed}`
      )
    } catch (err) {
      console.log('Error batch importing proxies:', err)
      notifications.error('Batch import failed')
    } finally {
      importing = false
    }
  }

  async function handleVercelDeploy() {
    if (!vercelForm.vercelToken.trim()) return
    deploying = true
    try {
      const data = await api.deployVercelRelay({
        vercelToken: vercelForm.vercelToken.trim(),
        projectName: vercelForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeVercelModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Vercel relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleCloudflareDeploy() {
    if (!cloudflareForm.accountId.trim() || !cloudflareForm.apiToken.trim()) return
    deploying = true
    try {
      const data = await api.deployCloudflareRelay({
        accountId: cloudflareForm.accountId.trim(),
        apiToken: cloudflareForm.apiToken.trim(),
        projectName: cloudflareForm.projectName.trim() || undefined,
        name: cloudflareForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeCloudflareModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Cloudflare relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleCloudflareBulkDeploy() {
    const poolBase =
      cloudflareBulkPoolName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 32) || 'cloudflare-relay'

    // Worker name = pool base. Number only appended when the same Cloudflare
    // account appears more than once (script name is unique per account, NOT
    // globally), so distinct accounts keep the clean identical name.
    const accountCounts = new Map<string, number>()
    const entries = cloudflareBulkText
      .split(/\r?\n/)
      .map((line, index) => ({ line: line.trim(), lineNumber: index + 1 }))
      .filter(({ line }) => line.length > 0)
      .map(({ line, lineNumber }) => {
        const [label = '', accountId = '', apiToken = '', ...extra] = line
          .split('|')
          .map((part) => part.trim())
        const seen = (accountCounts.get(accountId) ?? 0) + 1
        accountCounts.set(accountId, seen)
        return {
          lineNumber,
          label,
          accountId,
          apiToken,
          projectName: seen === 1 ? poolBase : `${poolBase}-${seen}`,
          valid: Boolean(label && accountId && apiToken && !extra.length)
        }
      })

    if (!entries.length) return

    const maskAccountId = (id: string) =>
      id.length > 8 ? `${id.slice(0, 4)}…${id.slice(-4)}` : '••••'

    cloudflareBulkResults = entries.map((entry) => ({
      lineNumber: entry.lineNumber,
      label: entry.label || `Line ${entry.lineNumber}`,
      accountId: entry.accountId ? maskAccountId(entry.accountId) : '—',
      projectName: entry.projectName,
      status: entry.valid ? 'pending' : 'failed',
      error: entry.valid ? undefined : 'Format harus name/email|accountID|apiToken'
    }))

    bulkCloudflareDeploying = true
    const validEntries = entries
      .map((entry, index) => ({ ...entry, originalIndex: index }))
      .filter((entry) => entry.valid)
    const BATCH_SIZE = 10
    const totalBatches = Math.ceil(validEntries.length / BATCH_SIZE) || 1

    let successCount = 0
    let failedCount = entries.filter((entry) => !entry.valid).length

    cloudflareBulkProgress = {
      total: entries.length,
      completed: failedCount,
      success: 0,
      failed: failedCount,
      currentBatch: validEntries.length > 0 ? 1 : 0,
      totalBatches
    }

    try {
      for (let batchIndex = 0; batchIndex < totalBatches; batchIndex++) {
        const batch = validEntries.slice(batchIndex * BATCH_SIZE, (batchIndex + 1) * BATCH_SIZE)
        if (!batch.length) continue

        cloudflareBulkProgress.currentBatch = batchIndex + 1

        const runningIndices = new Set(batch.map((item) => item.originalIndex))
        cloudflareBulkResults = cloudflareBulkResults.map((result, idx) =>
          runningIndices.has(idx) ? { ...result, status: 'running' } : result
        )

        await Promise.all(
          batch.map(async (entry) => {
            const index = entry.originalIndex
            try {
              const res = await api.deployCloudflareRelay({
                accountId: entry.accountId,
                apiToken: entry.apiToken,
                projectName: entry.projectName,
                name: entry.projectName,
                email: entry.label
              })

              successCount += 1
              cloudflareBulkResults = cloudflareBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'success', deployUrl: res.deployUrl || res.proxyUrl || 'Deployed' }
                  : result
              )
              cloudflareBulkProgress.completed += 1
              cloudflareBulkProgress.success += 1
            } catch (error) {
              failedCount += 1
              cloudflareBulkResults = cloudflareBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'failed', error: error instanceof Error ? error.message : 'Deploy failed' }
                  : result
              )
              cloudflareBulkProgress.completed += 1
              cloudflareBulkProgress.failed += 1
            }
          })
        )
      }

      await fetchProxyPools()
      if (failedCount > 0) {
        notifications.error(`${successCount} deployed, ${failedCount} failed`)
      } else {
        notifications.success(`${successCount} Cloudflare relays deployed`)
      }
    } catch (error) {
      notifications.error(error instanceof Error ? error.message : 'Bulk deploy failed')
    } finally {
      bulkCloudflareDeploying = false
    }
  }

  async function handleVercelBulkDeploy() {
    const poolBase =
      vercelBulkPoolName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 32) || 'vercel-relay'

    const tokenCounts = new Map<string, number>()
    const entries = vercelBulkText
      .split(/\r?\n/)
      .map((line, index) => ({ line: line.trim(), lineNumber: index + 1 }))
      .filter(({ line }) => line.length > 0)
      .map(({ line, lineNumber }, index) => {
        let label = `Token ${index + 1}`
        let token = line
        if (line.includes('|')) {
          const parts = line.split('|').map((p) => p.trim())
          label = parts[0] || `Token ${index + 1}`
          token = parts[1] || ''
        }
        const seen = (tokenCounts.get(token) ?? 0) + 1
        tokenCounts.set(token, seen)
        return {
          lineNumber,
          label,
          token,
          projectName: seen === 1 ? poolBase : `${poolBase}-${seen}`,
          valid: Boolean(token.length > 0)
        }
      })

    if (!entries.length) return

    const maskToken = (tok: string) =>
      tok.length > 8 ? `${tok.slice(0, 4)}…${tok.slice(-4)}` : '••••'

    vercelBulkResults = entries.map((entry) => ({
      lineNumber: entry.lineNumber,
      label: entry.label,
      tokenMasked: entry.token ? maskToken(entry.token) : '—',
      projectName: entry.projectName,
      status: entry.valid ? 'pending' : 'failed',
      error: entry.valid ? undefined : 'Token tidak boleh kosong'
    }))

    bulkVercelDeploying = true
    const validEntries = entries
      .map((entry, index) => ({ ...entry, originalIndex: index }))
      .filter((entry) => entry.valid)
    const BATCH_SIZE = 10
    const totalBatches = Math.ceil(validEntries.length / BATCH_SIZE) || 1

    let successCount = 0
    let failedCount = entries.filter((entry) => !entry.valid).length

    vercelBulkProgress = {
      total: entries.length,
      completed: failedCount,
      success: 0,
      failed: failedCount,
      currentBatch: validEntries.length > 0 ? 1 : 0,
      totalBatches
    }

    try {
      for (let batchIndex = 0; batchIndex < totalBatches; batchIndex++) {
        const batch = validEntries.slice(batchIndex * BATCH_SIZE, (batchIndex + 1) * BATCH_SIZE)
        if (!batch.length) continue

        vercelBulkProgress.currentBatch = batchIndex + 1

        const runningIndices = new Set(batch.map((item) => item.originalIndex))
        vercelBulkResults = vercelBulkResults.map((result, idx) =>
          runningIndices.has(idx) ? { ...result, status: 'running' } : result
        )

        await Promise.all(
          batch.map(async (entry) => {
            const index = entry.originalIndex
            try {
              const res = await api.deployVercelRelay({
                vercelToken: entry.token,
                projectName: entry.projectName
              })

              successCount += 1
              vercelBulkResults = vercelBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'success', deployUrl: res.deployUrl || res.proxyUrl || 'Deployed' }
                  : result
              )
              vercelBulkProgress.completed += 1
              vercelBulkProgress.success += 1
            } catch (error) {
              failedCount += 1
              vercelBulkResults = vercelBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'failed', error: error instanceof Error ? error.message : 'Deploy failed' }
                  : result
              )
              vercelBulkProgress.completed += 1
              vercelBulkProgress.failed += 1
            }
          })
        )
      }

      await fetchProxyPools()
      if (failedCount > 0) {
        notifications.error(`${successCount} deployed, ${failedCount} failed`)
      } else {
        notifications.success(`${successCount} Vercel relays deployed`)
      }
    } catch (error) {
      notifications.error(error instanceof Error ? error.message : 'Bulk deploy failed')
    } finally {
      bulkVercelDeploying = false
    }
  }

  async function handleDenoDeploy() {
    if (!denoForm.denoToken.trim() || !denoForm.orgDomain.trim()) return
    deploying = true
    try {
      const data = await api.deployDenoRelay({
        denoToken: denoForm.denoToken.trim(),
        orgDomain: denoForm.orgDomain.trim(),
        projectName: denoForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeDenoModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Deno relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleNetlifyDeploy() {
    if (!netlifyForm.netlifyToken.trim()) return
    deploying = true
    try {
      const data = await api.deployNetlifyRelay({
        netlifyToken: netlifyForm.netlifyToken.trim(),
        projectName: netlifyForm.projectName.trim() || undefined,
      })
      await fetchProxyPools()
      closeNetlifyModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Netlify relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }
</script>

<!-- The one-pool form is shared by the Single tab and the edit dialog, so the
     fields live in one snippet rather than being written twice. -->
{#snippet singleFields()}
  <Input label="Name" bind:value={formData.name} placeholder="Office Proxy" />
  <Input
    label="Group / Tag (optional)"
    bind:value={formData.group}
    placeholder="e.g. premium, dedicated-asia"
    hint="Assign to a group for provider smart load balancing and failover"
  />
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
    <div class="h-20 rounded-[14px] bg-surface border border-border-subtle animate-pulse"></div>
    <div class="h-64 rounded-[14px] bg-surface border border-border-subtle animate-pulse"></div>
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

        <div class="relative" bind:this={relayMenuRef}>
          <Button
            size="sm"
            variant="secondary"
            onclick={() => (showRelayMenu = !showRelayMenu)}
          >
            <span class="material-symbols-outlined text-[18px]">rocket_launch</span>
            Deploy Relay
            <span class="material-symbols-outlined ml-1 text-[18px]">
              {showRelayMenu ? 'expand_less' : 'expand_more'}
            </span>
          </Button>

          {#if showRelayMenu}
            <div
              class="absolute left-0 top-full z-50 mt-1 w-48 rounded-xl border border-black/10 bg-white p-1 shadow-xl dark:border-white/10 dark:bg-zinc-900 sm:left-auto sm:right-0"
            >
              <button
                type="button"
                onclick={() => {
                  openCloudflareModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-orange-500">cloud</span>
                Cloudflare Relay
              </button>
              <button
                type="button"
                onclick={() => {
                  openCloudflareBulkModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-amber-500">playlist_add</span>
                CF Bulk
              </button>
              <button
                type="button"
                onclick={() => {
                  openVercelModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-blue-500">cloud_upload</span>
                Vercel Relay
              </button>
              <button
                type="button"
                onclick={() => {
                  openVercelBulkModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-sky-500">playlist_add</span>
                Vercel Bulk
              </button>
              <button
                type="button"
                onclick={() => {
                  openDenoModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-green-500">terminal</span>
                Deno Relay
              </button>
              <button
                type="button"
                onclick={() => {
                  openNetlifyModal()
                  showRelayMenu = false
                }}
                class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-black/5 dark:hover:bg-white/5 cursor-pointer"
              >
                <span class="material-symbols-outlined text-[20px] text-teal-500">deployed_code</span>
                Netlify Relay
              </button>
            </div>
          {/if}
        </div>

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
            <select
              bind:value={groupFilter}
              class="h-8 rounded-lg border border-border-subtle bg-surface-2 px-2 text-xs text-text-main outline-none transition-colors hover:bg-surface-3 cursor-pointer"
              aria-label="Filter proxy pools by group"
            >
              <option value="all">All Groups ({proxyPools.length})</option>
              {#each availableGroups as grp}
                <option value={grp}>{grp} ({proxyPools.filter((p) => (p.group || '').trim() === grp).length})</option>
              {/each}
            </select>
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
                ? `Checking ${healthProgress.current}/${healthProgress.total}`
                : 'Health Check'}
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
          <Button variant="secondary" onclick={() => { statusFilter = 'all'; groupFilter = 'all'; }}>
            Reset Filter
          </Button>
        </div>
      {:else}
        <div class="flex flex-col divide-y divide-black/[0.04] dark:divide-white/[0.05]">
          {#each paginatedPools as pool (pool.id)}
            {@const latencyBadge = hasMeasuredLatency(pool) ? getLatencyBadge(pool.latency) : null}
            <div class="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between">
              <div class="flex items-start gap-3 min-w-0 flex-1">
                <input
                  type="checkbox"
                  checked={selectedIds.includes(pool.id)}
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
                    {#if pool.type === 'vercel'}
                      <Badge size="sm">vercel relay</Badge>
                    {/if}
                    {#if pool.type === 'cloudflare'}
                      <Badge size="sm">cloudflare relay</Badge>
                    {/if}
                    {#if pool.type === 'deno'}
                      <Badge size="sm">deno relay</Badge>
                    {/if}
                    {#if pool.type === 'netlify'}
                      <Badge size="sm">netlify relay</Badge>
                    {/if}
                    <Badge size="sm">
                      {pool.boundConnectionCount || 0} bound
                    </Badge>
                  </div>
                  <p class="text-xs text-text-muted truncate mt-1">{pool.proxyUrl}</p>
                  {#if pool.noProxy}
                    <p class="text-xs text-text-muted truncate">No proxy: {pool.noProxy}</p>
                  {/if}
                  <p class="text-[11px] text-text-muted mt-1">
                    Last tested: {formatDateTime(pool.lastTestedAt)}
                    {pool.lastError ? ` · ${pool.lastError}` : ''}
                  </p>

                  {#if pool.exitIpv4 || pool.exitIpv6 || pool.exitCountry || pool.exitIsp}
                    <div class="flex flex-wrap items-center gap-1.5 mt-2 text-[11px]">
                      {#if pool.exitIpv4}
                        <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded font-mono font-medium text-emerald-700 bg-emerald-500/10 dark:text-emerald-300 dark:bg-emerald-500/15 border border-emerald-500/20">
                          <span class="opacity-60 text-[9px] uppercase font-sans font-bold">IPv4</span> {pool.exitIpv4}
                        </span>
                      {/if}
                      {#if pool.exitIpv6}
                        <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded font-mono font-medium text-cyan-700 bg-cyan-500/10 dark:text-cyan-300 dark:bg-cyan-500/15 border border-cyan-500/20">
                          <span class="opacity-60 text-[9px] uppercase font-sans font-bold">IPv6</span> {pool.exitIpv6}
                        </span>
                      {/if}
                      {#if pool.exitCountry || pool.exitCity}
                        <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded font-medium text-text-main bg-black/[0.04] dark:bg-white/[0.06] border border-black/[0.06] dark:border-white/[0.08]">
                          <span class="text-[12px]">📍</span> {[pool.exitCity, pool.exitCountry].filter(Boolean).join(', ')}
                        </span>
                      {/if}
                      {#if pool.exitIsp || pool.exitOrg || pool.exitAsn}
                        <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded font-medium text-text-muted bg-black/[0.04] dark:bg-white/[0.06] border border-black/[0.06] dark:border-white/[0.08]">
                          <span class="text-[12px]">🏢</span> {[pool.exitAsn, pool.exitIsp || pool.exitOrg].filter(Boolean).join(' · ')}
                        </span>
                      {/if}
                    </div>
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
                  disabled={testingId === pool.id}
                >
                  <span
                    class="material-symbols-outlined text-[18px]"
                    style={testingId === pool.id ? 'animation: spin 1s linear infinite' : undefined}
                  >
                    {testingId === pool.id ? 'progress_activity' : 'science'}
                  </span>
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
                All {proxyPools.length} proxies are currently ungrouped. Group proxy nodes (e.g. "dc", "residential", "fast") to enable smart dynamic balancing and automatic failover in connection bindings.
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
                            <span class="material-symbols-outlined text-[14px] {testingId === pool.id ? 'animate-spin' : ''}">refresh</span>
                          </button>
                        </div>
                      </div>
                    {/each}
                  </div>
                </div>
              </div>

              <!-- Actions -->
              <div class="flex items-center justify-between gap-2 pt-2 border-t border-border/40">
                <Button
                  size="sm"
                  variant="secondary"
                  onclick={() => {
                    groupFilter = grp.name === 'Default' ? '' : grp.name
                    activeViewTab = 'pools'
                  }}
                >
                  <span class="material-symbols-outlined text-[16px]">filter_list</span>
                  View in Table
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
    {:else if activeViewTab === 'fitness'}
      <div class="flex flex-col gap-4">
        <!-- Telemetry Stats Cards -->
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div class="rounded-xl border border-border bg-surface p-3.5">
            <div class="text-[11px] font-medium text-text-muted">Total Monitored</div>
            <div class="text-xl font-bold text-text-main mt-1">{proxyPools.length}</div>
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
                  {#each proxyPools.filter((p) => p.inCooldown) as pool (pool.id)}
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
                          <span class="material-symbols-outlined text-[14px] {testingId === pool.id ? 'animate-spin' : ''}">refresh</span>
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
              Test All Nodes
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
                {#each fitnessLeaderboard as pool (pool.id)}
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
                        <span class="material-symbols-outlined text-[15px] {testingId === pool.id ? 'animate-spin' : ''}">refresh</span>
                      </button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
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
    {/if}

    <Modal isOpen={showVercelModal} title="Deploy Vercel Relay" onClose={closeVercelModal}>
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-blue-500/5 border border-blue-500/10 p-3 flex flex-col gap-1.5">
          <p class="text-sm text-text-main font-medium">What is Vercel Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys an edge relay function to Vercel. All AI provider requests will be forwarded
            through Vercel's edge network, masking your real IP from providers.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>
              Your IP is replaced by Vercel's dynamic edge IPs (hundreds of IPs across 20+ global
              regions)
            </li>
            <li>
              Vercel serves millions of apps — providers can't block Vercel IPs without affecting
              legitimate traffic
            </li>
            <li>Free tier: 100GB bandwidth/month, 500K edge invocations</li>
            <li>Deploy multiple relays on different accounts for more IP diversity</li>
          </ul>
        </div>
        <div>
          <Input
            label="Vercel API Token"
            type="password"
            bind:value={vercelForm.vercelToken}
            placeholder="your-vercel-api-token"
          />
          <p class="text-xs text-text-muted mt-1">
            Token is used once for deployment and not stored.
            <a
              href="https://vercel.com/account/tokens"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline"
            >
              Get token →
            </a>
          </p>
        </div>
        <Input
          label="Project Name"
          bind:value={vercelForm.projectName}
          placeholder="my-relay"
          hint="Unique name for your Vercel project. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleVercelDeploy}
            disabled={!vercelForm.vercelToken.trim() || deploying}
          >
            {deploying ? 'Deploying... (may take ~1 min)' : 'Deploy'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeVercelModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showVercelBulkModal}
      title="Vercel Bulk"
      size="3xl"
      onClose={closeVercelBulkModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-sky-500/10 border border-sky-500/20 p-3 flex flex-col gap-1">
          <p class="text-sm font-medium text-text-main">One account / token per line (Batch of 10 concurrent)</p>
          <p class="text-xs text-text-muted">
            Format: <code class="font-mono text-sky-500">name/email|vercelToken</code> atau langsung <code class="font-mono text-sky-500">vercelToken</code>. Project name otomatis memakai Pool Name + urutan batch.
          </p>
        </div>

        <Input
          label="Pool Name / Project Prefix"
          bind:value={vercelBulkPoolName}
          disabled={bulkVercelDeploying}
          placeholder="e.g. vercel-relay, vc-indo, vc-pool"
          hint="Prefix untuk nama pool dan project Vercel (contoh: vercel-relay-1, vercel-relay-2)."
        />

        <div class="flex flex-col gap-1.5">
          <label for="vercel-bulk-textarea" class="text-xs font-medium text-text-muted">Tokens List</label>
          <textarea
            id="vercel-bulk-textarea"
            bind:value={vercelBulkText}
            disabled={bulkVercelDeploying}
            placeholder={"team-a|vcp_xxxxxxxx...\nteam-b|vcp_yyyyyyyy...\nvcp_zzzzzzzz..."}
            spellcheck="false"
            rows={10}
            class="min-h-[220px] w-full resize-y rounded-lg border border-border bg-surface px-3 py-2 font-mono text-xs text-text-main outline-none focus:border-brand-500 disabled:opacity-50"
          ></textarea>
        </div>

        {#if vercelBulkProgress.total > 0}
          <div class="rounded-lg border border-border bg-surface p-3 flex flex-col gap-2">
            <div class="flex items-center justify-between text-xs font-medium">
              <span class="text-text-main flex items-center gap-1.5">
                {#if bulkVercelDeploying}
                  <span class="inline-block size-2 rounded-full bg-brand-500 animate-pulse"></span>
                {/if}
                {bulkVercelDeploying
                  ? `Deploying Batch ${vercelBulkProgress.currentBatch} of ${vercelBulkProgress.totalBatches}...`
                  : 'Deployment Complete'}
              </span>
              <span class="font-mono text-text-muted">
                {vercelBulkProgress.completed} / {vercelBulkProgress.total} ({Math.round(
                  (vercelBulkProgress.completed / vercelBulkProgress.total) * 100
                ) || 0}%)
              </span>
            </div>

            <div class="flex h-2 w-full overflow-hidden rounded-full bg-surface-2">
              <div
                class="bg-emerald-500 transition-all duration-300"
                style="width: {(vercelBulkProgress.success / vercelBulkProgress.total) * 100}%"
                title="{vercelBulkProgress.success} succeeded"
              ></div>
              <div
                class="bg-red-500 transition-all duration-300"
                style="width: {(vercelBulkProgress.failed / vercelBulkProgress.total) * 100}%"
                title="{vercelBulkProgress.failed} failed"
              ></div>
            </div>

            <div class="flex items-center justify-between text-[11px] text-text-muted pt-0.5">
              <div class="flex items-center gap-3">
                <span class="flex items-center gap-1 text-emerald-500 font-medium">
                  <span class="size-1.5 rounded-full bg-emerald-500"></span>
                  {vercelBulkProgress.success} Success
                </span>
                <span class="flex items-center gap-1 text-red-500 font-medium">
                  <span class="size-1.5 rounded-full bg-red-500"></span>
                  {vercelBulkProgress.failed} Failed
                </span>
              </div>
              <span>Concurrency: 10/batch</span>
            </div>
          </div>
        {/if}

        {#if vercelBulkResults.length > 0}
          <div class="max-h-60 overflow-y-auto rounded-lg border border-border divide-y divide-border">
            {#each vercelBulkResults as result}
              <div class="flex items-start gap-3 p-2.5 text-xs">
                <span class="w-6 shrink-0 text-text-muted font-mono">#{result.lineNumber}</span>
                <div class="min-w-0 flex-1 flex flex-col gap-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="font-medium text-text-main">{result.label}</span>
                    <Badge
                      size="sm"
                      variant={result.status === 'success'
                        ? 'success'
                        : result.status === 'failed'
                          ? 'error'
                          : result.status === 'running'
                            ? 'warning'
                            : 'neutral'}
                    >
                      {result.status}
                    </Badge>
                  </div>
                  <p class="font-mono text-text-muted text-[11px]">
                    {result.tokenMasked} · {result.projectName}
                  </p>
                  {#if result.deployUrl}
                    <p class="break-all text-emerald-500 text-[11px] font-mono">{result.deployUrl}</p>
                  {/if}
                  {#if result.error}
                    <p class="break-words text-red-500 text-[11px]">{result.error}</p>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleVercelBulkDeploy}
            disabled={!vercelBulkText.trim() || bulkVercelDeploying}
          >
            {bulkVercelDeploying
              ? `Deploying (${vercelBulkProgress.completed}/${vercelBulkProgress.total})...`
              : 'Deploy All'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeVercelBulkModal} disabled={bulkVercelDeploying}>
            Close
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showCloudflareModal}
      title="Deploy Cloudflare Relay"
      onClose={closeCloudflareModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-orange-500/5 border border-orange-500/10 p-3 flex flex-col gap-1.5">
          <p class="text-sm text-text-main font-medium">What is Cloudflare Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a Cloudflare Worker as a proxy relay. All AI provider requests will be forwarded
            through Cloudflare's global edge network.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>High performance global routing and IP masking via Cloudflare Workers</li>
            <li>Free tier: 100,000 requests per day</li>
            <li>Requires Cloudflare Account ID and a Workers API Token (Edit Workers permission)</li>
          </ul>
          <div class="mt-2 pt-2 border-t border-orange-500/10 text-xs text-text-muted">
            <p class="font-medium text-text-main mb-1">How to generate your API Token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>My Profile</b> → <b>API Tokens</b> → <b>Create Token</b></li>
              <li>Scroll down to <b>Custom Token</b> and click <b>Get started</b></li>
              <li>Under <b>Permissions</b>: Account | Workers Scripts | Edit</li>
              <li>
                Under <b>Account Resources</b>: Include | Account | <i>Your Account Name</i>
              </li>
              <li>Click <b>Continue to summary</b> → <b>Create Token</b></li>
            </ol>
          </div>
        </div>
        <Input
          label="Account ID"
          bind:value={cloudflareForm.accountId}
          placeholder="your-cloudflare-account-id"
          hint="Found on the right side of the Cloudflare dashboard overview page."
        />
        <div>
          <Input
            label="API Token"
            type="password"
            bind:value={cloudflareForm.apiToken}
            placeholder="your-cloudflare-api-token"
          />
          <p class="text-xs text-text-muted mt-1">
            Requires "Workers Scripts: Edit" permission.
            <a
              href="https://dash.cloudflare.com/profile/api-tokens"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline"
            >
              Get token →
            </a>
          </p>
        </div>
        <Input
          label="Worker Name"
          bind:value={cloudflareForm.projectName}
          placeholder="my-relay"
          hint="Unique name for your Cloudflare Worker. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleCloudflareDeploy}
            disabled={!cloudflareForm.accountId.trim() ||
              !cloudflareForm.apiToken.trim() ||
              deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Worker'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeCloudflareModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showCloudflareBulkModal}
      title="CF Bulk"
      size="3xl"
      onClose={closeCloudflareBulkModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-amber-500/10 border border-amber-500/20 p-3 flex flex-col gap-1">
          <p class="text-sm font-medium text-text-main">One account per line (Batch of 10 concurrent)</p>
          <p class="text-xs text-text-muted">
            Format: <code class="font-mono text-amber-500">name/email|accountID|apiToken</code>. Nama worker/subdomain otomatis memakai Pool Name + urutan batch (bukan nama/email akun).
          </p>
        </div>

        <Input
          label="Pool Name / Worker Prefix"
          bind:value={cloudflareBulkPoolName}
          disabled={bulkCloudflareDeploying}
          placeholder="e.g. cloudflare-relay, cf-indo, cf-pool"
          hint="Prefix untuk nama pool dan subdomain worker Cloudflare (contoh: cloudflare-relay-1, cloudflare-relay-2)."
        />

        <div class="flex flex-col gap-1.5">
          <label for="cf-bulk-textarea" class="text-xs font-medium text-text-muted">Accounts List</label>
          <textarea
            id="cf-bulk-textarea"
            bind:value={cloudflareBulkText}
            disabled={bulkCloudflareDeploying}
            placeholder={"email@example.com|account-id|api-token\nteam-account|account-id|api-token"}
            spellcheck="false"
            rows={10}
            class="min-h-[220px] w-full resize-y rounded-lg border border-border bg-surface px-3 py-2 font-mono text-xs text-text-main outline-none focus:border-brand-500 disabled:opacity-50"
          ></textarea>
        </div>

        {#if cloudflareBulkProgress.total > 0}
          <div class="rounded-lg border border-border bg-surface p-3 flex flex-col gap-2">
            <div class="flex items-center justify-between text-xs font-medium">
              <span class="text-text-main flex items-center gap-1.5">
                {#if bulkCloudflareDeploying}
                  <span class="inline-block size-2 rounded-full bg-brand-500 animate-pulse"></span>
                {/if}
                {bulkCloudflareDeploying
                  ? `Deploying Batch ${cloudflareBulkProgress.currentBatch} of ${cloudflareBulkProgress.totalBatches}...`
                  : 'Deployment Complete'}
              </span>
              <span class="font-mono text-text-muted">
                {cloudflareBulkProgress.completed} / {cloudflareBulkProgress.total} ({Math.round(
                  (cloudflareBulkProgress.completed / cloudflareBulkProgress.total) * 100
                ) || 0}%)
              </span>
            </div>

            <div class="flex h-2 w-full overflow-hidden rounded-full bg-surface-2">
              <div
                class="bg-emerald-500 transition-all duration-300"
                style="width: {(cloudflareBulkProgress.success / cloudflareBulkProgress.total) * 100}%"
                title="{cloudflareBulkProgress.success} succeeded"
              ></div>
              <div
                class="bg-red-500 transition-all duration-300"
                style="width: {(cloudflareBulkProgress.failed / cloudflareBulkProgress.total) * 100}%"
                title="{cloudflareBulkProgress.failed} failed"
              ></div>
            </div>

            <div class="flex items-center justify-between text-[11px] text-text-muted pt-0.5">
              <div class="flex items-center gap-3">
                <span class="flex items-center gap-1 text-emerald-500 font-medium">
                  <span class="size-1.5 rounded-full bg-emerald-500"></span>
                  {cloudflareBulkProgress.success} Success
                </span>
                <span class="flex items-center gap-1 text-red-500 font-medium">
                  <span class="size-1.5 rounded-full bg-red-500"></span>
                  {cloudflareBulkProgress.failed} Failed
                </span>
              </div>
              <span>Concurrency: 10/batch</span>
            </div>
          </div>
        {/if}

        {#if cloudflareBulkResults.length > 0}
          <div class="max-h-60 overflow-y-auto rounded-lg border border-border divide-y divide-border">
            {#each cloudflareBulkResults as result}
              <div class="flex items-start gap-3 p-2.5 text-xs">
                <span class="w-6 shrink-0 text-text-muted font-mono">#{result.lineNumber}</span>
                <div class="min-w-0 flex-1 flex flex-col gap-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="font-medium text-text-main">{result.label}</span>
                    <Badge
                      size="sm"
                      variant={result.status === 'success'
                        ? 'success'
                        : result.status === 'failed'
                          ? 'error'
                          : result.status === 'running'
                            ? 'warning'
                            : 'neutral'}
                    >
                      {result.status}
                    </Badge>
                  </div>
                  <p class="font-mono text-text-muted text-[11px]">
                    {result.accountId} · {result.projectName}
                  </p>
                  {#if result.deployUrl}
                    <p class="break-all text-emerald-500 text-[11px] font-mono">{result.deployUrl}</p>
                  {/if}
                  {#if result.error}
                    <p class="break-words text-red-500 text-[11px]">{result.error}</p>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleCloudflareBulkDeploy}
            disabled={!cloudflareBulkText.trim() || bulkCloudflareDeploying}
          >
            {bulkCloudflareDeploying
              ? `Deploying (${cloudflareBulkProgress.completed}/${cloudflareBulkProgress.total})...`
              : 'Deploy All'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeCloudflareBulkModal} disabled={bulkCloudflareDeploying}>
            Close
          </Button>
        </div>
      </div>
    </Modal>

    <Modal isOpen={showDenoModal} title="Deploy Deno Relay" onClose={closeDenoModal}>
      <div class="flex flex-col gap-4">
        <div
          class="rounded-lg bg-black/5 dark:bg-white/5 border border-black/10 dark:border-white/10 p-3 flex flex-col gap-1.5"
        >
          <p class="text-sm text-text-main font-medium">What is Deno Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a relay worker to Deno Deploy's global edge network. All AI provider requests
            are forwarded through Deno's edge, masking your real IP.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>Deno Deploy v2 runs on a high-performance global edge network</li>
            <li>Free tier: 1M requests & 100GiB outbound traffic per month</li>
            <li>No per-request CPU time limits (unlike Vercel/Cloudflare)</li>
            <li>Support up to 20 active apps & 50 custom domains</li>
            <li>Deploy multiple relays for maximum IP diversity</li>
          </ul>
          <div
            class="mt-2 pt-2 border-t border-black/10 dark:border-white/10 text-xs text-text-muted"
          >
            <p class="font-medium text-text-main mb-1">How to generate API token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>console.deno.com</b></li>
              <li>Select your <b>Organization</b> → <b>Settings</b> → <b>Organization Tokens</b></li>
              <li>Create a <b>Organization Token</b> (prefix <b>ddo_</b>)</li>
            </ol>
          </div>
        </div>
        <div>
          <Input
            label="Deno Deploy API Token"
            type="password"
            bind:value={denoForm.denoToken}
            placeholder="ddo_xxxxxxxxxxxxxxxx"
          />
          <p class="text-xs text-text-muted mt-1">
            Token is used once for deployment, not stored. Found in Organization Settings.
          </p>
        </div>
        <Input
          label="Organization Domain"
          bind:value={denoForm.orgDomain}
          placeholder="your-org.deno.net"
          hint="Organization's default domain. Your relay URL will be in the format: https://my-relay.your-org.deno.net"
        />
        <Input
          label="App Name"
          bind:value={denoForm.projectName}
          placeholder="deno-relay"
          hint="Unique app name. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleDenoDeploy}
            disabled={!denoForm.denoToken.trim() || !denoForm.orgDomain.trim() || deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Relay'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeDenoModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showNetlifyModal}
      title="Deploy Netlify Relay"
      onClose={closeNetlifyModal}
    >
      <div class="flex flex-col gap-4">
        <div
          class="rounded-lg bg-black/5 dark:bg-white/5 border border-black/10 dark:border-white/10 p-3 flex flex-col gap-1.5"
        >
          <p class="text-sm text-text-main font-medium">What is Netlify Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a serverless relay function to Netlify's global edge network. All AI provider
            requests are forwarded through Netlify's edge, masking your real IP.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>
              Runs on Netlify Functions with a streaming response and a 30-second execution limit
            </li>
            <li>Free tier: 125,000 function invocations and 100GB bandwidth per month</li>
            <li>Relay URL format: https://your-site.netlify.app/.netlify/functions/relay</li>
            <li>Deploy multiple relays on different accounts for more IP diversity</li>
          </ul>
          <div
            class="mt-2 pt-2 border-t border-black/10 dark:border-white/10 text-xs text-text-muted"
          >
            <p class="font-medium text-text-main mb-1">How to generate API token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>User Settings</b> → <b>Applications</b> → <b>Personal access tokens</b></li>
              <li>Select <b>New access token</b> and generate a token</li>
              <li>Copy the token — you won't see it again after leaving the page</li>
            </ol>
          </div>
        </div>
        <div>
          <Input
            label="Netlify API Token"
            type="password"
            bind:value={netlifyForm.netlifyToken}
            placeholder="nfp_xxxxxxxxxxxxxxxx"
          />
          <p class="text-xs text-text-muted mt-1">Token is used once for deployment, not stored.</p>
        </div>
        <Input
          label="Site Name"
          bind:value={netlifyForm.projectName}
          placeholder="netlify-relay"
          hint="Unique site name (lowercase letters, numbers, hyphens). Your relay URL will be https://site-name.netlify.app/.netlify/functions/relay"
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleNetlifyDeploy}
            disabled={!netlifyForm.netlifyToken.trim() || deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Relay'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeNetlifyModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

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
              <label for="bulk-proxies" class="text-sm font-medium text-text-main mb-1 block">
                Paste Proxy List (One per line)
              </label>
              <textarea
                id="bulk-proxies"
                bind:value={batchImportText}
                placeholder={'http://user:pass@127.0.0.1:7897\n127.0.0.1:7897:user:pass'}
                class="w-full min-h-[180px] py-2 px-3 text-sm text-text-main bg-surface-2 border border-transparent rounded-[10px] focus:ring-1 focus:ring-primary/30 focus:border-primary/50 focus:outline-none transition-all font-mono"
              ></textarea>
              <p class="text-xs text-text-muted mt-1">
                Supported formats: protocol://user:pass@host:port, host:port:user:pass
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
