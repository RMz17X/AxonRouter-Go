<script lang="ts">
  import { onMount } from 'svelte'
  import {
    api,
    type Combo,
    type JailbreakDoc,
    type JailbreakModelConfig,
    type ProviderConnection,
  } from '../../api/client'
  import { matchesAnyJailbreakKeyword } from '../../lib/jailbreakKeywords'
  import Button from '../../lib/ui/Button.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import ModelPickerModal from '../combos/ModelPickerModal.svelte'
  import BulkArm from './BulkArm.svelte'

  interface Props {
    presets: JailbreakDoc[]
    /** When set, the modal edits this binding instead of adding a new one. */
    editTarget: { id: string; cfg: JailbreakModelConfig } | null
    onClose: () => void
    /** Reload presets + bindings + usage after a successful bind. */
    onSaved: () => Promise<void> | void
  }

  let { presets, editTarget, onClose, onSaved }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // The modal mounts fresh per open, so the edit target is captured once,
  // on purpose: later prop changes must not rewrite a form in use.
  // svelte-ignore state_referenced_locally
  const bindingEditId = editTarget?.id ?? ''
  // Editing one existing binding reuses the Single tab: the target is shown
  // read-only and the on/off checkbox appears, which is the whole difference.
  // A second modal for the same two controls was the alternative, and it
  // duplicated the combobox, the outside-click handler, and the footer.
  // svelte-ignore state_referenced_locally
  let bindModelId = $state(editTarget?.id ?? '')
  // svelte-ignore state_referenced_locally
  let bindPresetId = $state(editTarget?.cfg.presetId ?? '')
  // svelte-ignore state_referenced_locally
  let bindingEditEnabled = $state(editTarget?.cfg.enabled ?? true)
  let bindError = $state('')
  let bindSaving = $state(false)

  // Add binding: one modal with two tabs — a single id picked from the live
  // inventory, or many ids picked from the live catalog (BulkArm).
  let bindModalTab = $state<'single' | 'many'>('single')

  // Single-bind preset combobox: a keyword search over name and id, so a
  // 50-preset library stays navigable without scrolling a <select>.
  let bindPresetSearch = $state('')
  let bindPresetDropdownOpen = $state(false)
  let bindPresetOptions = $derived.by(() => {
    const q = bindPresetSearch.trim().toLowerCase()
    if (!q) return presets.slice(0, 50)
    return presets
      .filter((p) => (p.name || '').toLowerCase().includes(q) || (p.id || '').toLowerCase().includes(q))
      .slice(0, 50)
  })
  // The label of whatever is currently bound, so reopening the modal shows the
  // selection instead of an empty field.
  let bindPresetLabel = $derived.by(() => {
    if (!bindPresetId) return 'Default document'
    const p = presets.find((x) => x.id === bindPresetId)
    return p ? p.name || p.id || bindPresetId : bindPresetId
  })

  // A picked binding id that a preset keyword already covers needs no binding.
  let bindHint = $derived.by(() => {
    const id = bindModelId.trim()
    if (!id) return ''
    for (const preset of presets) {
      if (preset.keywords && matchesAnyJailbreakKeyword(id, preset.keywords)) {
        return `Keyword in "${preset.name || preset.id}" already covers this id — binding is optional.`
      }
    }
    return ''
  })

  // The target is picked with the Combos flow's own picker (target 'combo',
  // the value whose resolveFilteredCombos keeps combos pickable), fed with
  // the live connections and combos exactly as CombosView passes them. The
  // modal mounts fresh per open, so mounting is the fetch trigger. Both
  // fetches are cosmetic-grade — the same posture as JailbreakView's chip
  // data: a failure leaves the picker with what did load and the type chip
  // reading MODEL, never a blocked form.
  let pickerConnections = $state<ProviderConnection[]>([])
  let pickerCombos = $state<Combo[]>([])
  let showTargetPicker = $state(false)
  onMount(() => {
    void (async () => {
      try {
        pickerConnections = await api.getConnections()
      } catch {}
      try {
        pickerCombos = await api.getCombos()
      } catch {}
    })()
  })

  // The MODEL/COMBO chip on the target field: the same derivation
  // BindingList's rows use — the id is a combo when its name is one.
  let targetIsCombo = $derived(
    bindModelId !== '' && pickerCombos.some((c) => c.name === bindModelId)
  )

  // Single-select over a multi-select picker: a pick is the whole answer,
  // so it sets the target and closes. bindModelId is only ever written here
  // and by the initial edit capture — opening the picker cannot clear it,
  // and a target the live inventory no longer offers (deactivated
  // connection, deleted combo, legacy id) still displays and saves as-is.
  function pickTarget(value: string) {
    bindModelId = value
    showTargetPicker = false
  }

  // Close the single-bind preset combobox on a click anywhere outside it —
  // modal padding, the title bar, the tab strip, the page behind the overlay.
  // A document listener rather than a wrapper onclick, because the wrapper
  // only sees clicks that bubble through the panel, and a click on the modal's
  // own padding or title never does. A blur handler is not an option: blur
  // fires before the option's click and would cancel the pick.
  $effect(() => {
    if (!bindPresetDropdownOpen) return
    const onDocClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement | null
      if (target?.closest('[data-preset-combobox]')) return
      bindPresetDropdownOpen = false
    }
    // capture: the overlay's own click-to-close handler runs on the same
    // event, and in the bubble phase it may already have unmounted this
    // component before the listener is removed.
    document.addEventListener('click', onDocClick, true)
    return () => document.removeEventListener('click', onDocClick, true)
  })

  async function bindModel() {
    const id = bindModelId.trim()
    if (!id) {
      bindError = 'Pick a model or combo to bind.'
      return
    }
    bindError = ''
    // Editing keeps the id and honours the on/off checkbox; creating a new
    // binding always switches it on.
    const enabled = bindingEditId ? bindingEditEnabled : true
    bindSaving = true
    try {
      await api.setJailbreakModel(id, bindPresetId, enabled)
      await onSaved()
      // The list is the proof the binding landed, so the modal closes on
      // success and stays open on failure with the reason in view.
      onClose()
    } catch (e) {
      bindError = errorMessage(e)
    } finally {
      bindSaving = false
    }
  }
</script>

<Modal isOpen={true} onClose={onClose} title={bindingEditId ? 'Edit model binding' : 'Add model bindings'} size="xl" icon={bindingEditId ? 'edit' : 'link'}>
  {#if !bindingEditId}
    <!-- Two tabs: pick one id from the live inventory, or pick many from the
         live catalog. Single opens first — it is the common case. Editing an
         existing binding always lands on Single, so the strip is hidden then. -->
    <div class="mb-5 flex gap-1 rounded-brand-lg border border-border-subtle bg-surface-2 p-1" role="tablist" aria-label="Binding mode">
      <button
        type="button"
        role="tab"
        aria-selected={bindModalTab === 'single'}
        onclick={() => (bindModalTab = 'single')}
        class="flex-1 rounded-brand px-3 py-1.5 text-xs font-medium transition-colors {bindModalTab === 'single'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Single
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={bindModalTab === 'many'}
        onclick={() => (bindModalTab = 'many')}
        class="flex-1 rounded-brand px-3 py-1.5 text-xs font-medium transition-colors {bindModalTab === 'many'
          ? 'bg-brand-500 text-white shadow-sm'
          : 'text-text-muted hover:text-text-main'}"
      >
        Bulk
      </button>
    </div>
  {/if}

  {#if bindModalTab === 'single'}
    <div class="space-y-4">
      <p class="text-xs text-text-subtle">
        {bindingEditId
          ? 'The id is what the client sends, so it cannot change here. Unbind and re-add to bind a different id.'
          : 'Pick the model or combo to bind, then pick the preset it resolves to.'}
      </p>
      <div class="space-y-1.5">
        <span class="text-xs text-text-muted">Model or combo</span>
        <div class="flex items-center gap-2">
          <!-- The target is display state, not an inventory lookup: a target
               the live inventory no longer offers still renders here and
               saves unchanged. Editing never offers the picker — the id is
               immutable, as the note above says. -->
          <div class="flex min-w-0 flex-1 items-center gap-2 rounded-brand border border-border-subtle bg-surface-2 px-3 py-2">
            {#if bindModelId}
              {#if targetIsCombo}
                <span class="shrink-0 rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-brand-500">COMBO</span>
              {:else}
                <span class="shrink-0 rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">MODEL</span>
              {/if}
              <span class="truncate font-code text-sm text-text-main" title={bindModelId}>{bindModelId}</span>
            {:else}
              <span class="text-sm text-text-subtle">No model picked yet</span>
            {/if}
          </div>
          {#if !bindingEditId}
            <Button variant="secondary" size="md" onclick={() => (showTargetPicker = true)}>Pick model…</Button>
          {/if}
        </div>
      </div>
      <label class="block space-y-1.5">
        <span class="text-xs text-text-muted">Preset</span>
        <div class="relative" data-preset-combobox>
          <input
            type="text"
            value={bindPresetDropdownOpen ? bindPresetSearch : bindPresetLabel}
            onfocus={() => {
              bindPresetSearch = ''
              bindPresetDropdownOpen = true
            }}
            oninput={(e) => {
              bindPresetSearch = e.currentTarget.value
              bindPresetDropdownOpen = true
            }}
            placeholder="Search presets…"
            aria-label="Search presets"
            class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none placeholder:text-text-subtle focus:border-brand-500"
          />
          {#if bindPresetDropdownOpen}
            <ul class="absolute z-10 mt-1 max-h-48 w-full divide-y divide-border overflow-y-auto rounded-brand border border-border-subtle bg-surface shadow-lg">
              <li>
                <button
                  type="button"
                  onclick={() => {
                    bindPresetId = ''
                    bindPresetSearch = ''
                    bindPresetDropdownOpen = false
                  }}
                  class="w-full px-3 py-2 text-left text-sm text-text-main hover:bg-surface-2 {bindPresetId === '' ? 'font-medium text-brand-500' : ''}"
                >
                  Default document
                </button>
              </li>
              {#each bindPresetOptions as preset (preset.id ?? preset.name)}
                <li>
                  <button
                    type="button"
                    onclick={() => {
                      bindPresetId = preset.id ?? ''
                      bindPresetSearch = ''
                      bindPresetDropdownOpen = false
                    }}
                    class="w-full truncate px-3 py-2 text-left text-sm text-text-main hover:bg-surface-2 {bindPresetId === (preset.id ?? '')
                      ? 'font-medium text-brand-500'
                      : ''}"
                  >
                    {preset.name || preset.id}
                    {#if preset.kind}<span class="ml-1.5 text-[10px] text-text-subtle uppercase">{preset.kind}</span>{/if}
                  </button>
                </li>
              {/each}
              {#if bindPresetOptions.length === 0}
                <li class="px-3 py-2 text-xs text-text-subtle">No preset matches.</li>
              {/if}
            </ul>
          {/if}
        </div>
      </label>
      {#if bindError}<p class="text-xs text-error">{bindError}</p>{/if}
      {#if bindHint}<p class="text-xs text-text-muted">{bindHint}</p>{/if}
      {#if bindingEditId}
        <label class="flex cursor-pointer items-center gap-2 select-none">
          <input type="checkbox" bind:checked={bindingEditEnabled} class="h-4 w-4 accent-brand-500" />
          <span class="text-sm text-text-main">Inject this preset for the id above</span>
        </label>
      {/if}
      <div class="flex justify-end gap-2 border-t border-border-subtle pt-4">
        <Button variant="secondary" size="md" onclick={onClose}>Cancel</Button>
        <Button variant="primary" size="md" onclick={bindModel} disabled={bindSaving}>
          {bindingEditId ? 'Save' : 'Bind'}
        </Button>
      </div>
    </div>
  {:else}
    <BulkArm {presets} onClose={onClose} onSaved={onSaved} />
  {/if}

  <!-- The Combos flow's picker, reused as-is: target 'combo' keeps combos
       pickable as entries alongside the per-connection models. -->
  <ModelPickerModal
    isOpen={showTargetPicker}
    target="combo"
    connections={pickerConnections}
    combos={pickerCombos}
    addedModelValues={bindModelId ? [bindModelId] : []}
    onSelect={pickTarget}
    onClose={() => (showTargetPicker = false)}
  />
</Modal>
