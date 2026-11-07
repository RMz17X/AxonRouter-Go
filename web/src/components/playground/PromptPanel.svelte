<script lang="ts">
  import type { JailbreakDoc } from '../../api/client'
  import Toggle from '../../lib/ui/Toggle.svelte'

  interface Props {
    /** Session system prompt typed by the operator (highest precedence). */
    systemPrompt?: string
    /** Persona library presets for the preset picker. */
    presets: JailbreakDoc[]
    /** Chosen preset id, '' for none. */
    presetId?: string
    /** The selected model's standing Persona preset name, or null when the
     * model has no standing configuration. */
    standingPresetName?: string | null
    /** Whether the standing configuration applies (default on). */
    applyStandingPersona?: boolean
  }

  let {
    systemPrompt = $bindable(''),
    presets,
    presetId = $bindable(''),
    standingPresetName = null,
    applyStandingPersona = $bindable(true),
  }: Props = $props()

  const sessionActive = $derived(systemPrompt.trim() !== '')
  const presetActive = $derived(!sessionActive && presetId !== '')
</script>

<div class="flex flex-col gap-3">
  <div class="flex flex-col gap-2">
    <label for="playground-system-prompt" class="text-xs font-medium text-text-muted">
      System prompt
    </label>
    <textarea
      id="playground-system-prompt"
      bind:value={systemPrompt}
      rows="5"
      placeholder="You are an expert assistant..."
      class="w-full px-3 py-2 rounded-brand bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500 resize-y"
    ></textarea>
  </div>

  <div class="flex flex-col gap-2">
    <label for="playground-preset" class="text-xs font-medium text-text-muted">Persona preset</label>
    <select
      id="playground-preset"
      bind:value={presetId}
      class="w-full rounded-brand border border-border bg-surface px-3 py-2 text-sm text-text-main focus:outline-none focus:ring-2 focus:ring-brand-500/40 cursor-pointer"
    >
      <option value="">No preset</option>
      {#each presets as preset (preset.id)}
        <option value={preset.id}>{preset.name || preset.id}</option>
      {/each}
    </select>
  </div>

  <div class="flex items-center justify-between gap-3">
    <div class="flex flex-col">
      <span class="text-xs font-medium text-text-muted">Standing Persona</span>
      <span class="text-xs text-text-subtle">
        {#if standingPresetName}
          {standingPresetName} is bound to this model
        {:else}
          No standing Persona for this model
        {/if}
      </span>
    </div>
    <Toggle
      checked={applyStandingPersona}
      disabled={!standingPresetName}
      label="Apply standing Persona"
      title="Apply the model's standing Persona configuration, as real traffic would get"
      onChange={(v) => (applyStandingPersona = v)}
    />
  </div>

  {#if sessionActive}
    <p class="text-xs text-text-subtle">
      The session prompt overrides the preset and the standing Persona for this chat.
    </p>
  {:else if presetActive}
    <p class="text-xs text-text-subtle">The preset overrides the standing Persona for this chat.</p>
  {/if}
</div>
