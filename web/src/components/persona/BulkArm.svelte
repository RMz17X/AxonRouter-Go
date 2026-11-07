<script lang="ts">
  import { api, type JailbreakDoc } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'

  interface Props {
    presets: JailbreakDoc[]
    onClose: () => void
    /** Reload presets + bindings + usage after the bulk bind lands. */
    onSaved: () => Promise<void> | void
  }

  let { presets, onClose, onSaved }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // #6 Bulk arm: pick several ids from the live catalog, bind them at once.
  let bulkArmSearch = $state('')
  let bulkArmModels = $state<string[]>([])
  let bulkArmModelsLoading = $state(false)
  let bulkArmPicked = $state<Set<string>>(new Set())
  let bulkArmPresetId = $state('')
  let bulkArmPresetSearch = $state('')
  let bulkArmPresetDropdownOpen = $state(false)
  let bulkArmSaving = $state(false)
  let bulkArmStatus = $state('')

  // Load the live model catalog once. This panel only mounts while the Bulk
  // tab is visible, so mounting is the reliable trigger — the operator can
  // reach Bulk from Single without reopening the modal.
  async function openBulkArm() {
    bulkArmStatus = ''
    bulkArmPicked = new Set()
    if (bulkArmModels.length > 0 || bulkArmModelsLoading) return
    bulkArmModelsLoading = true
    try {
      const res = await api.getJailbreakModelCatalog()
      bulkArmModels = Array.isArray(res.models) ? res.models : []
    } catch (e) {
      bulkArmStatus = 'Failed to load models: ' + errorMessage(e)
    } finally {
      bulkArmModelsLoading = false
    }
  }
  $effect(() => {
    void openBulkArm()
  })

  function toggleBulkArmPick(id: string) {
    const next = new Set(bulkArmPicked)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    bulkArmPicked = next
  }
  let bulkArmPresetOptions = $derived.by(() => {
    const q = bulkArmPresetSearch.trim().toLowerCase()
    if (!q) return presets.slice(0, 50)
    return presets
      .filter((p) => (p.name || '').toLowerCase().includes(q) || (p.id || '').toLowerCase().includes(q))
      .slice(0, 50)
  })
  // Bulk-arm filter: plain substring by default, or an RE2 pattern when the
  // query starts with "/" — the same convention the keyword rules use, so an
  // operator who already writes `^deepseek` there does not relearn it here.
  // The regex validity check runs as its own derived, and the filter reads it.
  // Writing bulkArmRegexError from inside bulkArmFiltered used to be the shape
  // here, and it silently broke the filter: a $derived.by that writes to
  // another $state is not re-tracked, so the list never recomputed — the input
  // accepted text, the count stayed at 200, and nothing filtered.
  let bulkArmRegexValid = $derived.by(() => {
    const q = bulkArmSearch.trim()
    if (!q.startsWith('/')) return true
    try {
      new RegExp(q.slice(1), 'i')
      return true
    } catch {
      return false
    }
  })
  // The message shown under the search box. Derived rather than assigned, so
  // nothing writes to state while a value is being computed.
  let bulkArmRegexError = $derived.by(() => {
    const q = bulkArmSearch.trim()
    if (!q.startsWith('/') || bulkArmRegexValid) return ''
    try {
      new RegExp(q.slice(1), 'i')
      return ''
    } catch (e) {
      return `Regex invalid: ${e instanceof Error ? e.message : String(e)}`
    }
  })
  let bulkArmFiltered = $derived.by(() => {
    const q = bulkArmSearch.trim()
    if (!q) return bulkArmModels.slice(0, 200)
    if (q.startsWith('/')) {
      if (!bulkArmRegexValid) return []
      const re = new RegExp(q.slice(1), 'i')
      return bulkArmModels.filter((m) => re.test(m)).slice(0, 200)
    }
    const lower = q.toLowerCase()
    return bulkArmModels.filter((m) => m.toLowerCase().includes(lower)).slice(0, 200)
  })
  // Select every id the current filter matches — the regex/substring query is
  // the selector, so "arm everything matching /^deepseek/" is one click.
  function selectAllBulkArmFiltered() {
    const next = new Set(bulkArmPicked)
    for (const id of bulkArmFiltered) next.add(id)
    bulkArmPicked = next
  }
  async function saveBulkArm() {
    const ids = Array.from(bulkArmPicked)
    if (ids.length === 0) {
      bulkArmStatus = 'Pick at least one model id.'
      return
    }
    bulkArmSaving = true
    bulkArmStatus = ''
    let ok = 0
    const failures: string[] = []
    for (const id of ids) {
      try {
        await api.setJailbreakModel(id, bulkArmPresetId || 'default', true)
        ok++
      } catch {
        failures.push(id)
      }
    }
    bulkArmStatus =
      failures.length === 0
        ? `Armed ${ok} model(s).`
        : `Armed ${ok}, failed ${failures.length}: ${failures.slice(0, 3).join(', ')}`
    bulkArmSaving = false
    if (ok > 0) {
      bulkArmPicked = new Set()
      await onSaved()
      onClose()
    }
  }
</script>

<div class="space-y-4">
  <p class="text-xs text-text-subtle">
    Pick model ids from the live catalog, then choose the preset below. Every picked id is bound to it.
  </p>

  <!-- Search: plain substring, or an RE2 pattern when the query starts with "/" -->
  <div class="space-y-1.5">
    <input
      type="text"
      bind:value={bulkArmSearch}
      placeholder="Filter models…  (/^deepseek for regex)"
      aria-label="Filter models"
      class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 font-code text-sm text-text-main placeholder:text-text-subtle"
    />
    <p class="text-[11px] text-text-subtle">
      Substring by default. Start with <span class="font-code text-text-muted">/</span> for an RE2 regex,
      matched case-insensitively — e.g. <span class="font-code text-text-muted">/^deepseek</span> or
      <span class="font-code text-text-muted">/(kimi|glm)-\d</span>.
    </p>
    {#if bulkArmRegexError}
      <p class="text-[11px] text-error">{bulkArmRegexError}</p>
    {/if}
  </div>

  {#if bulkArmModelsLoading}
    <p class="text-xs text-text-muted">Loading catalog…</p>
  {:else if bulkArmModels.length === 0}
    <div class="space-y-2">
      <p class="text-xs text-text-subtle">
        {bulkArmStatus
          ? 'The live catalog could not be loaded, so there is nothing to pick from yet.'
          : 'No models available.'}
      </p>
      <Button variant="secondary" size="sm" onclick={openBulkArm}>
        {bulkArmStatus ? 'Retry' : 'Reload catalog'}
      </Button>
    </div>
  {:else}
    <!-- Separator closes the search block, so the result count and Select
         all read as the header of the list below rather than part of the
         search hint. -->
    <div class="flex items-center justify-between gap-2 border-t border-border-subtle pt-3">
      <span class="text-[11px] text-text-subtle">
        {bulkArmFiltered.length} match{bulkArmFiltered.length === 1 ? '' : 'es'}
        {#if bulkArmModels.length > 200} (first 200 shown){/if}
      </span>
      <button
        type="button"
        onclick={selectAllBulkArmFiltered}
        disabled={bulkArmFiltered.length === 0}
        class="text-xs text-brand-500 hover:underline disabled:opacity-40"
      >
        Select all matches
      </button>
    </div>
    <ul class="max-h-[40vh] divide-y divide-border overflow-y-auto rounded-brand border border-border-subtle">
      {#each bulkArmFiltered as id (id)}
        <li>
          <label class="flex cursor-pointer items-center gap-2 px-3 py-2 hover:bg-surface-2">
            <input
              type="checkbox"
              checked={bulkArmPicked.has(id)}
              onchange={() => toggleBulkArmPick(id)}
              class="h-4 w-4 accent-brand-500"
            />
            <span class="truncate font-code text-sm text-text-main">{id}</span>
          </label>
        </li>
      {/each}
    </ul>
  {/if}

  <!-- Preset picker sits below the results: the operator picks the models
       first, then decides what they become. It is a searchable combobox so
       a 50-preset library stays navigable. -->
  <div class="space-y-2 border-t border-border-subtle pt-4">
    <span class="text-xs text-text-muted">Bind the picked ids to…</span>
    <div class="relative">
      <input
        type="text"
        bind:value={bulkArmPresetSearch}
        placeholder="Search presets…"
        aria-label="Search presets"
        onfocus={() => (bulkArmPresetDropdownOpen = true)}
        class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none placeholder:text-text-subtle focus:border-brand-500"
      />
      {#if bulkArmPresetDropdownOpen}
        <ul class="absolute z-10 mt-1 max-h-48 w-full divide-y divide-border overflow-y-auto rounded-brand border border-border-subtle bg-surface shadow-lg">
          <li>
            <button
              type="button"
              onclick={() => {
                bulkArmPresetId = ''
                bulkArmPresetSearch = 'Default document'
                bulkArmPresetDropdownOpen = false
              }}
              class="w-full px-3 py-2 text-left text-sm text-text-main hover:bg-surface-2"
            >
              Default document
            </button>
          </li>
          {#each bulkArmPresetOptions as preset (preset.id ?? preset.name)}
            <li>
              <button
                type="button"
                onclick={() => {
                  bulkArmPresetId = preset.id ?? ''
                  bulkArmPresetSearch = preset.name || preset.id || ''
                  bulkArmPresetDropdownOpen = false
                }}
                class="w-full truncate px-3 py-2 text-left text-sm text-text-main hover:bg-surface-2"
              >
                {preset.name || preset.id}
                {#if preset.kind}<span class="ml-1.5 text-[10px] text-text-subtle uppercase">{preset.kind}</span>{/if}
              </button>
            </li>
          {/each}
          {#if bulkArmPresetOptions.length === 0}
            <li class="px-3 py-2 text-xs text-text-subtle">No preset matches.</li>
          {/if}
        </ul>
      {/if}
    </div>
  </div>

  {#if bulkArmStatus}<p class="text-xs text-text-muted">{bulkArmStatus}</p>{/if}
  <div class="flex items-center justify-between gap-2 border-t border-border-subtle pt-4">
    <span class="text-xs text-text-subtle">{bulkArmPicked.size} picked</span>
    <div class="flex gap-2">
      <Button variant="secondary" size="md" onclick={onClose}>Cancel</Button>
      <Button variant="primary" size="md" onclick={saveBulkArm} disabled={bulkArmSaving || bulkArmPicked.size === 0}>
        {bulkArmSaving ? 'Binding…' : 'Bulk Bind'}
      </Button>
    </div>
  </div>
</div>
