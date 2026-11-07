<script lang="ts">
  import type { JailbreakDoc } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'

  interface Props {
    presets: JailbreakDoc[]
    busy: boolean
    /** The preset the Rebind action applies to the selected bindings. */
    presetId: string
    onRebind: () => void
    onSetEnabled: (enabled: boolean) => void
    onUnbind: () => void
    onClear: () => void
  }

  let { presets, busy, presetId = $bindable(), onRebind, onSetEnabled, onUnbind, onClear }: Props = $props()
</script>

<!-- Batch menu for the selected bindings: rebind them to one preset, turn
     them on or off, or unbind them outright. -->
<div class="flex flex-wrap items-center gap-2 pt-2">
  <select
    bind:value={presetId}
    aria-label="Preset for the selected bindings"
    class="rounded-brand border border-border-subtle bg-surface-2 px-2 py-1 text-xs text-text-main outline-none"
  >
    <option value="">Choose preset…</option>
    {#each presets as preset (preset.id ?? preset.name)}
      <option value={preset.id ?? ''}>{preset.name || preset.id}</option>
    {/each}
  </select>
  <Button variant="primary" size="sm" onclick={onRebind} disabled={busy || !presetId}>Rebind</Button>
  <Button variant="outline" size="sm" onclick={() => onSetEnabled(true)} disabled={busy}>Turn on</Button>
  <Button variant="outline" size="sm" onclick={() => onSetEnabled(false)} disabled={busy}>Turn off</Button>
  <Button variant="danger" size="sm" onclick={onUnbind} disabled={busy}>
    {busy ? 'Working…' : 'Unbind'}
  </Button>
  <button type="button" onclick={onClear} class="text-xs text-text-subtle hover:underline">Clear</button>
</div>
