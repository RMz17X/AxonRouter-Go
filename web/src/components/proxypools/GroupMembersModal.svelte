<script lang="ts">
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import type { ProxyPool } from '../../api/client'

  interface Props {
    isOpen: boolean
    groupName: string
    pools: ProxyPool[]
    busy?: boolean
    /** Called with the desired member id set; the parent diffs and writes. */
    onSave: (memberIds: string[]) => void
    onClose: () => void
  }

  let { isOpen, groupName, pools, busy = false, onSave, onClose }: Props = $props()

  let checked = $state<Set<string>>(new Set())
  let initial = $state<Set<string>>(new Set())
  let query = $state('')

  // Seed the selection from the group's current members each time it opens.
  $effect(() => {
    if (!isOpen) return
    const members = pools
      .filter((p) => (p.group || '').trim() === groupName)
      .map((p) => p.id)
    checked = new Set(members)
    initial = new Set(members)
    query = ''
  })

  let visible = $derived.by(() => {
    const q = query.trim().toLowerCase()
    if (!q) return pools
    return pools.filter(
      (p) =>
        (p.name || '').toLowerCase().includes(q) ||
        (p.proxyUrl || '').toLowerCase().includes(q)
    )
  })

  let dirty = $derived(
    checked.size !== initial.size || [...checked].some((id) => !initial.has(id))
  )

  function toggle(id: string) {
    const next = new Set(checked)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    checked = next
  }
</script>

<Modal {isOpen} title={`Manage members — ${groupName}`} onClose={busy ? undefined : onClose}>
  {#snippet footer()}
    <span class="mr-auto text-xs text-text-muted">{checked.size} member{checked.size === 1 ? '' : 's'}</span>
    <Button variant="ghost" onclick={onClose} disabled={busy}>Cancel</Button>
    <Button onclick={() => onSave([...checked])} disabled={!dirty || busy}>
      {busy ? 'Saving…' : 'Save Members'}
    </Button>
  {/snippet}

  <div class="flex flex-col gap-3">
    <div class="relative">
      <span class="material-symbols-outlined pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[16px] text-text-muted">search</span>
      <input
        type="text"
        bind:value={query}
        placeholder="Filter pools by name or URL…"
        aria-label="Filter pools"
        class="h-8 w-full rounded-lg border border-border-subtle bg-surface-2 pl-7 pr-2 text-xs text-text-main outline-none placeholder:text-text-muted focus:border-primary/50"
      />
    </div>

    <div class="flex max-h-80 flex-col gap-1 overflow-y-auto custom-scrollbar">
      {#each visible as pool (pool.id)}
        <label
          class="flex cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 transition-colors hover:bg-surface-2"
        >
          <input
            type="checkbox"
            checked={checked.has(pool.id)}
            onchange={() => toggle(pool.id)}
            class="size-4 shrink-0 rounded border-black/20 cursor-pointer dark:border-white/20"
          />
          <span class="min-w-0 flex-1">
            <span class="block truncate text-xs font-medium text-text-main">
              {pool.name}
              {#if !pool.isActive}<span class="text-text-muted">(inactive)</span>{/if}
            </span>
            <span class="block truncate font-mono text-[10px] text-text-muted">{pool.proxyUrl}</span>
          </span>
          {#if (pool.group || '').trim() && (pool.group || '').trim() !== groupName}
            <span class="shrink-0 text-[10px] text-amber-500">in {pool.group}</span>
          {/if}
        </label>
      {/each}
      {#if visible.length === 0}
        <p class="py-4 text-center text-xs text-text-muted">No pools match the filter.</p>
      {/if}
    </div>
    <p class="text-[11px] text-text-muted">
      Checking a pool from another group moves it here on save.
    </p>
  </div>
</Modal>
