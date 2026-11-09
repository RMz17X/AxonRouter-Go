<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type Combo, type JailbreakDoc, type JailbreakKeyword, type PersonaDef } from '../api/client'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import GlobalExcludes from './persona/GlobalExcludes.svelte'
  import PersonaList from './persona/PersonaList.svelte'
  import PersonaModal from './persona/PersonaModal.svelte'
  import PresetEditor from './persona/PresetEditor.svelte'
  import PresetGrid from './persona/PresetGrid.svelte'
  import TestMatchModal from './persona/TestMatchModal.svelte'
  import type { PersonaSegment } from './persona/types'

  // Library + persona models data, loaded once and reloaded after every mutation.
  let presets = $state<JailbreakDoc[]>([])
  let personas = $state<PersonaDef[]>([])
  let presetUsage = $state<Record<string, number>>({})
  let globalExcludes = $state<JailbreakKeyword[]>([])
  let combos = $state<Combo[]>([])
  let comboNames = $derived(new Set(combos.map((c: Combo) => c.name).filter(Boolean)))
  let loading = $state(false)
  // Cross-cutting failures (load, duplicate) land here; per-list results
  // stay inside their segment.
  let pageStatus = $state('')

  let segment = $state<PersonaSegment>('presets')

  // Modal mounts. Each modal component mounts fresh per open, so its
  // internal state always starts from the props it is handed.
  let editorOpen = $state(false)
  let editorPreset = $state<JailbreakDoc | null>(null)
  let personaOpen = $state(false)
  let personaEdit = $state<PersonaDef | null>(null)
  let testMatchOpen = $state(false)

  let totalInjections = $derived(Object.values(presetUsage).reduce((a, b) => a + b, 0))

  onMount(load)
  async function load() {
    loading = true
    try {
      const [presetList, personaRes, excludeList] = await Promise.all([
        api.listJailbreakPresets(),
        api.listPersonas(),
        api.getJailbreakExcludes(),
      ])
      presets = Array.isArray(presetList) ? presetList : []
      personas = personaRes?.personas || []
      globalExcludes = excludeList?.excludes || []
      // Per-preset hit counters. A failure here is cosmetic, so it is
      // swallowed: the preset list still renders without the counts.
      try {
        const usageRes = await api.getJailbreakUsage()
        presetUsage = usageRes?.usage || {}
      } catch {}
      // Combos feed the MODEL/COMBO chip on persona rows (via the derived
      // name set) and the persona modal's target picker, so the page and
      // the modal share one fetch. A failure is cosmetic for the chips:
      // without the list every target simply reads MODEL.
      try {
        combos = await api.getCombos()
      } catch {}
    } catch (e) {
      pageStatus = 'Load failed: ' + errorMessage(e)
    } finally {
      loading = false
    }
  }
  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  function openCreate() {
    editorPreset = null
    editorOpen = true
  }
  function openEdit(preset: JailbreakDoc) {
    editorPreset = preset
    editorOpen = true
  }
  function openPersonaCreate() {
    personaEdit = null
    personaOpen = true
  }
  function openPersonaEdit(persona: PersonaDef) {
    personaEdit = persona
    personaOpen = true
  }
</script>

<div class="space-y-5">
  <!-- Header strip: title and description on the left, the three live
       counts on the right — label above, number below, the same reading
       order the summary cards used, compressed into one row. -->
  <Card padding="md">
    <div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
      <div class="min-w-0 space-y-1">
        <h2 class="text-sm font-bold text-text-main">Persona</h2>
        <p class="text-xs text-text-muted">
          System prompts with model names of their own — mint a name, pick the model or combo it runs on, and the preset is always injected.
        </p>
      </div>
      <div class="flex items-center gap-6">
        <div>
          <div class="font-code text-[10px] tracking-wider text-text-subtle uppercase">Presets</div>
          <div class="mt-0.5 text-sm font-semibold text-text-main">{presets.length}</div>
        </div>
        <div>
          <div class="font-code text-[10px] tracking-wider text-text-subtle uppercase">Persona models</div>
          <div class="mt-0.5 text-sm font-semibold text-text-main">{personas.length}</div>
        </div>
        <div>
          <div class="font-code text-[10px] tracking-wider text-text-subtle uppercase">Injections</div>
          <div class="mt-0.5 text-sm font-semibold text-text-main">{totalInjections}</div>
        </div>
      </div>
    </div>
  </Card>

  <!-- Segment bar: the two workspaces, plus the active segment's primary
       action — the page no longer stacks both lists in one scroll. -->
  <div class="flex flex-wrap items-center justify-between gap-2">
    <div class="flex gap-1 rounded-brand-lg border border-border-subtle bg-surface-2 p-1" role="tablist" aria-label="Persona sections">
      <button
        type="button"
        role="tab"
        aria-selected={segment === 'presets'}
        onclick={() => (segment = 'presets')}
        class="flex items-center gap-1.5 rounded-brand px-3 py-1.5 text-xs font-medium transition-colors {segment === 'presets'
          ? 'border border-brand-500 bg-brand-500/10 text-text-main'
          : 'border border-transparent text-text-muted hover:text-text-main'}"
      >
        Presets
        <span class="rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">{presets.length}</span>
      </button>
      <button
        type="button"
        role="tab"
        aria-selected={segment === 'personas'}
        onclick={() => (segment = 'personas')}
        class="flex items-center gap-1.5 rounded-brand px-3 py-1.5 text-xs font-medium transition-colors {segment === 'personas'
          ? 'border border-brand-500 bg-brand-500/10 text-text-main'
          : 'border border-transparent text-text-muted hover:text-text-main'}"
      >
        Persona models
        <span class="rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">{personas.length}</span>
      </button>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      {#if loading}<span class="text-xs text-text-subtle">Loading…</span>{/if}
      {#if segment === 'presets'}
        <Button variant="outline" size="sm" onclick={() => (testMatchOpen = true)} icon="troubleshoot">Test match</Button>
        <Button variant="primary" size="sm" onclick={openCreate} icon="note_add">New preset</Button>
      {:else}
        <Button variant="primary" size="sm" onclick={openPersonaCreate} icon="add">New persona model</Button>
      {/if}
    </div>
  </div>

  {#if pageStatus}<p class="text-xs text-error">{pageStatus}</p>{/if}

  {#if segment === 'presets'}
    <div class="space-y-5">
      <PresetGrid {presets} {presetUsage} onEdit={openEdit} onNew={openCreate} onChanged={load} onError={(m) => (pageStatus = m)} />
      <GlobalExcludes bind:excludes={globalExcludes} />
    </div>
  {:else}
    <PersonaList {personas} {presets} {comboNames} onAdd={openPersonaCreate} onEdit={openPersonaEdit} onChanged={load} onError={(m) => (pageStatus = m)} />
  {/if}

  <!-- How it works: read-once reference, so it carries the lightest
       visual weight of any card on the page. -->
  <Card padding="sm" class="bg-surface-2/60">
    <details class="group">
      <summary class="flex cursor-pointer items-center gap-2 text-sm font-bold text-text-main">
        <span class="material-symbols-outlined text-[18px] text-text-subtle">info</span>
        How it works
        <span class="material-symbols-outlined ml-auto text-[16px] text-text-subtle transition-transform group-open:rotate-180">expand_more</span>
      </summary>
      <div class="space-y-3 pt-3 text-xs leading-relaxed text-text-muted">
        <p>
          A <span class="text-text-main">persona model</span> is a model name you mint yourself. It runs
          one target — a real model or a whole combo — chosen in the picker, and it always injects its
          preset ahead of the conversation. Clients call the persona name exactly like a model, and it
          appears in the model list next to the real ones.
        </p>
        <p>
          The name must be <span class="text-text-main">different from every existing name</span> — no
          real model, combo, or alias may share it. That is the whole design: a prompt is never armed
          on a name that already means something else, so the traffic that gets the prompt is exactly
          the traffic that asked for the persona.
        </p>
        <p>
          A persona is simply a system prompt of your own — nothing about the mechanism limits what it
          may say. Because it is prepended ahead of the model's original instructions, it is also the
          usual way to apply <span class="text-text-main">jailbreak prompting</span>: a persona that
          reframes the assistant's role, drops its refusals, or replaces its rules entirely is written
          here and served exactly like any other. The gateway does not judge the content, it only
          delivers it to the callers of that name.
        </p>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold tracking-wide text-text-subtle uppercase">Two ways a preset fires</div>
          <ul class="space-y-1.5 pl-4">
            <li class="flex gap-2">
              <span class="shrink-0 text-brand-500">1.</span>
              <span><span class="text-text-main">Persona model</span> — the explicit path: the client sends the persona's own name, the gateway runs its target and injects its preset. Nothing else is consulted for that request — no keyword rule stacks on top.</span>
            </li>
            <li class="flex gap-2">
              <span class="shrink-0 text-brand-500">2.</span>
              <span><span class="text-text-main">Keyword (include)</span> — a rule inside a preset covers every real model id that matches, across providers and combo legs. Three modes: <span class="font-code">word</span> (whole token, default), <span class="font-code">partial</span> (substring), <span class="font-code">regex</span> (RE2).</span>
            </li>
          </ul>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold tracking-wide text-text-subtle uppercase">Precedence</div>
          <ol class="list-decimal space-y-1 pl-4">
            <li>Persona model — a request to a persona name gets that persona's preset, full stop.</li>
            <li>Global exclude list — a match means no keyword injection, whatever anything else says.</li>
            <li>Exclude of the winning preset.</li>
            <li>Keyword on the id the client sent, before a keyword on a forwarded leg.</li>
            <li>Among keyword hits on the same id, the longest wins; a tie goes to the lowest preset id.</li>
          </ol>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold tracking-wide text-text-subtle uppercase">Filling a preset</div>
          <ul class="space-y-1 pl-4">
            <li class="flex gap-2"><span class="shrink-0 text-brand-500">•</span><span><span class="text-text-main">New preset</span> — one modal, three tabs: <span class="text-text-muted">Write</span> builds one preset field by field, <span class="text-text-muted">From files</span> picks or drops several at once, <span class="text-text-muted">From URL</span> pulls a repository or any http(s) address. The file name becomes the preset name and its keyword chain, so <span class="font-code">SOUL.md</span>, <span class="font-code">AGENTS.md</span>, <span class="font-code">CLAUDE.md</span> are labelled with their kind right away.</span></li>
          </ul>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold tracking-wide text-text-subtle uppercase">Checking without guessing</div>
          <ul class="space-y-1 pl-4">
            <li class="flex gap-2"><span class="shrink-0 text-brand-500">•</span><span><span class="text-text-main">Test match</span> — type an id and the page answers which preset a keyword rule would inject for it and why (keyword, global-exclude, preset-exclude, or no-match), plus a preview of the final payload.</span></li>
            <li class="flex gap-2"><span class="shrink-0 text-brand-500">•</span><span><span class="text-text-main">Usage stats</span> — every preset shows how many times it actually injected, persona traffic included.</span></li>
          </ul>
        </div>
        <p class="text-[11px] text-text-subtle">
          Limits: prompt 512 KB, at most 20 rules per list, rule length 200 characters. A rule the gateway would reject is refused at save time with the reason, so a typo never becomes a preset that silently stops matching.
        </p>
      </div>
    </details>
  </Card>
</div>

{#if editorOpen}
  <PresetEditor editing={editorPreset} onClose={() => (editorOpen = false)} onSaved={load} />
{/if}
{#if personaOpen}
  <PersonaModal {presets} editTarget={personaEdit} {comboNames} {combos} onClose={() => (personaOpen = false)} onSaved={load} />
{/if}
{#if testMatchOpen}
  <TestMatchModal onClose={() => (testMatchOpen = false)} />
{/if}
