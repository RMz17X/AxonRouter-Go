<script lang="ts">
  import type { JailbreakDoc } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import { presetExcerpt } from './types'

  interface Props {
    preset: JailbreakDoc
    /** Injection counter for this preset; undefined until usage loads. */
    usage?: number
    selected: boolean
    /** True while this card's two-step delete is armed. */
    confirmDelete: boolean
    onToggleSelect: (id: string) => void
    onEdit: (preset: JailbreakDoc) => void
    onDuplicate: (id: string) => void
    /** First click arms the confirm, second click (confirmDelete) deletes. */
    onDelete: (id: string) => void
    onCancelDelete: () => void
  }

  let { preset, usage, selected, confirmDelete, onToggleSelect, onEdit, onDuplicate, onDelete, onCancelDelete }: Props =
    $props()

  const presetKey = $derived(preset.id ?? preset.name ?? '')
  const excerpt = $derived(presetExcerpt(preset))
</script>

<Card padding="sm" class="flex h-full flex-col gap-2.5">
  <div class="flex items-start gap-2.5">
    <input
      type="checkbox"
      checked={selected}
      onchange={() => onToggleSelect(presetKey)}
      aria-label={`Select ${preset.name || presetKey}`}
      class="mt-0.5 h-4 w-4 shrink-0 accent-brand-500"
    />
    <div class="min-w-0 flex-1">
      <div class="truncate text-sm font-medium text-text-main">{preset.name || preset.id || 'Untitled'}</div>
      <div class="truncate font-code text-[11px] text-text-subtle">{preset.id}</div>
    </div>
    <div class="flex shrink-0 items-center gap-0.5">
      <button
        type="button"
        onclick={() => onEdit(preset)}
        aria-label={`Edit ${preset.name || presetKey}`}
        title="Edit"
        class="rounded-brand p-1.5 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-brand-500"
      >
        <span class="material-symbols-outlined text-[16px]">edit</span>
      </button>
      <button
        type="button"
        onclick={() => onDuplicate(preset.id ?? '')}
        aria-label={`Duplicate ${preset.name || presetKey}`}
        title="Duplicate"
        class="rounded-brand p-1.5 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-brand-500"
      >
        <span class="material-symbols-outlined text-[16px]">content_copy</span>
      </button>
      {#if confirmDelete}
        <button type="button" onclick={() => onDelete(presetKey)} class="px-1.5 py-1 text-xs font-medium text-error hover:underline">
          Confirm
        </button>
        <button type="button" onclick={onCancelDelete} class="px-1.5 py-1 text-xs text-text-subtle hover:underline">
          Cancel
        </button>
      {:else}
        <button
          type="button"
          onclick={() => onDelete(presetKey)}
          aria-label={`Delete ${preset.name || presetKey}`}
          title="Delete"
          class="rounded-brand p-1.5 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-error"
        >
          <span class="material-symbols-outlined text-[16px]">delete</span>
        </button>
      {/if}
    </div>
  </div>

  {#if excerpt}
    <p class="line-clamp-3 break-words font-code text-[11px] leading-relaxed text-text-muted">{excerpt}</p>
  {:else}
    <p class="text-[11px] italic text-text-subtle">No prompt text.</p>
  {/if}

  <div class="mt-auto space-y-1.5">
    <div class="flex flex-wrap items-center gap-1.5">
      {#if preset.kind}
        <span class="rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold uppercase text-text-main">
          {preset.kind}
        </span>
      {/if}
      {#if preset.mode === 'chat'}
        <span class="rounded-full border border-purple-500/30 bg-purple-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-purple-400">
          Chat · {preset.turns?.length || 0} turns
        </span>
      {:else}
        <span class="rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-main">
          Single
        </span>
        {#if preset.position === 'append'}
          <span class="rounded-full border border-blue-500/30 bg-blue-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-blue-400">
            Append
          </span>
        {:else if preset.position === 'replace'}
          <span class="rounded-full border border-red-500/30 bg-red-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-red-400">
            Replace
          </span>
        {:else}
          <span class="rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">
            Prepend
          </span>
        {/if}
      {/if}
      {#if preset.geminiSafety}
        <span class="rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-amber-400">
          Safety relaxed
        </span>
      {/if}
    </div>
    <div class="truncate text-[11px] text-text-subtle">
      {preset.source ? preset.source + ' · ' : ''}{preset.keywords?.length
        ? preset.keywords.length + ' keyword' + (preset.keywords.length > 1 ? 's' : '') + ' · '
        : ''}{preset.excludes?.length ? preset.excludes.length + ' excluded · ' : ''}{#if usage !== undefined}<span class="text-text-muted">{usage} hit{usage === 1 ? '' : 's'}</span>{:else}No hits yet{/if}
    </div>
  </div>
</Card>
