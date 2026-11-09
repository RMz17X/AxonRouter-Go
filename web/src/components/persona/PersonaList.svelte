<script lang="ts">
  import { api, type JailbreakDoc, type PersonaDef } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'

  interface Props {
    personas: PersonaDef[]
    presets: JailbreakDoc[]
    /** Known combo names, for the MODEL/COMBO type chip on each target. */
    comboNames: Set<string>
    onAdd: () => void
    onEdit: (persona: PersonaDef) => void
    /** Reload presets + personas after a mutation. */
    onChanged: () => Promise<void> | void
    /** Surface a failure on the page status line. */
    onError: (message: string) => void
  }

  let { personas, presets, comboNames, onAdd, onEdit, onChanged, onError }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function presetLabel(id: string): string {
    const p = presets.find((x) => x.id === id)
    return p ? p.name || p.id || id : id
  }

  let search = $state('')
  let filtered = $derived.by(() => {
    const q = search.trim().toLowerCase()
    if (!q) return personas
    return personas.filter(
      (p) =>
        p.name.toLowerCase().includes(q) ||
        p.target.toLowerCase().includes(q) ||
        presetLabel(p.presetId).toLowerCase().includes(q)
    )
  })

  // Delete is two-step inline: the first click arms the row's confirm,
  // the second deletes. Arming resets whenever the list is re-filtered.
  let confirming = $state('')
  async function remove(persona: PersonaDef) {
    if (confirming !== persona.name) {
      confirming = persona.name
      return
    }
    confirming = ''
    try {
      await api.deletePersona(persona.name)
      await onChanged()
    } catch (e) {
      onError('Delete failed: ' + errorMessage(e))
    }
  }
</script>

<Card padding="md" class="overflow-hidden">
  <p class="text-xs text-text-subtle">
    A persona model is a name you mint: clients call it like any model, the gateway runs the target
    underneath, and the persona's preset is always injected. The name must differ from every real
    model, combo, and alias — that is what keeps the prompt attached to exactly the traffic that
    asked for it.
  </p>

  {#if personas.length === 0}
    <div class="flex flex-col items-center gap-2 px-4 py-10 text-center">
      <span class="material-symbols-outlined text-[22px] text-text-subtle">person_add</span>
      <p class="text-sm font-medium text-text-main">No persona models yet</p>
      <p class="max-w-md text-xs text-text-muted">
        Create one to give a prompt its own model name — pick the model or combo it runs on and the
        preset it injects.
      </p>
      <Button variant="primary" size="sm" onclick={onAdd} icon="add">New persona model</Button>
    </div>
  {:else}
    <div class="flex flex-wrap items-center gap-2 pt-4">
      <input
        type="text"
        bind:value={search}
        placeholder="Filter persona models…"
        aria-label="Filter persona models"
        class="min-w-[240px] flex-1 rounded-brand border border-border-subtle bg-surface-2 px-3 py-1.5 text-xs text-text-main placeholder:text-text-subtle"
      />
    </div>

    {#if filtered.length === 0}
      <p class="pt-3 text-xs text-text-subtle">No persona model matches "{search}".</p>
    {:else}
      <div class="divide-y divide-border-subtle">
        {#each filtered as persona (persona.name)}
          <div class="flex flex-wrap items-center gap-x-4 gap-y-2 py-3">
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-code text-sm font-semibold text-text-main">{persona.name}</span>
                <span class="rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-brand-500">PERSONA</span>
              </div>
              <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-text-muted">
                <span class="text-text-subtle">runs on</span>
                {#if comboNames.has(persona.target)}
                  <span class="shrink-0 rounded-full border border-brand-500/30 bg-brand-500/10 px-1.5 py-0.5 text-[10px] font-semibold text-brand-500">COMBO</span>
                {:else}
                  <span class="shrink-0 rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">MODEL</span>
                {/if}
                <span class="truncate font-code" title={persona.target}>{persona.target}</span>
                <span class="text-text-subtle">· preset</span>
                <span class="truncate">{presetLabel(persona.presetId)}</span>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Button variant="outline" size="sm" onclick={() => onEdit(persona)} icon="edit">Edit</Button>
              {#if confirming === persona.name}
                <Button variant="danger" size="sm" onclick={() => remove(persona)} icon="delete">Confirm delete</Button>
                <Button variant="ghost" size="sm" onclick={() => (confirming = '')}>Cancel</Button>
              {:else}
                <Button variant="ghost" size="sm" onclick={() => remove(persona)} icon="delete">Delete</Button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</Card>
