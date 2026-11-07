<script lang="ts">
  import { api, type JailbreakDoc, type JailbreakModelConfig } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import Toggle from '../../lib/ui/Toggle.svelte'
  import Pagination from '../Pagination.svelte'
  import BindingBatchBar from './BindingBatchBar.svelte'

  interface Props {
    /** The binding map: target id → {enabled, presetId}. */
    models: Record<string, JailbreakModelConfig>
    presets: JailbreakDoc[]
    /** Known combo names, for the MODEL/COMBO type chip on each row. */
    comboNames: Set<string>
    onAdd: () => void
    onEdit: (id: string, cfg: JailbreakModelConfig) => void
    /** Reload presets + bindings + usage after a mutation. */
    onChanged: () => Promise<void> | void
    /** Surface a failure on the page status line. */
    onError: (message: string) => void
  }

  let { models, presets, comboNames, onAdd, onEdit, onChanged, onError }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function presetLabel(id: string): string {
    if (id === 'off') return 'Off'
    const p = presets.find((x) => x.id === id)
    return p ? p.name || p.id || id : id
  }

  let boundEntries = $derived(Object.entries(models))

  // Pagination + batch selection over the model-binding list.
  let bindingPage = $state(1)
  let bindingPageSize = $state(10)
  let bindingSearch = $state('')
  let selectedBindingIds = $state<Set<string>>(new Set())
  let bindingBatchBusy = $state(false)
  let bindingBatchStatus = $state('')
  let bindingBatchPresetId = $state('')

  let filteredBindings = $derived.by(() => {
    const q = bindingSearch.trim().toLowerCase()
    if (!q) return boundEntries
    return boundEntries.filter(
      ([id, cfg]) => id.toLowerCase().includes(q) || presetLabel(cfg.presetId).toLowerCase().includes(q),
    )
  })
  let bindingPageCount = $derived(Math.max(1, Math.ceil(filteredBindings.length / bindingPageSize)))
  let pagedBindings = $derived.by(() => {
    const start = (bindingPage - 1) * bindingPageSize
    return filteredBindings.slice(start, start + bindingPageSize)
  })
  $effect(() => {
    if (bindingPage > bindingPageCount) bindingPage = bindingPageCount
  })

  function toggleBindingSelect(id: string) {
    const next = new Set(selectedBindingIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    selectedBindingIds = next
  }
  function toggleSelectAllBindingsOnPage() {
    const ids = pagedBindings.map(([id]) => id)
    const allSelected = ids.length > 0 && ids.every((id) => selectedBindingIds.has(id))
    const next = new Set(selectedBindingIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedBindingIds = next
  }
  // Select all reaches every binding the filter matches, not just the page.
  function toggleSelectAllBindingsFiltered() {
    const ids = filteredBindings.map(([id]) => id)
    const allSelected = ids.length > 0 && ids.every((id) => selectedBindingIds.has(id))
    const next = new Set(selectedBindingIds)
    if (allSelected) ids.forEach((id) => next.delete(id))
    else ids.forEach((id) => next.add(id))
    selectedBindingIds = next
  }
  function clearBindingSelection() {
    selectedBindingIds = new Set()
  }

  // Batch menu for bindings: turn the picked ids on or off, rebind them to one
  // preset, or unbind them. Each step reports per-item failures so a partial
  // batch still tells the operator what happened.
  async function batchSetBindingsEnabled(enabled: boolean) {
    const ids = Array.from(selectedBindingIds)
    if (ids.length === 0) return
    bindingBatchBusy = true
    bindingBatchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      const cfg = models[id]
      if (!cfg) continue
      try {
        await api.setJailbreakModel(id, cfg.presetId, enabled)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bindingBatchStatus =
      failures.length === 0
        ? `${enabled ? 'Enabled' : 'Disabled'} ${ok} binding(s).`
        : `Updated ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedBindingIds = new Set()
    bindingBatchBusy = false
    await onChanged()
  }
  async function batchRebindPreset() {
    const ids = Array.from(selectedBindingIds)
    const preset = bindingBatchPresetId
    if (ids.length === 0 || !preset) {
      bindingBatchStatus = 'Pick ids and a preset first.'
      return
    }
    bindingBatchBusy = true
    bindingBatchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.setJailbreakModel(id, preset, true)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bindingBatchStatus =
      failures.length === 0
        ? `Rebound ${ok} binding(s).`
        : `Rebound ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedBindingIds = new Set()
    bindingBatchBusy = false
    await onChanged()
  }
  async function batchUnbindModels() {
    const ids = Array.from(selectedBindingIds)
    if (ids.length === 0) return
    bindingBatchBusy = true
    bindingBatchStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.deleteJailbreakModel(id)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bindingBatchStatus =
      failures.length === 0
        ? `Unbound ${ok} model(s).`
        : `Unbound ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    selectedBindingIds = new Set()
    bindingBatchBusy = false
    await onChanged()
  }

  async function toggleModel(id: string, cfg: JailbreakModelConfig) {
    try {
      await api.setJailbreakModel(id, cfg.presetId, !cfg.enabled)
      await onChanged()
    } catch (e) {
      onError('Toggle failed: ' + errorMessage(e))
    }
  }

  async function unbindModel(id: string) {
    try {
      await api.deleteJailbreakModel(id)
      await onChanged()
    } catch (e) {
      bindingBatchStatus = 'Unbind failed: ' + errorMessage(e)
    }
  }
</script>

<Card padding="md" class="overflow-hidden">
  <p class="text-xs text-text-subtle">
    Bind a model, provider, or combo id to a preset. The id is matched exactly as the client sends it.
    A keyword on a preset already covers every id it matches, so a binding is only needed to pick a
    different preset for one specific id.
  </p>
  {#if bindingBatchStatus}<p class="pt-2 text-xs text-text-muted">{bindingBatchStatus}</p>{/if}

  {#if boundEntries.length === 0}
    <div class="flex flex-col items-center gap-2 px-4 py-10 text-center">
      <span class="material-symbols-outlined text-[22px] text-text-subtle">link_off</span>
      <p class="text-sm font-medium text-text-main">No models armed</p>
      <p class="max-w-md text-xs text-text-muted">
        Add a binding to inject a preset for one exact model, provider, or combo id.
      </p>
      <Button variant="primary" size="sm" onclick={onAdd} icon="add">Add binding</Button>
    </div>
  {:else}
    <!-- The filter row lives outside the match/no-match branch: when a query
         matches nothing, the field has to stay on screen or the operator
         cannot clear what they typed. -->
    <div class="flex flex-wrap items-center gap-2 pt-4">
      <input
        type="text"
        bind:value={bindingSearch}
        placeholder="Filter bindings…"
        aria-label="Filter bindings"
        class="min-w-[240px] flex-1 rounded-brand border border-border-subtle bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-subtle"
      />
      <select
        bind:value={bindingPageSize}
        aria-label="Rows per page"
        class="rounded-brand border border-border-subtle bg-surface-2 px-2 py-1.5 text-xs text-text-main outline-none"
      >
        <option value={10}>10 / page</option>
        <option value={20}>20 / page</option>
        <option value={50}>50 / page</option>
        <option value={100}>100 / page</option>
      </select>
    </div>

    {#if filteredBindings.length === 0}
      <p class="border-t border-border-subtle pt-3 text-xs text-text-subtle">
        No binding matches "{bindingSearch}".
      </p>
    {:else}
      <!-- Two rows: the select-all checkboxes and the selected count stay on
           the first line, and every action that applies to the selection
           moves to its own line below. -->
      <div class="flex flex-wrap items-center gap-2 border-t border-border-subtle pt-3">
        <label class="flex cursor-pointer items-center gap-2 text-xs text-text-subtle select-none" title="Select the bindings on this page only">
          <input
            type="checkbox"
            checked={pagedBindings.length > 0 && pagedBindings.every(([id]) => selectedBindingIds.has(id))}
            onchange={toggleSelectAllBindingsOnPage}
            class="h-4 w-4 accent-brand-500"
          />
          Select page
        </label>
        <label class="flex cursor-pointer items-center gap-2 text-xs text-text-subtle select-none" title="Select every binding the current filter matches, across all pages">
          <input
            type="checkbox"
            checked={filteredBindings.length > 0 && filteredBindings.every(([id]) => selectedBindingIds.has(id))}
            onchange={toggleSelectAllBindingsFiltered}
            class="h-4 w-4 accent-brand-500"
          />
          Select all ({filteredBindings.length})
        </label>
        {#if selectedBindingIds.size > 0}
          <span class="inline-flex items-center rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[11px] font-semibold text-brand-500">
            {selectedBindingIds.size} selected
          </span>
        {/if}
      </div>
      {#if selectedBindingIds.size > 0}
        <BindingBatchBar
          {presets}
          busy={bindingBatchBusy}
          bind:presetId={bindingBatchPresetId}
          onRebind={batchRebindPreset}
          onSetEnabled={batchSetBindingsEnabled}
          onUnbind={batchUnbindModels}
          onClear={clearBindingSelection}
        />
      {/if}

      <!-- Rows: a table on wide screens, stacked cards on the phone. The
           header row only exists where the grid does. -->
      <div class="-mx-5 mt-4 border-t border-border-subtle">
        <div class="hidden gap-3 border-b border-border-subtle px-5 py-2 md:grid md:grid-cols-[1.25rem_minmax(0,1.5fr)_minmax(0,1fr)_auto_auto] md:items-center">
          <span></span>
          <span class="text-[10px] font-semibold tracking-wider text-text-subtle uppercase">Target</span>
          <span class="text-[10px] font-semibold tracking-wider text-text-subtle uppercase">Preset</span>
          <span class="text-[10px] font-semibold tracking-wider text-text-subtle uppercase">Inject</span>
          <span></span>
        </div>
        <ul class="divide-y divide-border-subtle">
          {#each pagedBindings as [id, cfg] (id)}
            <li class="flex flex-col gap-2 px-5 py-3 md:grid md:grid-cols-[1.25rem_minmax(0,1.5fr)_minmax(0,1fr)_auto_auto] md:items-center md:gap-3">
              <input
                type="checkbox"
                checked={selectedBindingIds.has(id)}
                onchange={() => toggleBindingSelect(id)}
                aria-label={`Select ${id}`}
                class="h-4 w-4 shrink-0 accent-brand-500 max-md:hidden"
              />
              <div class="flex min-w-0 items-center gap-2">
                <input
                  type="checkbox"
                  checked={selectedBindingIds.has(id)}
                  onchange={() => toggleBindingSelect(id)}
                  aria-label={`Select ${id}`}
                  class="h-4 w-4 shrink-0 accent-brand-500 md:hidden"
                />
                {#if comboNames.has(id)}
                  <span class="shrink-0 rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-brand-500">COMBO</span>
                {:else}
                  <span class="shrink-0 rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">MODEL</span>
                {/if}
                <span class="truncate font-code text-sm text-text-main" title={id}>{id}</span>
              </div>
              <div class="min-w-0 truncate text-xs text-text-muted max-md:pl-6">
                {presetLabel(cfg.presetId)}{cfg.enabled ? '' : ' · injection off'}
              </div>
              <div class="flex items-center gap-1 max-md:pl-6">
                <!-- Toggle: the binding stays listed either way, only injection
                     is switched on or off. -->
                <Toggle
                  checked={cfg.enabled}
                  label={`${cfg.enabled ? 'Disable' : 'Enable'} ${id}`}
                  title={cfg.enabled ? 'On — click to disable' : 'Off — click to enable'}
                  onChange={() => toggleModel(id, cfg)}
                />
                <button
                  type="button"
                  onclick={() => onEdit(id, cfg)}
                  aria-label={`Edit ${id}`}
                  title="Edit"
                  class="rounded-brand p-1.5 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-brand-500"
                >
                  <span class="material-symbols-outlined text-[16px]">edit</span>
                </button>
                <button
                  type="button"
                  onclick={() => unbindModel(id)}
                  aria-label={`Unbind ${id}`}
                  title="Unbind"
                  class="rounded-brand p-1.5 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-error"
                >
                  <span class="material-symbols-outlined text-[16px]">delete</span>
                </button>
              </div>
              <span class="max-md:hidden"></span>
            </li>
          {/each}
        </ul>
      </div>

      <Pagination
        page={bindingPage}
        pageCount={bindingPageCount}
        total={filteredBindings.length}
        noun="binding"
        onPage={(p) => (bindingPage = p)}
      />
    {/if}
  {/if}
</Card>
