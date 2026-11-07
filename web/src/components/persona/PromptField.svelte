<script lang="ts">
  import { marked } from 'marked'
  import type { PresetFormState } from './types'
  import TurnEditor from './TurnEditor.svelte'

  interface Props {
    /** The editor form; this field mutates mode/position/prompt/turns on it. */
    form: PresetFormState
  }

  let { form }: Props = $props()

  // #8 Prompt preview inside the preset editor.
  let promptPreviewOpen = $state(false)
</script>

<!-- Mode Toggle: System Prompt vs Multi-turn Script -->
<div class="space-y-1.5">
  <span class="text-xs text-text-muted">Injection Mode</span>
  <div class="grid grid-cols-2 gap-2">
    <button
      type="button"
      onclick={() => (form.mode = 'single')}
      class="flex items-center justify-center gap-2 rounded-brand border px-3 py-2 text-xs font-medium transition-colors {form.mode === 'single'
        ? 'border-brand-500 bg-brand-500/10 font-semibold text-text-main'
        : 'border-border-subtle bg-surface-2 text-text-muted hover:border-border hover:text-text-main'}"
    >
      <span class="material-symbols-outlined text-[16px]">description</span>
      System Prompt
    </button>
    <button
      type="button"
      onclick={() => (form.mode = 'chat')}
      class="flex items-center justify-center gap-2 rounded-brand border px-3 py-2 text-xs font-medium transition-colors {form.mode === 'chat'
        ? 'border-purple-500 bg-purple-500/10 font-semibold text-purple-400'
        : 'border-border-subtle bg-surface-2 text-text-muted hover:border-border hover:text-text-main'}"
    >
      <span class="material-symbols-outlined text-[16px]">chat</span>
      Multi-turn Script
    </button>
  </div>
</div>

{#if form.mode === 'single'}
  <div class="space-y-1.5">
    <span class="text-xs text-text-muted">Position</span>
    <div class="flex flex-wrap items-center gap-4 py-1">
      <label class="flex cursor-pointer items-center gap-2 text-xs text-text-main">
        <input type="radio" name="preset-position" value="prepend" bind:group={form.position} class="accent-brand-500" />
        Prepend (before system/user prompt)
      </label>
      <label class="flex cursor-pointer items-center gap-2 text-xs text-text-main">
        <input type="radio" name="preset-position" value="append" bind:group={form.position} class="accent-brand-500" />
        Append (after existing system prompt)
      </label>
      <label class="flex cursor-pointer items-center gap-2 text-xs text-text-main">
        <input type="radio" name="preset-position" value="replace" bind:group={form.position} class="accent-brand-500" />
        Replace (discard the client's system prompt)
      </label>
    </div>
  </div>

  <!-- The prompt is Markdown: it is stored and injected verbatim, and models
       read Markdown natively, so the preview below is a rendering aid only —
       what the model receives is the text itself. -->
  <div class="block space-y-1.5">
    <span class="text-xs text-text-muted">System Prompt <span class="text-text-subtle">(Markdown)</span></span>
    <textarea
      bind:value={form.prompt}
      rows="8"
      placeholder={'## Who you are\nYou are an expert assistant…\n\n## Rules\n- Be direct.\n- **Never** hedge.'}
      class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 font-code text-sm text-text-main outline-none placeholder:text-text-subtle focus:border-brand-500 resize-y"
    ></textarea>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="text-[11px] text-text-subtle">
        Written in Markdown — <span class="font-code">##</span> headings, <span class="font-code">-</span> lists,
        <span class="font-code">**bold**</span>. Sent to the model exactly as written.
      </p>
      <button
        type="button"
        onclick={() => (promptPreviewOpen = !promptPreviewOpen)}
        class="shrink-0 text-[11px] text-text-muted hover:text-brand-500 hover:underline"
      >
        {promptPreviewOpen ? 'Hide preview' : 'Preview markdown'}
      </button>
    </div>
    {#if promptPreviewOpen && form.prompt.trim()}
      <div class="mt-1 max-h-60 overflow-y-auto rounded-brand border border-border-subtle bg-surface-2 p-3">
        <div class="prose-jailbreak text-xs leading-relaxed text-text-main">
          {@html marked.parse(form.prompt, { async: false }) as string}
        </div>
      </div>
    {/if}
  </div>
{:else}
  <TurnEditor bind:turns={form.turns} />
{/if}
