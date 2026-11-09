<script lang="ts">
  import { api, type Combo, type JailbreakDoc, type PersonaDef, type ProviderConnection } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import ModelPickerModal from '../combos/ModelPickerModal.svelte'

  interface Props {
    presets: JailbreakDoc[]
    /** When set, the modal edits this persona instead of minting a new one. */
    editTarget: PersonaDef | null
    /** Known combo names, for the MODEL/COMBO chip on the picked target. */
    comboNames: Set<string>
    onClose: () => void
    /** Reload presets + personas after a successful save. */
    onSaved: () => Promise<void> | void
  }

  let { presets, editTarget, comboNames, onClose, onSaved }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // Draft state is seeded once per mount; the parent mounts the modal
  // fresh per open, so the seed always matches the persona handed in.
  // svelte-ignore state_referenced_locally
  let name = $state(editTarget?.name ?? '')
  // svelte-ignore state_referenced_locally
  let target = $state(editTarget?.target ?? '')
  // svelte-ignore state_referenced_locally
  let presetId = $state(editTarget?.presetId ?? '')
  let error = $state('')
  let saving = $state(false)

  const editing = $derived(editTarget !== null)
  const targetIsCombo = $derived(target !== '' && comboNames.has(target))

  // Target picker: the Combos flow's picker, reused as-is — target
  // 'combo' keeps combos pickable as entries alongside per-connection
  // models. Its inventory loads lazily on first open.
  let showTargetPicker = $state(false)
  let pickerConnections = $state<ProviderConnection[]>([])
  let pickerCombos = $state<Combo[]>([])
  let pickerLoaded = $state(false)
  $effect(() => {
    if (showTargetPicker && !pickerLoaded) {
      pickerLoaded = true
      void (async () => {
        try {
          pickerConnections = await api.getConnections()
        } catch {}
        try {
          pickerCombos = await api.getCombos()
        } catch {}
      })()
    }
  })
  function pickTarget(value: string) {
    target = value
    showTargetPicker = false
  }

  // Preset combobox: a keyword search over name and id, so a large
  // library stays navigable without scrolling a <select>.
  let presetSearch = $state('')
  let presetDropdownOpen = $state(false)
  let presetOptions = $derived.by(() => {
    const q = presetSearch.trim().toLowerCase()
    if (!q) return presets.slice(0, 50)
    return presets
      .filter((p) => (p.name || '').toLowerCase().includes(q) || (p.id || '').toLowerCase().includes(q))
      .slice(0, 50)
  })
  let presetLabel = $derived.by(() => {
    if (!presetId) return ''
    const p = presets.find((x) => x.id === presetId)
    return p ? p.name || p.id || presetId : presetId
  })

  async function save() {
    error = ''
    if (!name.trim()) {
      error = 'Give the persona model a name.'
      return
    }
    if (!target) {
      error = 'Pick the model or combo this persona runs on.'
      return
    }
    if (!presetId) {
      error = 'Pick the preset this persona injects.'
      return
    }
    saving = true
    try {
      await api.savePersona({ name: name.trim(), target, presetId })
      await onSaved()
      onClose()
    } catch (e) {
      error = errorMessage(e)
    } finally {
      saving = false
    }
  }
</script>

<Modal isOpen={true} {onClose} title={editing ? 'Edit persona model' : 'New persona model'} size="xl" icon={editing ? 'edit' : 'person_add'}>
  <div class="space-y-4">
    <p class="text-xs text-text-subtle">
      A persona model is a <span class="text-text-main">new model name of your own</span> that runs the
      target you pick, with the chosen preset always injected. Clients call the persona name exactly
      like a model; the name must differ from every real model, combo, and alias.
    </p>

    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">Persona model name</span>
      <input
        type="text"
        bind:value={name}
        readonly={editing}
        placeholder="e.g. aira-coding"
        class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 font-code text-sm text-text-main outline-none placeholder:text-text-subtle focus:border-brand-500 {editing
          ? 'opacity-60'
          : ''}"
      />
      {#if editing}
        <span class="block text-[11px] text-text-subtle">The name is the model's identity, so it cannot change here. Delete and re-create to rename.</span>
      {/if}
    </label>

    <div class="space-y-1.5">
      <span class="text-xs text-text-muted">Runs on</span>
      <div class="flex items-center gap-2">
        <div class="flex min-w-0 flex-1 items-center gap-2 rounded-brand border border-border-subtle bg-surface-2 px-3 py-2">
          {#if target}
            {#if targetIsCombo}
              <span class="shrink-0 rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-brand-500">COMBO</span>
            {:else}
              <span class="shrink-0 rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">MODEL</span>
            {/if}
            <span class="truncate font-code text-sm text-text-main" title={target}>{target}</span>
          {:else}
            <span class="text-sm text-text-subtle">No model or combo picked yet</span>
          {/if}
        </div>
        <Button variant="secondary" size="md" onclick={() => (showTargetPicker = true)}>Pick model…</Button>
      </div>
    </div>

    <label class="block space-y-1.5">
      <span class="text-xs text-text-muted">Preset</span>
      <div class="relative">
        <input
          type="text"
          value={presetDropdownOpen ? presetSearch : presetLabel}
          placeholder="Search presets…"
          aria-label="Search presets"
          onfocus={() => {
            presetSearch = ''
            presetDropdownOpen = true
          }}
          onblur={() => {
            // Delay so a click on an option lands before the list closes.
            setTimeout(() => (presetDropdownOpen = false), 120)
          }}
          oninput={(e) => (presetSearch = e.currentTarget.value)}
          class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none placeholder:text-text-subtle focus:border-brand-500"
        />
        {#if presetDropdownOpen}
          <div class="absolute inset-x-0 top-full z-10 mt-1 max-h-56 overflow-y-auto rounded-brand border border-border-subtle bg-surface shadow-[var(--shadow-elevated)]">
            {#each presetOptions as preset (preset.id)}
              <button
                type="button"
                onclick={() => {
                  presetId = preset.id || ''
                  presetDropdownOpen = false
                }}
                class="w-full truncate px-3 py-2 text-left text-sm text-text-main hover:bg-surface-2 {presetId === (preset.id ?? '')
                  ? 'font-medium text-brand-500'
                  : ''}"
              >
                {preset.name || preset.id}
              </button>
            {:else}
              <p class="px-3 py-2 text-sm text-text-subtle">No presets match.</p>
            {/each}
          </div>
        {/if}
      </div>
    </label>

    {#if error}<p class="text-xs text-error">{error}</p>{/if}

    <div class="flex justify-end gap-2 pt-1">
      <Button variant="ghost" size="md" onclick={onClose}>Cancel</Button>
      <Button variant="primary" size="md" onclick={save} disabled={saving}>{saving ? 'Saving…' : editing ? 'Save changes' : 'Create persona model'}</Button>
    </div>
  </div>

  <!-- The Combos flow's picker, reused as-is: target 'combo' keeps combos
       pickable as entries alongside the per-connection models. -->
  <ModelPickerModal
    isOpen={showTargetPicker}
    target="combo"
    connections={pickerConnections}
    combos={pickerCombos}
    addedModelValues={target ? [target] : []}
    onSelect={pickTarget}
    onClose={() => (showTargetPicker = false)}
  />
</Modal>
