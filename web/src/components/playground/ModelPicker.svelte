<script lang="ts">
  import type { PlaygroundModelEntry } from '../../api/client'
  import Modal from '../../lib/ui/Modal.svelte'

  interface Props {
    entries: PlaygroundModelEntry[]
    /** The selected wire model id (a combo name, or "provider/model"). */
    value?: string
    disabled?: boolean
  }

  let { entries, value = $bindable(''), disabled = false }: Props = $props()

  let open = $state(false)
  let query = $state('')

  interface Group {
    label: string
    items: PlaygroundModelEntry[]
  }

  const selected = $derived(entries.find((e) => e.model === value))

  // Entries arrive combos-first from the server; grouping preserves that
  // order (Map insertion order), so Combos lead and each provider follows
  // in inventory order.
  function groupEntries(list: PlaygroundModelEntry[]): Group[] {
    const byLabel = new Map<string, Group>()
    for (const entry of list) {
      const label =
        entry.type === 'combo'
          ? 'Combos'
          : entry.connectionName
            ? `${entry.provider} — ${entry.connectionName}`
            : entry.provider
      let group = byLabel.get(label)
      if (!group) {
        group = { label, items: [] }
        byLabel.set(label, group)
      }
      group.items.push(entry)
    }
    return [...byLabel.values()]
  }

  const groups = $derived.by<Group[]>(() => {
    const q = query.trim().toLowerCase()
    if (!q) return groupEntries(entries)
    return groupEntries(
      entries.filter(
        (e) =>
          e.model.toLowerCase().includes(q) ||
          e.provider.toLowerCase().includes(q) ||
          (e.connectionName || '').toLowerCase().includes(q)
      )
    )
  })

  // The row label shows the bare model under its provider group; the wire
  // id stays whole for combos, whose name is the entire address.
  function bareModel(entry: PlaygroundModelEntry): string {
    if (entry.type === 'combo') return entry.model
    const prefix = `${entry.provider}/`
    return entry.model.startsWith(prefix) ? entry.model.slice(prefix.length) : entry.model
  }

  function pick(entry: PlaygroundModelEntry) {
    value = entry.model
    open = false
  }

  function openPicker() {
    if (disabled) return
    query = ''
    open = true
  }
</script>

<button
  type="button"
  onclick={openPicker}
  {disabled}
  class="flex min-w-0 flex-1 items-center gap-2.5 rounded-brand-lg border border-border bg-surface px-3 py-2 text-left transition-colors hover:border-brand-500/50 disabled:cursor-not-allowed disabled:opacity-50 sm:max-w-md"
  title="Choose a model"
>
  <span class="material-symbols-outlined shrink-0 text-[20px] text-brand-500">
    {selected?.type === 'combo' ? 'bolt' : 'smart_toy'}
  </span>
  <span class="min-w-0 flex-1">
    <span class="block truncate text-sm font-medium text-text-main">
      {selected ? bareModel(selected) : entries.length === 0 ? 'No models available' : 'Choose a model'}
    </span>
    <span class="block truncate text-xs text-text-subtle">
      {selected
        ? selected.type === 'combo'
          ? 'Combo'
          : selected.provider
        : 'Enable a provider connection first'}
    </span>
  </span>
  <span class="material-symbols-outlined shrink-0 text-[18px] text-text-subtle">expand_more</span>
</button>

<Modal isOpen={open} onClose={() => (open = false)} title="Choose a model" icon="smart_toy" size="lg">
  <div class="flex flex-col gap-3">
    <div class="relative">
      <span
        class="material-symbols-outlined pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[18px] text-text-subtle"
      >
        search
      </span>
      <input
        type="text"
        bind:value={query}
        placeholder="Search models or providers…"
        class="w-full rounded-brand border border-border-subtle bg-surface-2 py-2 pl-9 pr-3 text-sm text-text-main outline-none focus:border-brand-500"
      />
    </div>

    <div class="flex max-h-[50vh] flex-col gap-4 overflow-y-auto pr-1">
      {#if entries.length === 0}
        <p class="py-6 text-center text-sm text-text-muted">
          No active connections. Enable a provider connection first — the playground only lists
          models a request could actually be routed to.
        </p>
      {:else if groups.length === 0}
        <p class="py-6 text-center text-sm text-text-muted">No model matches “{query}”.</p>
      {:else}
        {#each groups as group (group.label)}
          <div class="flex flex-col gap-1">
            <p class="px-1 text-xs font-medium uppercase tracking-wide text-text-subtle">
              {group.label}
            </p>
            {#each group.items as entry (entry.type + ':' + entry.model + ':' + (entry.connectionId || ''))}
              <button
                type="button"
                onclick={() => pick(entry)}
                class="flex items-center gap-2 rounded-brand px-2.5 py-2 text-left text-sm transition-colors {entry.model ===
                value
                  ? 'bg-primary/10 text-primary'
                  : 'text-text-main hover:bg-surface-2'}"
              >
                <span class="material-symbols-outlined text-[17px] {entry.type === 'combo' ? 'text-brand-500' : 'text-text-subtle'}">
                  {entry.type === 'combo' ? 'bolt' : 'smart_toy'}
                </span>
                <span class="min-w-0 flex-1 truncate">{bareModel(entry)}</span>
                {#if entry.model === value}
                  <span class="material-symbols-outlined text-[17px]">check</span>
                {/if}
              </button>
            {/each}
          </div>
        {/each}
      {/if}
    </div>

    <p class="text-xs text-text-subtle">
      {entries.length} routable model{entries.length === 1 ? '' : 's'} from active connections and
      combos.
    </p>
  </div>
</Modal>
