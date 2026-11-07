<script lang="ts">
  import type { PlaygroundModelEntry } from '../../api/client'

  interface Props {
    entries: PlaygroundModelEntry[]
    /** The selected wire model id (a combo name, or "provider/model"). */
    value?: string
    disabled?: boolean
  }

  let { entries, value = $bindable(''), disabled = false }: Props = $props()

  interface Group {
    label: string
    items: PlaygroundModelEntry[]
  }

  // Entries arrive combos-first from the server; grouping preserves that
  // order (Map insertion order), so Combos lead and each provider's
  // connections follow in inventory order.
  const groups = $derived.by<Group[]>(() => {
    const byLabel = new Map<string, Group>()
    for (const entry of entries) {
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
  })

  // The option label shows the bare model under its connection group; the
  // wire id stays visible for combos, whose name is the whole address.
  function optionLabel(entry: PlaygroundModelEntry): string {
    if (entry.type === 'combo') return entry.model
    const prefix = `${entry.provider}/`
    return entry.model.startsWith(prefix) ? entry.model.slice(prefix.length) : entry.model
  }
</script>

<div class="flex flex-col gap-2">
  <label for="playground-model" class="text-xs font-medium text-text-muted">Model</label>
  {#if entries.length === 0}
    <p class="rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-muted">
      No active connections. Enable a provider connection first — the playground only lists models
      a request could actually be routed to.
    </p>
  {:else}
    <select
      id="playground-model"
      bind:value
      {disabled}
      class="w-full rounded-brand border border-border bg-surface px-3 py-2 text-sm text-text-main focus:outline-none focus:ring-2 focus:ring-brand-500/40 cursor-pointer disabled:opacity-50"
    >
      {#each groups as group (group.label)}
        <optgroup label={group.label}>
          {#each group.items as entry (entry.type + ':' + entry.model + ':' + (entry.connectionId || ''))}
            <option value={entry.model}>{optionLabel(entry)}</option>
          {/each}
        </optgroup>
      {/each}
    </select>
  {/if}
</div>
