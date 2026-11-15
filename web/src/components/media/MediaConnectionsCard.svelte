<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type ProviderConnection, type Settings } from '../../api/client'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import type { ProviderCatalogItem } from '../../lib/providers'
  import { loadMediaRoster, mediaRoster, MEDIA_ROSTER_PAGE_SIZE } from './mediaConnectionStore.svelte'

  interface Props {
    provider: ProviderCatalogItem
    settings?: Settings
    onRefresh: () => void
  }

  let { provider, settings = {}, onRefresh }: Props = $props()

  // This card owns its provider's roster. The dashboard no longer ships the
  // full connection list to the browser, so the rows are fetched here, one
  // provider at a time and one page at a time.
  let roster = $derived(mediaRoster(provider.id))

  let connPage = $state(1)
  let connPageSize = $state(MEDIA_ROSTER_PAGE_SIZE)
  let isLoading = $state(false)

  onMount(() => {
    void loadMediaRoster(provider.id, connPage, connPageSize)
  })

  $effect(() => {
    // The effect reads the provider id and the page, so both changes
    // refetch. The roster is not a dependency: writing it must not loop.
    void provider.id
    void connPage
    void connPageSize
    isLoading = true
    loadMediaRoster(provider.id, connPage, connPageSize).finally(() => {
      isLoading = false
    })
  })

  let totalConnections = $derived(roster.total ?? roster.connections.length)

  // The pager's page count follows the server-reported total, so a pool of
  // a thousand accounts reports fifty pages instead of one.
  let totalPages = $derived(Math.max(1, Math.ceil(totalConnections / connPageSize)))

  // Strategy & Round Robin
  let isRoundRobin = $state(false)
  let stickyLimit = $state('1')

  $effect(() => {
    const strategy = settings?.providerStrategies?.[provider.id]
    isRoundRobin = strategy?.fallbackStrategy === 'round-robin'
    stickyLimit = String(strategy?.stickyRoundRobinLimit ?? 1)
  })

  async function toggleRoundRobin() {
    isRoundRobin = !isRoundRobin
    const current = settings?.providerStrategies || {}
    const updated = {
      ...current,
      [provider.id]: {
        ...(current[provider.id] || {}),
        fallbackStrategy: isRoundRobin ? 'round-robin' : 'priority',
        stickyRoundRobinLimit: Number(stickyLimit) || 1
      }
    }
    try {
      await api.updateSettings({ providerStrategies: updated })
      onRefresh()
    } catch (e) {
      console.error('Failed to toggle round robin', e)
    }
  }

  async function handleStickyLimitChange() {
    if (!isRoundRobin) return
    const current = settings?.providerStrategies || {}
    const updated = {
      ...current,
      [provider.id]: {
        ...(current[provider.id] || {}),
        fallbackStrategy: 'round-robin',
        stickyRoundRobinLimit: Number(stickyLimit) || 1
      }
    }
    try {
      await api.updateSettings({ providerStrategies: updated })
      onRefresh()
    } catch (e) {
      console.error('Failed to update sticky limit', e)
    }
  }

  // New connection
  let showAddConnModal = $state(false)
  let newConnKey = $state('')
  let newConnName = $state('')
  let newConnError = $state('')
  let isSavingConn = $state(false)

  async function handleSaveNewConnection() {
    if (!newConnKey.trim()) {
      newConnError = 'API Key is required.'
      return
    }
    isSavingConn = true
    newConnError = ''
    try {
      await api.createConnection({
        provider: provider.id,
        authType: 'apikey',
        apiKey: newConnKey.trim(),
        name: newConnName.trim() || undefined
      })
      showAddConnModal = false
      newConnKey = ''
      newConnName = ''
      onRefresh()
      await loadMediaRoster(provider.id, connPage, connPageSize)
    } catch (err) {
      newConnError = err instanceof Error ? err.message : String(err)
    } finally {
      isSavingConn = false
    }
  }

  // Per-row actions
  async function handleToggleConnActive(conn: ProviderConnection) {
    try {
      await api.updateConnection(conn.id, { isActive: conn.isActive === 1 ? 0 : 1 })
      onRefresh()
      await loadMediaRoster(provider.id, connPage, connPageSize)
    } catch (e) {
      console.error('Failed to toggle connection active', e)
    }
  }

  async function handleDeleteConn(id: string) {
    if (!confirm('Are you sure you want to delete this connection?')) return
    try {
      await api.deleteConnection(id)
      onRefresh()
      await loadMediaRoster(provider.id, connPage, connPageSize)
    } catch (e) {
      console.error('Failed to delete connection', e)
    }
  }

  // Non-null while a reorder request is in flight.
  let reorderingConnId = $state<string | null>(null)

  // Single server-side transactional call instead of two independent PUTs:
  // a partial failure between those two writes left two rows sharing a
  // priority, and a stable sort over tied priorities made every later click a
  // literal no-op. The in-flight flag also stops a second click from firing a
  // swap computed from the same stale list.
  async function handleMovePriority(conn: ProviderConnection, delta: -1 | 1) {
    if (!conn || reorderingConnId) return
    reorderingConnId = conn.id
    try {
      await api.reorderConnection(conn.id, delta < 0 ? 'up' : 'down')
      onRefresh()
      await loadMediaRoster(provider.id, connPage, connPageSize)
    } catch (e) {
      alert(`Reorder failed: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      reorderingConnId = null
    }
  }

  // Edit connection
  let editingConn = $state<ProviderConnection | null>(null)
  let editConnName = $state('')
  let isEditingActive = $state(true)

  function handleOpenEditConn(conn: ProviderConnection) {
    editingConn = conn
    editConnName = formatEmailLabel(conn.displayName || conn.name, $emailPrivacy) || ''
    isEditingActive = conn.isActive === 1
  }

  async function handleSaveEditedConn() {
    if (!editingConn) return
    try {
      const payload: { name?: string; isActive?: number } = {
        isActive: isEditingActive ? 1 : 0
      }
      const trimmed = editConnName.trim()
      const original = (editingConn.displayName || editingConn.name || '').trim()
      const masked = formatEmailLabel(original, $emailPrivacy).trim()
      if (trimmed !== original && trimmed !== masked) {
        payload.name = trimmed || undefined
      }
      await api.updateConnection(editingConn.id, payload)
      editingConn = null
      onRefresh()
      await loadMediaRoster(provider.id, connPage, connPageSize)
    } catch (err) {
      alert(`Save failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }
</script>

<!-- Connections Card. NoAuth providers have no connections to list, and
     their proxy strategy is NOT set here: the card that owns it lives on the
     provider's Connections detail page (ProviderDetailView). This page used
     to render that card too, offering a chat-egress setting media handlers
     never read — one setting, one home. -->
<div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] p-6">
  <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4">
    <h2 class="text-lg font-semibold text-text-main">Connections</h2>
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-xs text-text-muted font-medium">Round Robin</span>
      <div class="flex items-center gap-3">
        <button
          type="button"
          role="switch"
          aria-label="Round Robin"
          aria-checked={isRoundRobin}
          onclick={toggleRoundRobin}
          class="relative inline-flex shrink-0 cursor-pointer rounded-full transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-brand-500/30 {isRoundRobin ? 'bg-brand-500' : 'bg-surface-3'} w-11 h-6"
        >
          <span class="pointer-events-none inline-block rounded-full bg-white shadow-sm transform transition duration-200 ease-in-out {isRoundRobin ? 'translate-x-5' : 'translate-x-0.5'} size-5 mt-0.5"></span>
        </button>
      </div>
      {#if isRoundRobin}
        <div class="flex flex-wrap items-center gap-1.5 ml-2">
          <span class="text-xs text-text-muted">Sticky:</span>
          <input
            type="number"
            min="1"
            bind:value={stickyLimit}
            onchange={handleStickyLimitChange}
            class="w-16 px-2 py-1 text-xs border border-border rounded-md bg-background text-text-main focus:outline-none focus:border-brand-500"
          />
        </div>
      {/if}
    </div>
  </div>

  {#if isLoading && roster.connections.length === 0}
    <div class="flex flex-col items-center justify-center py-8 border border-dashed border-border rounded-xl text-text-muted text-xs gap-2">
      <span>Loading connections…</span>
    </div>
  {:else if roster.connections.length === 0}
    <div class="flex flex-col items-center justify-center py-8 border border-dashed border-border rounded-xl text-text-muted text-xs gap-2">
      <span>No connections yet for {provider.name}.</span>
      <button
        type="button"
        onclick={() => (showAddConnModal = true)}
        class="inline-flex items-center gap-1 px-3 py-1.5 rounded-lg bg-primary text-white font-medium hover:bg-primary/90 transition-colors cursor-pointer"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        Add Connection
      </button>
    </div>
  {:else}
    <div class="flex flex-col gap-2">
      {#each roster.connections as conn, idx (conn.id)}
        <div class="flex flex-col gap-2 p-3 rounded-lg border border-border bg-sidebar/50 sm:flex-row sm:items-center sm:justify-between">
          <div class="flex items-center gap-2 min-w-0">
            <div class="flex flex-col items-center gap-0.5 shrink-0">
              <button
                type="button"
                disabled={idx === 0 || !!reorderingConnId}
                onclick={() => handleMovePriority(conn, -1)}
                class="p-1 text-text-muted hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                title="Move Up"
              >
                <span class="material-symbols-outlined text-sm">keyboard_arrow_up</span>
              </button>
              <button
                type="button"
                disabled={idx === roster.connections.length - 1 || !!reorderingConnId}
                onclick={() => handleMovePriority(conn, 1)}
                class="p-1 text-text-muted hover:text-text-main disabled:opacity-30 disabled:cursor-not-allowed cursor-pointer"
                title="Move Down"
              >
                <span class="material-symbols-outlined text-sm">keyboard_arrow_down</span>
              </button>
            </div>
            <span class="material-symbols-outlined text-base text-text-muted shrink-0">key</span>
            <span class="text-sm font-medium text-text-main truncate">{formatEmailLabel(conn.displayName || conn.name || conn.email || conn.id, $emailPrivacy)}</span>
            <button
              type="button"
              onclick={() => handleToggleConnActive(conn)}
              class="px-2 py-0.5 rounded text-[11px] font-semibold cursor-pointer transition-colors {conn.isActive === 1 ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border border-emerald-500/20' : 'bg-surface-2 text-text-muted border border-border'}"
            >
              {conn.isActive === 1 ? 'active' : 'disabled'}
            </button>
            <span class="text-xs text-text-muted font-mono">#{idx + 1}</span>
          </div>
          <div class="flex items-center gap-1.5 shrink-0">
            <button
              type="button"
              onclick={() => handleOpenEditConn(conn)}
              class="inline-flex items-center gap-1 px-2.5 py-1 text-xs rounded-lg border border-border hover:bg-surface-2 text-text-main transition-colors cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">edit</span>
              Edit
            </button>
            <button
              type="button"
              onclick={() => handleDeleteConn(conn.id)}
              class="inline-flex items-center gap-1 px-2.5 py-1 text-xs rounded-lg border border-red-500/20 text-red-500 hover:bg-red-500/10 transition-colors cursor-pointer"
            >
              <span class="material-symbols-outlined text-sm">delete</span>
              Delete
            </button>
          </div>
        </div>
      {/each}
      <div class="pt-2">
        <button
          type="button"
          onclick={() => (showAddConnModal = true)}
          class="inline-flex items-center gap-1 px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer"
        >
          <span class="material-symbols-outlined text-sm">add</span>
          Add
        </button>
      </div>

      {#if totalPages > 1}
        <div class="flex items-center justify-between pt-3 mt-1 border-t border-border text-xs text-text-muted">
          <span>
            Page {connPage} of {totalPages} · {totalConnections} connection{totalConnections === 1 ? '' : 's'}
          </span>
          <div class="flex items-center gap-2">
            <button
              type="button"
              disabled={connPage <= 1}
              onclick={() => (connPage = Math.max(1, connPage - 1))}
              class="px-2.5 py-1 rounded-lg border border-border hover:bg-surface-2 text-text-main disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
            >
              Previous
            </button>
            <button
              type="button"
              disabled={connPage >= totalPages}
              onclick={() => (connPage = Math.min(totalPages, connPage + 1))}
              class="px-2.5 py-1 rounded-lg border border-border hover:bg-surface-2 text-text-main disabled:opacity-40 disabled:cursor-not-allowed cursor-pointer"
            >
              Next
            </button>
          </div>
        </div>
      {/if}
    </div>
  {/if}
</div>

<!-- Modal: Add Connection -->
{#if showAddConnModal}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 transition-opacity"
      onclick={() => (showAddConnModal = false)}
      role="presentation"
    ></div>
    <div class="relative w-full max-w-md bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-elev)] p-6 z-10 flex flex-col gap-4">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold text-text-main">Add {provider.name} Connection</h3>
        <button
          type="button"
          onclick={() => (showAddConnModal = false)}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      {#if newConnError}
        <div class="p-2.5 rounded-lg bg-red-500/10 border border-red-500/30 text-red-500 text-xs">
          {newConnError}
        </div>
      {/if}
      <div class="flex flex-col gap-1.5">
        <label for="new-conn-key" class="text-xs font-medium text-text-muted">API Key / Token</label>
        <input
          id="new-conn-key"
          type="password"
          bind:value={newConnKey}
          placeholder="Enter API key"
          class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background text-text-main focus:outline-none focus:border-brand-500 font-mono"
        />
      </div>
      <div class="flex flex-col gap-1.5">
        <label for="new-conn-name" class="text-xs font-medium text-text-muted">Display Name (optional)</label>
        <input
          id="new-conn-name"
          type="text"
          bind:value={newConnName}
          placeholder="e.g. Primary Account"
          class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background text-text-main focus:outline-none focus:border-brand-500"
        />
      </div>
      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <button
          type="button"
          onclick={() => (showAddConnModal = false)}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={handleSaveNewConnection}
          disabled={isSavingConn}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer disabled:opacity-50"
        >
          {isSavingConn ? 'Saving...' : 'Add Connection'}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Modal: Edit Connection -->
{#if editingConn}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 transition-opacity"
      onclick={() => (editingConn = null)}
      role="presentation"
    ></div>
    <div class="relative w-full max-w-md bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-elev)] p-6 z-10 flex flex-col gap-4">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold text-text-main">Edit Connection</h3>
        <button
          type="button"
          onclick={() => (editingConn = null)}
          class="p-1 rounded text-text-muted hover:text-text-main cursor-pointer"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>
      <div class="flex flex-col gap-1.5">
        <label for="edit-conn-name" class="text-xs font-medium text-text-muted">Display Name</label>
        <input
          id="edit-conn-name"
          type="text"
          bind:value={editConnName}
          class="w-full px-3 py-2 text-sm border border-border rounded-lg bg-background text-text-main focus:outline-none focus:border-brand-500"
        />
      </div>
      <div class="flex items-center justify-between py-1">
        <span class="text-xs font-medium text-text-muted">Active</span>
        <input type="checkbox" bind:checked={isEditingActive} class="size-4 rounded text-brand-500 focus:ring-brand-500" />
      </div>
      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <button
          type="button"
          onclick={() => (editingConn = null)}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer"
        >
          Cancel
        </button>
        <button
          type="button"
          onclick={handleSaveEditedConn}
          class="px-3 py-1.5 text-xs font-semibold rounded-lg bg-primary text-white hover:bg-primary/90 transition-colors cursor-pointer"
        >
          Save
        </button>
      </div>
    </div>
  </div>
{/if}
