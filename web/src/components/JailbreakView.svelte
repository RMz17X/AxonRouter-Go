<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type Combo, type JailbreakDoc, type JailbreakKeyword, type JailbreakModelConfig } from '../api/client'
  import Button from '../lib/ui/Button.svelte'
  import Card from '../lib/ui/Card.svelte'
  import BindingList from './persona/BindingList.svelte'
  import BindModal from './persona/BindModal.svelte'
  import GlobalExcludes from './persona/GlobalExcludes.svelte'
  import PresetEditor from './persona/PresetEditor.svelte'
  import PresetGrid from './persona/PresetGrid.svelte'
  import TestMatchModal from './persona/TestMatchModal.svelte'
  import type { PersonaSegment } from './persona/types'

  // Library + bindings data, loaded once and reloaded after every mutation.
  let presets = $state<JailbreakDoc[]>([])
  let models = $state<Record<string, JailbreakModelConfig>>({})
  let presetUsage = $state<Record<string, number>>({})
  let globalExcludes = $state<JailbreakKeyword[]>([])
  let comboNames = $state<Set<string>>(new Set())
  let loading = $state(false)
  // Cross-cutting failures (load, duplicate, toggle) land here; per-list
  // batch results stay inside their segment.
  let pageStatus = $state('')

  let segment = $state<PersonaSegment>('presets')

  // Modal mounts. Each modal component mounts fresh per open, so its
  // internal state always starts from the props it is handed.
  let editorOpen = $state(false)
  let editorPreset = $state<JailbreakDoc | null>(null)
  let bindOpen = $state(false)
  let bindEdit = $state<{ id: string; cfg: JailbreakModelConfig } | null>(null)
  let testMatchOpen = $state(false)

  let armedCount = $derived(Object.values(models).filter((c) => c.enabled).length)
  let bindingCount = $derived(Object.keys(models).length)
  let totalInjections = $derived(Object.values(presetUsage).reduce((a, b) => a + b, 0))

  onMount(load)
  async function load() {
    loading = true
    try {
      const [presetList, modelMap, excludeList] = await Promise.all([
        api.listJailbreakPresets(),
        api.listJailbreakModels(),
        api.getJailbreakExcludes(),
      ])
      presets = Array.isArray(presetList) ? presetList : []
      models = modelMap || {}
      globalExcludes = excludeList?.excludes || []
      // #5 Per-preset hit counters. A failure here is cosmetic, so it is
      // swallowed: the preset list still renders without the counts.
      try {
        const usageRes = await api.getJailbreakUsage()
        presetUsage = usageRes?.usage || {}
      } catch {}
      // Combo names feed the MODEL/COMBO chip on binding rows. Cosmetic as
      // well: without them every row simply reads MODEL.
      try {
        const combos = await api.getCombos()
        comboNames = new Set(combos.map((c: Combo) => c.name).filter(Boolean))
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
  function openBind() {
    bindEdit = null
    bindOpen = true
  }
  function openBindingEdit(id: string, cfg: JailbreakModelConfig) {
    bindEdit = { id, cfg }
    bindOpen = true
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
          System prompts injected ahead of a model's own instructions — armed per model, provider, or combo.
        </p>
      </div>
      <div class="flex items-center gap-6">
        <div>
          <div class="font-code text-[10px] tracking-wider text-text-subtle uppercase">Presets</div>
          <div class="mt-0.5 text-sm font-semibold text-text-main">{presets.length}</div>
        </div>
        <div>
          <div class="font-code text-[10px] tracking-wider text-text-subtle uppercase">Bindings armed</div>
          <div class="mt-0.5 text-sm font-semibold text-text-main">
            {armedCount} <span class="font-normal text-text-subtle">/ {bindingCount}</span>
          </div>
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
        aria-selected={segment === 'bindings'}
        onclick={() => (segment = 'bindings')}
        class="flex items-center gap-1.5 rounded-brand px-3 py-1.5 text-xs font-medium transition-colors {segment === 'bindings'
          ? 'border border-brand-500 bg-brand-500/10 text-text-main'
          : 'border border-transparent text-text-muted hover:text-text-main'}"
      >
        Bindings
        <span class="rounded-full border border-border-subtle bg-surface-3 px-1.5 py-0.5 text-[10px] font-semibold text-text-muted">{bindingCount}</span>
      </button>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      {#if loading}<span class="text-xs text-text-subtle">Loading…</span>{/if}
      {#if segment === 'presets'}
        <Button variant="outline" size="sm" onclick={() => (testMatchOpen = true)} icon="troubleshoot">Test match</Button>
        <Button variant="primary" size="sm" onclick={openCreate} icon="note_add">New preset</Button>
      {:else}
        <Button variant="primary" size="sm" onclick={openBind} icon="add">Add binding</Button>
      {/if}
    </div>
  </div>

  {#if pageStatus}<p class="text-xs text-error">{pageStatus}</p>{/if}

  {#if segment === 'presets'}
    <PresetGrid {presets} {presetUsage} onEdit={openEdit} onNew={openCreate} onChanged={load} onError={(m) => (pageStatus = m)} />
  {:else}
    <div class="space-y-5">
      <BindingList {models} {presets} {comboNames} onAdd={openBind} onEdit={openBindingEdit} onChanged={load} onError={(m) => (pageStatus = m)} />
      <GlobalExcludes bind:excludes={globalExcludes} />
    </div>
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
          This feature prepends a <span class="text-text-main">system prompt</span> ahead of the original content for the models, providers, or combos you choose.
          Injection is <span class="text-text-main">opt-in</span> and off by default: a model is never injected just because it exists, only because you armed it.
        </p>
        <p>
          A persona is simply a system prompt of your own — nothing about the mechanism limits what it
          may say. Because it is prepended ahead of the model's original instructions, it is also the
          usual way to apply <span class="text-text-main">jailbreak prompting</span>: a persona that
          reframes the assistant's role, drops its refusals, or replaces its rules entirely is written
          here and armed exactly like any other. The gateway does not judge the content, it only
          delivers it to the models you chose.
        </p>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold tracking-wide text-text-subtle uppercase">Three ways to arm a preset</div>
          <ul class="space-y-1.5 pl-4">
            <li class="flex gap-2">
              <span class="shrink-0 text-brand-500">1.</span>
              <span><span class="text-text-main">Exact binding</span> — bind one id to one preset. The precise tool: use it when one id needs a different preset from the rest of its family, or when a whole combo should be armed regardless of its legs.</span>
            </li>
            <li class="flex gap-2">
              <span class="shrink-0 text-brand-500">2.</span>
              <span><span class="text-text-main">Keyword (include)</span> — one rule covers every model id that matches, across providers and combo legs. Three modes: <span class="font-code">word</span> (whole token, default), <span class="font-code">partial</span> (substring), <span class="font-code">regex</span> (RE2).</span>
            </li>
            <li class="flex gap-2">
              <span class="shrink-0 text-brand-500">3.</span>
              <span><span class="text-text-main">Excludes (deny)</span> — two deny lists: per preset (a model matching a keyword still never gets this preset) and global (never injected at all, outranking everything).</span>
            </li>
          </ul>
        </div>
        <div class="space-y-1.5">
          <div class="text-[11px] font-semibold tracking-wide text-text-subtle uppercase">Precedence</div>
          <ol class="list-decimal space-y-1 pl-4">
            <li>Global exclude list — a match means no injection, whatever anything else says.</li>
            <li>Exact binding on the id the client sent.</li>
            <li>Exclude of the winning preset.</li>
            <li>Keyword on the primary id, before a keyword on a forwarded leg.</li>
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
            <li class="flex gap-2"><span class="shrink-0 text-brand-500">•</span><span><span class="text-text-main">Test match</span> — type an id and the page answers which preset wins and why (exact-binding, keyword, global-exclude, preset-exclude, or no-match), plus a preview of the final payload.</span></li>
            <li class="flex gap-2"><span class="shrink-0 text-brand-500">•</span><span><span class="text-text-main">Add</span> — one modal, two tabs: <span class="text-text-muted">Single</span> types one id by hand, <span class="text-text-muted">Bulk</span> picks many ids straight from the live catalog (1500+ models) and binds them all to one preset.</span></li>
            <li class="flex gap-2"><span class="shrink-0 text-brand-500">•</span><span><span class="text-text-main">Usage stats</span> — every preset shows how many times it actually injected.</span></li>
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
{#if bindOpen}
  <BindModal {presets} editTarget={bindEdit} onClose={() => (bindOpen = false)} onSaved={load} />
{/if}
{#if testMatchOpen}
  <TestMatchModal onClose={() => (testMatchOpen = false)} />
{/if}
