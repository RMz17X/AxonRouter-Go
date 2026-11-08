<script lang="ts">
  import { Brain, Check, Eye } from 'lucide-svelte'

  interface Props {
    label: string
    value: string
    isAdded: boolean
    /** Flattened out of the model's caps so the props compare by value: an
     *  object prop is a fresh identity on every picker rebuild and reads as a
     *  change to all ~1000 pills at once. */
    vision?: boolean
    reasoning?: boolean
    /** One handler for the whole list, so a pill is never handed a fresh
     *  closure per render pass. */
    onToggle: (value: string) => void
    /** Flattened boolean for the same reason as caps: the picker marks a
     *  model the gateway saw refuse with a 410, so the operator knows before
     *  putting it in a combo. */
    deprecated?: boolean
    /** Per-request credit price (CodeBuddy family), resolved server-side
     *  and fetched with the picker extras — never from the static bundle. */
    credit?: number
    /** Server-resolved free flag; wins over the price pill, mirroring the
     *  provider detail grid's FREE badge. */
    free?: boolean
  }

  let {
    label,
    value,
    isAdded,
    vision = false,
    reasoning = false,
    onToggle,
    deprecated = false,
    credit,
    free = false,
  }: Props = $props()

  function handleClick() {
    onToggle(value)
  }
</script>

<button
  type="button"
  {value}
  onclick={handleClick}
  class={`px-2 py-1 rounded-xl text-xs font-medium transition-all border hover:cursor-pointer flex items-center gap-1 ${
    isAdded
      ? 'bg-brand-500 text-white border-brand-500 hover:bg-brand-600'
      : 'bg-surface border-border text-text-main hover:border-brand-500/50 hover:bg-brand-500/5'
  }`}
>
  {#if isAdded}
    <Check class="w-3 h-3 shrink-0" />
  {/if}
  <span class="truncate">{label}</span>
  {#if free}
    <span
      class="inline-flex items-center text-[10px] font-semibold px-1.5 py-0.5 rounded shrink-0 {isAdded
        ? 'bg-white/20 text-white'
        : 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border border-emerald-500/30'}"
      title="Free — no credits charged per request"
    >
      FREE
    </span>
  {:else if typeof credit === 'number'}
    <span
      class="inline-flex items-center text-[10px] font-semibold px-1.5 py-0.5 rounded shrink-0 {isAdded
        ? 'bg-white/20 text-white'
        : 'bg-surface-3 text-text-muted border border-border'}"
      title="Credits per request"
    >
      x{credit.toFixed(2)}
    </span>
  {/if}
  {#if vision}
    <span class="inline-flex shrink-0" title="Vision — Supports image input">
      <Eye class={isAdded ? 'w-3 h-3 text-white/90 shrink-0' : 'w-3 h-3 text-blue-500 shrink-0'} />
    </span>
  {/if}
  {#if reasoning}
    <span class="inline-flex shrink-0" title="Reasoning / Thinking">
      <Brain class={isAdded ? 'w-3 h-3 text-white/90 shrink-0' : 'w-3 h-3 text-amber-500 shrink-0'} />
    </span>
  {/if}
  {#if deprecated}
    <span
      class="material-symbols-outlined text-[13px] leading-none shrink-0 {isAdded ? 'text-white/90' : 'text-warning'}"
      title="Deprecated — this model was retired by the provider and cannot serve a request"
      aria-label="Deprecated"
    >error</span>
  {/if}
</button>