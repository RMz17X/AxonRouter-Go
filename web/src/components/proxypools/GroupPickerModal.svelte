<script lang="ts">
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'

  interface Props {
    isOpen: boolean
    title: string
    groups: { name: string; count: number }[]
    /** Offer "No group" (remove the assignment) when it means something. */
    showUngroup?: boolean
    busy?: boolean
    /** name = assign to that group; null = remove from group. */
    onPick: (group: string | null) => void
    onClose: () => void
  }

  let { isOpen, title, groups, showUngroup = false, busy = false, onPick, onClose }: Props =
    $props()

  let newGroupName = $state('')

  $effect(() => {
    if (isOpen) newGroupName = ''
  })

  function submitNew() {
    const name = newGroupName.trim()
    if (!name || busy) return
    onPick(name)
  }
</script>

<Modal {isOpen} {title} size="sm" onClose={busy ? undefined : onClose}>
  <div class="flex flex-col gap-3">
    <div class="flex max-h-64 flex-col gap-1 overflow-y-auto custom-scrollbar">
      {#if showUngroup}
        <button
          type="button"
          disabled={busy}
          onclick={() => onPick(null)}
          class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main cursor-pointer disabled:opacity-50"
        >
          <span class="material-symbols-outlined text-[18px]">folder_off</span>
          No group
        </button>
      {/if}
      {#each groups as grp (grp.name)}
        <button
          type="button"
          disabled={busy}
          onclick={() => onPick(grp.name)}
          class="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm text-text-main transition-colors hover:bg-surface-2 cursor-pointer disabled:opacity-50"
        >
          <span class="material-symbols-outlined text-[18px] text-primary">folder</span>
          <span class="min-w-0 flex-1 truncate">{grp.name}</span>
          <span class="text-xs text-text-muted">{grp.count}</span>
        </button>
      {/each}
      {#if groups.length === 0 && !showUngroup}
        <p class="px-1 py-2 text-xs text-text-muted">No groups yet — create the first one below.</p>
      {/if}
    </div>

    <div class="border-t border-border/50 pt-3">
      <label class="mb-1.5 block text-sm font-medium text-text-main" for="new-group-name">
        New group
      </label>
      <div class="flex gap-2">
        <input
          id="new-group-name"
          type="text"
          bind:value={newGroupName}
          placeholder="e.g. residential-us"
          disabled={busy}
          onkeydown={(e) => {
            if (e.key === 'Enter') submitNew()
          }}
          class="min-w-0 flex-1 rounded-sm border border-border bg-surface px-3 py-2 text-sm text-text-main outline-none transition-all placeholder:text-text-muted/60 focus:border-primary/60 focus:ring-2 focus:ring-primary/15 disabled:opacity-50"
        />
        <Button onclick={submitNew} disabled={!newGroupName.trim() || busy}>
          {busy ? 'Saving…' : 'Add'}
        </Button>
      </div>
    </div>
  </div>
</Modal>
