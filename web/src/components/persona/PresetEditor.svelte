<script lang="ts">
  import { api, type JailbreakDoc } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import KeywordRules from '../KeywordRules.svelte'
  import PresetImport from './PresetImport.svelte'
  import PromptField from './PromptField.svelte'
  import { emptyPresetForm, presetToForm, type PresetFormState } from './types'

  interface Props {
    /** The preset being edited, or null to create a new one. */
    editing: JailbreakDoc | null
    onClose: () => void
    /** Reload presets + bindings + usage after a save or import lands. */
    onSaved: () => Promise<void> | void
  }

  let { editing, onClose, onSaved }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // The modal mounts fresh per open, so the edited preset is captured once,
  // on purpose: later prop changes must not rewrite a form in use.
  // svelte-ignore state_referenced_locally
  const editingId = editing?.id || null
  // svelte-ignore state_referenced_locally
  let presetForm = $state<PresetFormState>(editing ? presetToForm(editing) : emptyPresetForm())
  let presetSaving = $state(false)
  let presetError = $state('')

  // The preset modal carries three tabs: writing one preset by hand, or
  // filling many at once from files or URLs. Editing an existing preset is
  // always Write — the import paths create, they never modify.
  type PresetModalTab = 'write' | 'files' | 'source'
  let presetModalTab = $state<PresetModalTab>('write')

  async function savePreset() {
    if (!presetForm.name.trim()) {
      presetError = 'Name is required.'
      return
    }
    if (presetForm.mode === 'chat') {
      const activeTurns = presetForm.turns.filter((t) => t.content.trim())
      if (activeTurns.length === 0) {
        presetError = 'At least one chat turn with content is required in Multi-turn Script mode.'
        return
      }
    } else {
      if (!presetForm.prompt.trim()) {
        presetError = 'Prompt is required in System Prompt mode.'
        return
      }
    }
    presetSaving = true
    presetError = ''
    try {
      await api.saveJailbreakPreset({
        id: presetForm.id.trim() || undefined,
        name: presetForm.name.trim(),
        prompt: presetForm.prompt,
        geminiSafety: presetForm.geminiSafety,
        mode: presetForm.mode,
        position: presetForm.position,
        turns: presetForm.mode === 'chat' ? presetForm.turns.filter((t) => t.content.trim()) : undefined,
        kind: presetForm.kind.trim() || undefined,
        source: presetForm.source.trim() || undefined,
        keywords: presetForm.keywords,
        excludes: presetForm.excludes,
        order: presetForm.order || undefined,
      })
      await onSaved()
      onClose()
    } catch (e) {
      presetError = errorMessage(e)
    } finally {
      presetSaving = false
    }
  }
</script>

<Modal
  isOpen={true}
  onClose={onClose}
  title={editingId
    ? 'Edit preset'
    : presetModalTab === 'files'
      ? 'New preset from files'
      : presetModalTab === 'source'
        ? 'New preset from URLs'
        : 'New preset'}
  size="lg"
  icon={editingId ? 'edit' : presetModalTab === 'files' ? 'upload_file' : presetModalTab === 'source' ? 'cloud_download' : 'note_add'}
>
  {#if !editingId}
    <!-- Three tabs. Write builds one preset from its fields; the other two
         fill many presets at once and have no fields to fill, only a source
         and a result log. -->
    <div class="mb-5 grid grid-cols-3 gap-2" role="tablist" aria-label="Preset source">
      <button
        type="button"
        role="tab"
        aria-selected={presetModalTab === 'write'}
        onclick={() => (presetModalTab = 'write')}
        class="flex items-center justify-center gap-1.5 rounded-brand border px-3 py-2 text-xs font-medium transition-colors {presetModalTab === 'write'
          ? 'border-brand-500 bg-brand-500/10 text-text-main'
          : 'border-transparent text-text-muted hover:border-border hover:text-text-main'}"
      >
        <span class="material-symbols-outlined text-[16px]">edit</span>
        Write
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={presetModalTab === 'files'}
        onclick={() => (presetModalTab = 'files')}
        class="flex items-center justify-center gap-1.5 rounded-brand border px-3 py-2 text-xs font-medium transition-colors {presetModalTab === 'files'
          ? 'border-brand-500 bg-brand-500/10 text-text-main'
          : 'border-transparent text-text-muted hover:border-border hover:text-text-main'}"
      >
        <span class="material-symbols-outlined text-[16px]">upload_file</span>
        From files
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={presetModalTab === 'source'}
        onclick={() => (presetModalTab = 'source')}
        class="flex items-center justify-center gap-1.5 rounded-brand border px-3 py-2 text-xs font-medium transition-colors {presetModalTab === 'source'
          ? 'border-brand-500 bg-brand-500/10 text-text-main'
          : 'border-transparent text-text-muted hover:border-border hover:text-text-main'}"
      >
        <span class="material-symbols-outlined text-[16px]">cloud_download</span>
        From URL
      </button>
    </div>
  {/if}

  {#if editingId || presetModalTab === 'write'}
    <div class="space-y-5">
      <!-- Section: Prompt — identity, injection mode, and the prompt itself. -->
      <div class="space-y-4">
        <div class="text-[10px] font-semibold tracking-wider text-text-subtle uppercase">Prompt</div>
        <label class="block space-y-1.5">
          <span class="text-xs text-text-muted">ID</span>
          <input
            type="text"
            bind:value={presetForm.id}
            disabled={!!editingId}
            placeholder="expert-coder"
            class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none focus:border-brand-500 disabled:opacity-50"
          />
          {#if editingId}
            <span class="text-[11px] text-text-subtle">
              The id is what bindings and keyword rules already point at, so it cannot change here.
            </span>
          {/if}
        </label>
        <label class="block space-y-1.5">
          <span class="text-xs text-text-muted">Name</span>
          <input
            type="text"
            bind:value={presetForm.name}
            placeholder="Expert Coder"
            class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none focus:border-brand-500"
          />
        </label>
        <PromptField form={presetForm} />
      </div>

      <!-- Section: Behavior — provenance metadata and the safety switch. -->
      <div class="space-y-4 border-t border-border-subtle pt-5">
        <div class="text-[10px] font-semibold tracking-wider text-text-subtle uppercase">Behavior</div>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block space-y-1.5">
            <span class="text-xs text-text-muted">Source file name</span>
            <input
              type="text"
              bind:value={presetForm.source}
              placeholder="expert.md"
              class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none focus:border-brand-500"
            />
          </label>
          <label class="block space-y-1.5">
            <span class="text-xs text-text-muted">Kind</span>
            <input
              type="text"
              bind:value={presetForm.kind}
              list="jailbreak-kinds"
              placeholder="SOUL, PERSONA, AGENTS, LOAD…"
              class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none focus:border-brand-500"
            />
          </label>
        </div>
        <p class="text-[11px] text-text-subtle">
          Kind is the instruction-file convention this preset came from — filled in automatically on import
          from a name such as SOUL.md or AGENTS.md; leave empty for a prompt about a model.
        </p>
        <datalist id="jailbreak-kinds">
          {#each ['AGENTS', 'CLAUDE', 'GEMINI', 'CURSORRULES', 'WINDSURF', 'COPILOT', 'SOUL', 'PERSONA', 'IDENTITY', 'CHARACTER', 'LOAD', 'SYSTEM', 'RULES', 'POLICY', 'MODELFILE', 'CONFIG'] as kind (kind)}
            <option value={kind}></option>
          {/each}
        </datalist>
        <label class="flex cursor-pointer items-center gap-2 select-none">
          <input type="checkbox" bind:checked={presetForm.geminiSafety} class="h-4 w-4 accent-brand-500" />
          <span class="text-sm text-text-main">Relax Gemini safety filters</span>
        </label>
      </div>

      <!-- Section: Keywords — who this preset covers, and who it never touches. -->
      <div class="space-y-4 border-t border-border-subtle pt-5">
        <div class="text-[10px] font-semibold tracking-wider text-text-subtle uppercase">Keywords</div>
        <KeywordRules
          label="Keywords (include)"
          hint="Every model id matching one of these rules resolves to this preset — across providers and combo legs."
          rules={presetForm.keywords}
          onchange={(next) => (presetForm.keywords = next)}
        />
        <KeywordRules
          label="Excludes (deny)"
          hint="Models matching one of these rules never get this preset, even when a keyword above matches."
          rules={presetForm.excludes}
          onchange={(next) => (presetForm.excludes = next)}
        />
      </div>

      {#if presetError}<p class="text-xs text-error">{presetError}</p>{/if}
      <div class="flex justify-end gap-2 border-t border-border-subtle pt-4">
        <Button variant="secondary" size="md" onclick={onClose}>Cancel</Button>
        <Button variant="primary" size="md" onclick={savePreset} disabled={presetSaving}>{presetSaving ? 'Saving…' : 'Save'}</Button>
      </div>
    </div>
  {:else}
    <PresetImport tab={presetModalTab} onImported={onSaved} />
  {/if}
</Modal>
