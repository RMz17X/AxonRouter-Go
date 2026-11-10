<script module lang="ts">
  import type { ResolvedPickerExtras } from './pickerExtras'
  let cachedPickerExtras: ResolvedPickerExtras | null = null
</script>

<script lang="ts">
  import { Info, Search, X } from 'lucide-svelte'
  import { api, type Combo, type ProviderConnection, type ProviderNode } from '../../api/client'
  import ModelPill from './ModelPill.svelte'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import {
    resolveFilteredCombos,
    resolveFilteredGroups,
    resolveModelPickerGroups,
    type PickerModel,
  } from './pickerData'
  import { EMPTY_PICKER_EXTRAS, loadPickerExtras } from './pickerExtras'
  import { emailPrivacy, formatEmailLabel } from '../../lib/privacy'
  import { parseSearchQuery } from '../../lib/modelSearchFilter'
  import { pushEscapeHandler } from '../../lib/ui/escapeStack'

  interface Props {
    isOpen: boolean
    target: 'combo' | 'vision' | 'audio' | 'judge' | 'router' | 'api-key' | 'models'
    connections?: ProviderConnection[]
    combos?: Combo[]
    providerNodes?: ProviderNode[]
    modelAliases?: Record<string, string>
    customModels?: Array<{ providerAlias?: string; id: string; name?: string; type?: string }>
    disabledModels?: Record<string, string[]>
    /** Overrides the batched fetch; the provider page passes its own cache. */
    deprecations?: Record<string, unknown>
    currentComboName?: string
    addedModelValues?: string[]
    onSelect: (modelValue: string) => void
    onDeselect?: (modelValue: string) => void
    /** When provided, replaces onSelect for provider-model pill picks and
     * receives the exact entry clicked — so a consumer can honor the
     * picked entry itself (e.g. an alias entry's own name) instead of
     * only its resolved value. Combo picks still go through onSelect. */
    onSelectEntry?: (entry: PickerModel) => void
    onClose: () => void
  }

  let {
    isOpen,
    target,
    connections = [],
    combos = [],
    providerNodes = [],
    modelAliases,
    customModels,
    disabledModels,
    deprecations,
    currentComboName,
    addedModelValues = [],
    onSelect,
    onDeselect,
    onSelectEntry,
    onClose,
  }: Props = $props()

  let searchQuery = $state('')
  let parsedQuery = $derived(parseSearchQuery(searchQuery))
  let fetchedExtras = $state<ResolvedPickerExtras>(cachedPickerExtras ?? EMPTY_PICKER_EXTRAS)

  $effect(() => {
    if (isOpen) {
      searchQuery = ''
      // Upstream parity (ComboFormModal fetchModalData): aliases drive
      // passthrough + custom-node rows; customs/disabled complete the merge.
      // One batch, one write: three chained fetches each published their own
      // $state and rebuilt the whole pill list on every settle (#61).
      void loadPickerExtras(api).then((extras) => {
        cachedPickerExtras = extras
        fetchedExtras = extras
      })
    }
  })

  // The picker opens over Create/Edit Combo. Registering on the shared stack
  // makes Escape close the picker alone; without it the dialog underneath
  // took the key and closed, leaving the picker floating over nothing.
  $effect(() => {
    if (!isOpen) return
    return pushEscapeHandler(() => onClose())
  })

  let groups = $derived(
    resolveModelPickerGroups(connections, providerNodes, {
      modelAliases: modelAliases ?? fetchedExtras.modelAliases,
      customModels: customModels ?? fetchedExtras.customModels,
      disabledModels: disabledModels ?? fetchedExtras.disabledModels,
      deprecations: deprecations ?? fetchedExtras.deprecations,
      modelCaps: fetchedExtras.modelCaps,
      connectedProviders: fetchedExtras.connectedProviders,
      providerStats: fetchedExtras.providerStats,
    }, target)
  )
  let filteredCombos = $derived(
    resolveFilteredCombos(combos, currentComboName, searchQuery, target)
  )
  let filteredGroups = $derived(resolveFilteredGroups(groups, searchQuery, target))

  let activeFilter = $state<'all' | 'connected' | 'vision' | 'reasoning' | 'decision'>('all')

  let displayGroups = $derived.by(() => {
    let grps = filteredGroups
    if (activeFilter === 'connected') {
      grps = grps.filter((g) => g.isConnected || (g.accountCount ?? 0) > 0)
    } else if (activeFilter === 'vision') {
      grps = grps
        .map((g) => ({
          ...g,
          models: g.models.filter((m) => m.caps.vision),
        }))
        .filter((g) => g.models.length > 0)
    } else if (activeFilter === 'reasoning') {
      grps = grps
        .map((g) => ({
          ...g,
          models: g.models.filter((m) => m.caps.reasoning),
        }))
        .filter((g) => g.models.length > 0)
    } else if (activeFilter === 'decision') {
      grps = grps
        .map((g) => ({
          ...g,
          models: g.models.filter((m) => m.caps.decision),
        }))
        .filter((g) => g.models.length > 0)
    }
    return grps
  })

  let totalModelsCount = $derived(groups.reduce((acc, g) => acc + g.models.length, 0))
  let connectedModelsCount = $derived(
    groups
      .filter((g) => g.isConnected || (g.accountCount ?? 0) > 0)
      .reduce((acc, g) => acc + g.models.length, 0)
  )

  function handleToggle(val: string) {
    if (addedModelValues.includes(val)) {
      if (onDeselect) {
        onDeselect(val)
      } else {
        onSelect(val)
      }
    } else {
      onSelect(val)
    }
  }

  function handleToggleModel(model: PickerModel) {
    if (onSelectEntry) {
      onSelectEntry(model)
      return
    }
    handleToggle(model.value)
  }

  function clearSearch() {
    searchQuery = ''
  }
</script>

{#if isOpen}
  <div class="fixed inset-0 z-[120] flex items-center justify-center p-4">
    <!-- Overlay -->
    <div class="absolute inset-0 bg-black/60 backdrop-blur-xs fade-in" onclick={onClose} aria-hidden="true"></div>

    <div
      class="relative w-full max-w-[1180px] bg-surface border border-border rounded-brand-lg shadow-[var(--shadow-elev)] fade-in overflow-hidden flex flex-col max-h-[85vh] z-10 p-4!"
      role="dialog"
      aria-modal="true"
    >
      <!-- Header -->
      <div class="flex items-center justify-between p-2 border-b border-border -m-4 mb-4">
        <div class="flex items-center">
          <h2 class="text-sm font-semibold text-text-main">
            {target === 'vision'
              ? 'Add Vision Model'
              : target === 'audio'
                ? 'Add Audio Model'
                : target === 'judge'
                  ? 'Select Judge Model'
                  : target === 'router'
                    ? 'Select System One Router'
                    : target === 'api-key' || target === 'models'
                      ? 'Select Allowed Models'
                      : 'Add Model to Combo'}
          </h2>
        </div>
        <button
          type="button"
          onclick={onClose}
          aria-label="Close"
          class="p-1.5 rounded-sm text-text-muted hover:bg-surface-2 hover:text-text-main transition-colors"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <!-- Info bar -->
      <div class="flex items-center gap-2 mb-3 px-2.5 py-2 bg-brand-500/10 border border-brand-500/20 rounded-lg text-xs text-text-muted">
        <Info class="w-3.5 h-3.5 text-brand-500 shrink-0" />
        <span>Click to add, click again to remove. Changes are saved automatically.</span>
      </div>

      <!-- Search & Filters -->
      <div class="mb-3 space-y-2">
        <div class="relative">
          <Search class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-text-muted pointer-events-none" />
          <input
            type="text"
            placeholder="Search models (e.g. claude -haiku, !embed, /regex/)..."
            bind:value={searchQuery}
            class="w-full bg-surface border border-border rounded pl-8 pr-24 py-1.5 text-xs text-text-main placeholder:text-text-muted focus:outline-none focus:ring-1 focus:ring-brand-500/50"
          />
          <div class="absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-1">
            {#if parsedQuery.isRegex}
              <span class="text-[10px] px-1 py-0.2 rounded bg-brand-500/10 text-brand-500 font-mono font-medium">regex</span>
            {:else if parsedQuery.negativeTerms.length > 0}
              <span class="text-[10px] px-1 py-0.2 rounded bg-amber-500/10 text-amber-500 font-mono font-medium">!exclude</span>
            {/if}
            {#if searchQuery}
              <button
                type="button"
                onclick={clearSearch}
                aria-label="Clear search"
                class="p-1 rounded text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors"
              >
                <X class="w-3.5 h-3.5" />
              </button>
            {/if}
          </div>
        </div>

        <!-- Quick filter chips -->
        <div class="flex items-center gap-1.5 overflow-x-auto text-[11px]">
          <button
            type="button"
            onclick={() => (activeFilter = 'all')}
            class="px-2 py-0.5 rounded-full transition cursor-pointer {activeFilter === 'all'
              ? 'bg-brand-500 text-white font-medium shadow-xs'
              : 'bg-surface border border-border text-text-muted hover:text-text-main hover:bg-surface-2'}"
          >
            All ({totalModelsCount})
          </button>
          <button
            type="button"
            onclick={() => (activeFilter = 'connected')}
            class="px-2 py-0.5 rounded-full transition cursor-pointer flex items-center gap-1 {activeFilter === 'connected'
              ? 'bg-emerald-600 text-white font-medium shadow-xs'
              : 'bg-surface border border-border text-text-muted hover:text-text-main hover:bg-surface-2'}"
          >
            <span class="w-1.5 h-1.5 rounded-full bg-emerald-400"></span>
            <span>Connected ({connectedModelsCount})</span>
          </button>
          <button
            type="button"
            onclick={() => (activeFilter = 'vision')}
            class="px-2 py-0.5 rounded-full transition cursor-pointer {activeFilter === 'vision'
              ? 'bg-brand-500 text-white font-medium shadow-xs'
              : 'bg-surface border border-border text-text-muted hover:text-text-main hover:bg-surface-2'}"
          >
            Vision
          </button>
          <button
            type="button"
            onclick={() => (activeFilter = 'reasoning')}
            class="px-2 py-0.5 rounded-full transition cursor-pointer {activeFilter === 'reasoning'
              ? 'bg-brand-500 text-white font-medium shadow-xs'
              : 'bg-surface border border-border text-text-muted hover:text-text-main hover:bg-surface-2'}"
          >
            Reasoning
          </button>
          <button
            type="button"
            onclick={() => (activeFilter = 'decision')}
            class="px-2 py-0.5 rounded-full transition cursor-pointer {activeFilter === 'decision'
              ? 'bg-brand-500 text-white font-medium shadow-xs'
              : 'bg-surface border border-border text-text-muted hover:text-text-main hover:bg-surface-2'}"
          >
            Decision
          </button>
        </div>
      </div>

      <!-- Categories & Models List -->
      <div class="min-h-0 flex-1 overflow-y-auto space-y-3 custom-scrollbar">
        <!-- Combos section - always first (if filter allows) -->
        {#if filteredCombos.length > 0 && activeFilter !== 'vision' && activeFilter !== 'reasoning' && activeFilter !== 'decision'}
          <div>
            <div class="flex items-center gap-1.5 mb-1.5 sticky top-0 bg-surface py-0.5 z-10">
              <span class="text-xs font-medium text-brand-500">Combos</span>
              <span class="text-[10px] text-text-muted">({filteredCombos.length})</span>
            </div>
            <div class="flex flex-wrap gap-1.5">
              {#each filteredCombos as combo (combo.id)}
                <ModelPill
                  label={combo.name}
                  value={combo.name}
                  isAdded={addedModelValues.includes(combo.name)}
                  onToggle={handleToggle}
                />
              {/each}
            </div>
          </div>
        {/if}

        <!-- Provider sections -->
        {#each displayGroups as group (group.id)}
          <div>
            <div class="flex items-center gap-1.5 mb-1.5 sticky top-0 bg-surface py-0.5 z-10">
              <ProviderArtwork
                id={group.id}
                alt={group.name}
                class="w-3.5 h-3.5 object-contain rounded-sm text-[9px] leading-none font-semibold"
              />
              <span class="text-xs font-medium text-brand-500">
                {formatEmailLabel(group.name, $emailPrivacy)}
              </span>
              {#if (group.accountCount ?? 0) > 0}
                <span class="text-[9px] px-1.5 py-0.2 rounded bg-emerald-500/10 text-emerald-500 font-semibold border border-emerald-500/20">
                  {group.accountCount} active
                </span>
              {:else if group.isConnected}
                <span class="text-[9px] px-1.5 py-0.2 rounded bg-emerald-500/10 text-emerald-500 font-semibold border border-emerald-500/20">
                  active
                </span>
              {/if}
              <span class="text-[10px] text-text-muted">
                ({group.models.length})
              </span>
            </div>
            <div class="flex flex-wrap gap-1.5">
              <!-- Keyed by name + value, not value alone: two aliases can
                   resolve to one servable model (step-5 and step-5-preview
                   both → trust/tig/step-5-preview), and duplicate keys in a
                   keyed each corrupt the list update — the picker froze on
                   its pre-extras snapshot and would not reopen. -->
              {#each group.models as model (model.name + '|' + model.value)}
                <ModelPill
                  label={model.name}
                  value={model.value}
                  isAdded={addedModelValues.includes(model.value)}
                  vision={model.caps.vision}
                  reasoning={model.caps.reasoning}
                  decision={model.caps.decision}
                  deprecated={model.deprecated}
                  credit={model.credit}
                  free={model.free}
                  onToggle={() => handleToggleModel(model)}
                />
              {/each}
            </div>
          </div>
        {/each}

        {#if (filteredCombos.length === 0 || activeFilter === 'vision' || activeFilter === 'reasoning' || activeFilter === 'decision') && displayGroups.length === 0}
          <div class="text-center py-4 text-text-muted">
            <Search class="w-6 h-6 mx-auto mb-1 opacity-50" />
            <p class="text-xs">No models found</p>
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}
