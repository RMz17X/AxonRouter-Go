<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type PlaygroundModelEntry } from '../../api/client'

  interface Props {
    /** The binding target: a model wire id ("provider/model") or a combo name —
     * exactly the id the binding API stores. */
    value?: string
    disabled?: boolean
  }

  let { value = $bindable(''), disabled = false }: Props = $props()

  let entries = $state<PlaygroundModelEntry[]>([])
  let loading = $state(true)
  let loadError = $state('')

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // The picker mounts fresh with the bind modal, so mounting is the fetch
  // trigger — the inventory is the live one (active connections + combos),
  // the same list the Playground offers.
  async function load() {
    try {
      entries = await api.getPlaygroundModels()
    } catch (e) {
      loadError = errorMessage(e)
    } finally {
      loading = false
    }
  }
  onMount(() => {
    void load()
  })

  interface Group {
    label: string
    items: PlaygroundModelEntry[]
  }

  // One group per provider/connection for models, in inventory order, and
  // the Combos group last — a combo is an alias over models, not a peer of
  // the connections that serve them.
  const groups = $derived.by<Group[]>(() => {
    const byLabel = new Map<string, Group>()
    const combos: PlaygroundModelEntry[] = []
    for (const entry of entries) {
      if (entry.type === 'combo') {
        combos.push(entry)
        continue
      }
      const label = entry.connectionName ? `${entry.provider} — ${entry.connectionName}` : entry.provider
      let group = byLabel.get(label)
      if (!group) {
        group = { label, items: [] }
        byLabel.set(label, group)
      }
      group.items.push(entry)
    }
    const list = [...byLabel.values()]
    if (combos.length > 0) list.push({ label: 'Combos', items: combos })
    return list
  })

  // A target the inventory no longer offers — a model on a since-deactivated
  // connection, a deleted combo, a legacy catalog id — stays on the select
  // as a pinned first option, so an edit that only changes the preset or the
  // on/off switch can never silently rewrite or lose the binding's target.
  const pinned = $derived(
    value !== '' && !entries.some((entry) => entry.model === value) ? value : ''
  )

  // The option label shows the bare model under its connection group; the
  // wire id stays visible for combos, whose name is the whole address.
  function optionLabel(entry: PlaygroundModelEntry): string {
    if (entry.type === 'combo') return entry.model
    const prefix = `${entry.provider}/`
    return entry.model.startsWith(prefix) ? entry.model.slice(prefix.length) : entry.model
  }
</script>

{#if loading}
  <select disabled aria-label="Binding target" class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none disabled:opacity-50">
    <option>Loading models…</option>
  </select>
{:else if entries.length === 0 && !pinned}
  {#if loadError}
    <p class="rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-error">
      Could not load the model list: {loadError}
    </p>
  {:else}
    <p class="rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-muted">
      Nothing to bind yet — there are no active connections and no combos. Enable a provider
      connection or create a combo first, then bind it here.
    </p>
  {/if}
{:else}
  <select
    bind:value
    {disabled}
    aria-label="Binding target"
    class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main outline-none focus:border-brand-500 disabled:opacity-50"
  >
    {#if pinned}
      <option value={pinned}>(current) {pinned}</option>
    {/if}
    {#if value === ''}
      <option value="">Choose a model or combo…</option>
    {/if}
    {#each groups as group (group.label)}
      <optgroup label={group.label}>
        {#each group.items as entry, i (group.label + '|' + entry.model + '|' + (entry.connectionId ?? '') + '|' + i)}
          <option value={entry.model}>{optionLabel(entry)}</option>
        {/each}
      </optgroup>
    {/each}
  </select>
  {#if loadError}
    <p class="text-xs text-error">Could not refresh the model list: {loadError}</p>
  {/if}
{/if}
