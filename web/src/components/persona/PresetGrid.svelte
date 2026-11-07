<script lang="ts">
  import { api, type JailbreakDoc } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Pagination from '../Pagination.svelte'
  import PresetCard from './PresetCard.svelte'

  interface Props {
    presets: JailbreakDoc[]
    presetUsage: Record<string, number>
    onEdit: (preset: JailbreakDoc) => void
    onNew: () => void
    /** Reload presets + bindings + usage after a mutation. */
    onChanged: () => Promise<void> | void
    /** Surface a failure on the page status line. */
    onError: (message: string) => void
  }

  let { presets, presetUsage, onEdit, onNew, onChanged, onError }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // Pagination + batch selection for the preset list.
  let presetPage = $state(1)
  let presetPageSize = $state(10)
  let presetSearch = $state('')
  let selectedPresetIds = $state<Set<string>>(new Set())
  let batchBusy = $state(false)
  let batchStatus = $state('')
  let confirmDeleteId = $state<string | null>(null)

  // Sorting for the preset list. The default is name A→Z; the other modes are
  // what an operator reaches for in a long library — newest first to find what
  // was just added, recently updated to find what was just edited, most-used to
  // see what actually earns its keep, and kind to group the instruction files.
  let presetSort = $state<'name-asc' | 'name-desc' | 'newest' | 'updated' | 'usage' | 'kind'>('name-asc')
  let sortedPresets = $derived.by(() => {
    const list = [...presets]
    const byName = (a: JailbreakDoc, b: JailbreakDoc) =>
      (a.name || a.id || '').localeCompare(b.name || b.id || '')
    if (presetSort === 'name-desc') return list.sort((a, b) => byName(b, a))
    if (presetSort === 'newest') {
      return list.sort((a, b) => Date.parse(b.createdAt || '0') - Date.parse(a.createdAt || '0') || byName(a, b))
    }
    if (presetSort === 'updated') {
      return list.sort((a, b) => Date.parse(b.updatedAt || '0') - Date.parse(a.updatedAt || '0') || byName(a, b))
    }
    if (presetSort === 'usage') {
      return list.sort((a, b) => (presetUsage[b.id ?? ''] ?? 0) - (presetUsage[a.id ?? ''] ?? 0) || byName(a, b))
    }
    if (presetSort === 'kind') {
      return list.sort((a, b) => (a.kind || 'zzz').localeCompare(b.kind || 'zzz') || byName(a, b))
    }
    return list.sort(byName)
  })

  let filteredPresets = $derived.by(() => {
    const q = presetSearch.trim().toLowerCase()
    if (!q) return sortedPresets
    return sortedPresets.filter(
      (p) =>
        (p.name || '').toLowerCase().includes(q) ||
        (p.id || '').toLowerCase().includes(q) ||
        (p.kind || '').toLowerCase().includes(q),
    )
  })
  let presetPageCount = $derived(Math.max(1, Math.ceil(filteredPresets.length / presetPageSize)))
  let pagedPresets = $derived.by(() => {
    const start = (presetPage - 1) * presetPageSize
    return filteredPresets.slice(start, start + presetPageSize)
  })
  // Keep the page in range when the list shrinks (a batch delete, or a filter
  // that matches fewer rows than the current page holds).
  $effect(() => {
    if (presetPage > presetPageCount) presetPage = presetPageCount
  })

  function togglePresetSelect(id: string) {
    const next = new Set(selectedPresetIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedPresetIds = next
  }
  function toggleSelectAllOnPage() {
    const ids = pagedPresets.map((p) => p.id ?? p.name ?? '').filter(Boolean)
    const allSelected = ids.every((id) => selectedPresetIds.has(id))
    const next = new Set(selectedPresetIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedPresetIds = next
  }
  // Select all reaches every preset the current filter matches, not just the
  // visible page — the two controls sit side by side so the difference is
  // obvious: "in page" is scoped, "all" is the whole filtered set.
  function toggleSelectAllFiltered() {
    const ids = filteredPresets.map((p) => p.id ?? p.name ?? '').filter(Boolean)
    const allSelected = ids.length > 0 && ids.every((id) => selectedPresetIds.has(id))
    const next = new Set(selectedPresetIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedPresetIds = next
  }
  function clearPresetSelection() {
    selectedPresetIds = new Set()
  }

  // Batch menu: delete, or export the picked docs as one JSON file. Both
  // report per-item failures rather than stopping at the first one, so a
  // partial batch still tells the operator what happened.
  async function batchDeletePresets() {
    const ids = Array.from(selectedPresetIds)
    if (ids.length === 0) return
    batchBusy = true
    batchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.deleteJailbreakPreset(id)
        ok++
      } catch {
        failures.push(id)
      }
    }
    batchStatus =
      failures.length === 0
        ? `Deleted ${ok} preset(s).`
        : `Deleted ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedPresetIds = new Set()
    batchBusy = false
    await onChanged()
  }
  async function batchExportPresets() {
    const ids = Array.from(selectedPresetIds)
    if (ids.length === 0) return
    const chosen = presets.filter((p) => ids.includes(p.id ?? p.name ?? ''))
    const blob = new Blob([JSON.stringify(chosen, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `jailbreak-presets-${chosen.length}.json`
    a.click()
    URL.revokeObjectURL(url)
    batchStatus = `Exported ${chosen.length} preset(s).`
  }

  // #4 Duplicate a preset with its rules.
  async function duplicatePreset(id: string) {
    try {
      await api.duplicateJailbreakPreset(id)
      await onChanged()
    } catch (e) {
      onError('Duplicate failed: ' + errorMessage(e))
    }
  }

  async function removePreset(id: string) {
    if (confirmDeleteId !== id) {
      confirmDeleteId = id
      return
    }
    confirmDeleteId = null
    try {
      await api.deleteJailbreakPreset(id)
      await onChanged()
    } catch (e) {
      onError('Delete failed: ' + errorMessage(e))
    }
  }
</script>

{#if presets.length === 0}
  <div class="flex flex-col items-center gap-2 rounded-brand-lg border border-dashed border-border-subtle bg-surface px-4 py-10 text-center">
    <span class="material-symbols-outlined text-[22px] text-text-subtle">note_add</span>
    <p class="text-sm font-medium text-text-main">No presets yet</p>
    <p class="max-w-md text-xs text-text-muted">
      A preset is a system prompt you write once and arm for the models, providers, or combos you choose.
    </p>
    <Button variant="primary" size="sm" onclick={onNew} icon="note_add">New preset</Button>
  </div>
{:else}
  <!-- The filter row lives outside the match/no-match branch: when a query
       matches nothing, the field has to stay on screen or the operator
       cannot clear what they typed. -->
  <div class="flex flex-wrap items-center gap-2">
    <input
      type="text"
      bind:value={presetSearch}
      placeholder="Filter presets…"
      aria-label="Filter presets"
      class="min-w-[240px] flex-1 rounded-brand border border-border-subtle bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-subtle"
    />
    <select
      bind:value={presetSort}
      aria-label="Sort presets"
      class="rounded-brand border border-border-subtle bg-surface-2 px-2 py-1.5 text-xs text-text-main outline-none"
    >
      <option value="name-asc">Name A→Z</option>
      <option value="name-desc">Name Z→A</option>
      <option value="newest">Newest first</option>
      <option value="updated">Recently updated</option>
      <option value="usage">Most used</option>
      <option value="kind">By kind</option>
    </select>
    <select
      bind:value={presetPageSize}
      aria-label="Rows per page"
      class="rounded-brand border border-border-subtle bg-surface-2 px-2 py-1.5 text-xs text-text-main outline-none"
    >
      <option value={10}>10 / page</option>
      <option value={20}>20 / page</option>
      <option value={50}>50 / page</option>
      <option value={100}>100 / page</option>
    </select>
  </div>

  <!-- Two rows: the select-all checkboxes and the selected count stay on the
       first line, and every action that applies to the selection moves to
       its own line below. One line held both, so the actions pushed the
       count sideways and wrapped unpredictably as the selection grew. -->
  <div class="flex flex-wrap items-center gap-2 pt-3">
    <label class="flex cursor-pointer items-center gap-2 text-xs text-text-subtle select-none" title="Select the presets on this page only">
      <input
        type="checkbox"
        checked={pagedPresets.length > 0 && pagedPresets.every((p) => selectedPresetIds.has(p.id ?? p.name ?? ''))}
        onchange={toggleSelectAllOnPage}
        class="h-4 w-4 accent-brand-500"
      />
      Select page
    </label>
    <label class="flex cursor-pointer items-center gap-2 text-xs text-text-subtle select-none" title="Select every preset the current filter matches, across all pages">
      <input
        type="checkbox"
        checked={filteredPresets.length > 0 && filteredPresets.every((p) => selectedPresetIds.has(p.id ?? p.name ?? ''))}
        onchange={toggleSelectAllFiltered}
        class="h-4 w-4 accent-brand-500"
      />
      Select all ({filteredPresets.length})
    </label>
    {#if selectedPresetIds.size > 0}
      <span class="inline-flex items-center rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[11px] font-semibold text-brand-500">
        {selectedPresetIds.size} selected
      </span>
    {/if}
  </div>
  {#if selectedPresetIds.size > 0}
    <div class="flex flex-wrap items-center gap-2 pt-2">
      <Button variant="outline" size="sm" onclick={batchExportPresets}>Export JSON</Button>
      <Button variant="danger" size="sm" onclick={batchDeletePresets} disabled={batchBusy}>
        {batchBusy ? 'Deleting…' : 'Delete'}
      </Button>
      <button type="button" onclick={clearPresetSelection} class="text-xs text-text-subtle hover:underline">Clear</button>
    </div>
  {/if}
  {#if batchStatus}<p class="pt-2 text-xs text-text-muted">{batchStatus}</p>{/if}

  {#if filteredPresets.length === 0}
    <p class="pt-3 text-xs text-text-subtle">No preset matches "{presetSearch}".</p>
  {:else}
    <div class="grid grid-cols-1 gap-3 pt-4 md:grid-cols-2 xl:grid-cols-3">
      {#each pagedPresets as preset (preset.id ?? preset.name)}
        <PresetCard
          {preset}
          usage={presetUsage[preset.id ?? '']}
          selected={selectedPresetIds.has(preset.id ?? preset.name ?? '')}
          confirmDelete={confirmDeleteId === (preset.id ?? preset.name ?? '')}
          onToggleSelect={togglePresetSelect}
          onEdit={onEdit}
          onDuplicate={duplicatePreset}
          onDelete={removePreset}
          onCancelDelete={() => (confirmDeleteId = null)}
        />
      {/each}
    </div>
    <Pagination
      page={presetPage}
      pageCount={presetPageCount}
      total={filteredPresets.length}
      noun="preset"
      onPage={(p) => (presetPage = p)}
    />
  {/if}
{/if}
