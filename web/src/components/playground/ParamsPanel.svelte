<script lang="ts">
  interface Props {
    temperature?: number
    /** Raw input text; empty means "omit max_tokens" (provider default). */
    maxTokens?: string
  }

  let { temperature = $bindable(1), maxTokens = $bindable('') }: Props = $props()
</script>

<div class="flex flex-col gap-3">
  <div class="flex flex-col gap-2">
    <div class="flex items-center justify-between">
      <label for="playground-temperature" class="text-xs font-medium text-text-muted">
        Temperature
      </label>
      <span class="text-xs text-text-subtle">{temperature.toFixed(1)}</span>
    </div>
    <input
      id="playground-temperature"
      type="range"
      min="0"
      max="2"
      step="0.1"
      bind:value={temperature}
      class="w-full accent-brand-500"
    />
  </div>

  <div class="flex flex-col gap-2">
    <label for="playground-max-tokens" class="text-xs font-medium text-text-muted">Max tokens</label>
    <input
      id="playground-max-tokens"
      type="number"
      min="1"
      step="1"
      bind:value={maxTokens}
      placeholder="Provider default"
      class="w-full px-3 py-2 rounded-brand bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500"
    />
  </div>
</div>
