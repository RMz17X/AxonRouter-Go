<script lang="ts">
  import type { PlaygroundMessage } from './types'
  import { renderMarkdown } from './markdown'

  interface Props {
    messages: PlaygroundMessage[]
    sending?: boolean
    /** Seconds since the current send started (drives the waiting row). */
    elapsed?: number
    /** Composer disabled (e.g. no model selected). */
    disabled?: boolean
    onSend: (text: string) => void
    onStop: () => void
  }

  let { messages, sending = false, elapsed = 0, disabled = false, onSend, onStop }: Props =
    $props()

  let draft = $state('')
  let scroller = $state<HTMLDivElement | null>(null)
  let composer = $state<HTMLTextAreaElement | null>(null)
  let stick = $state(true)
  let copiedIdx = $state(-1)

  const SUGGESTIONS = [
    'Write a one-line haiku about a proxy server.',
    'Explain recursion like I am five.',
    'Python function that reverses a string, with a docstring.',
    'What can you do? Answer in three bullet points.',
  ]

  // Follow the stream only while the reader is parked near the bottom;
  // scrolling up to read history must not be yanked back down.
  $effect(() => {
    void messages.map((m) => m.content).join('')
    if (scroller && stick) scroller.scrollTop = scroller.scrollHeight
  })

  function onScroll() {
    if (!scroller) return
    stick = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 120
  }

  function submit() {
    const text = draft.trim()
    if (!text || sending || disabled) return
    draft = ''
    stick = true
    resizeComposer()
    onSend(text)
  }

  function onComposerKeydown(event: KeyboardEvent) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      submit()
    }
  }

  function resizeComposer() {
    requestAnimationFrame(() => {
      if (!composer) return
      composer.style.height = 'auto'
      composer.style.height = Math.min(composer.scrollHeight, 160) + 'px'
    })
  }

  function fillSuggestion(text: string) {
    draft = text
    resizeComposer()
    composer?.focus()
  }

  async function copyReply(idx: number, content: string) {
    try {
      await navigator.clipboard.writeText(content)
      copiedIdx = idx
      setTimeout(() => {
        if (copiedIdx === idx) copiedIdx = -1
      }, 1500)
    } catch {}
  }

  const isPending = (idx: number) =>
    sending && idx === messages.length - 1 && messages[idx]?.role === 'assistant'
</script>

<div class="flex min-h-0 flex-1 flex-col">
  <div
    bind:this={scroller}
    onscroll={onScroll}
    class="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-3 py-4 sm:px-5"
  >
    {#if messages.length === 0}
      <div class="m-auto flex w-full max-w-md flex-col items-center gap-3 text-center">
        <span
          class="flex size-12 items-center justify-center rounded-full bg-primary/10 text-primary"
        >
          <span class="material-symbols-outlined text-[24px]">science</span>
        </span>
        <p class="text-base font-medium text-text-main">Test a model through the real gateway</p>
        <p class="-mt-1.5 text-sm text-text-subtle">
          Every message runs the same serving path as client traffic — routing, proxies, guardrails
          and usage logging included.
        </p>
        <div class="mt-1 flex flex-wrap justify-center gap-1.5">
          {#each SUGGESTIONS as suggestion (suggestion)}
            <button
              type="button"
              onclick={() => fillSuggestion(suggestion)}
              disabled={disabled}
              class="rounded-full border border-border-subtle bg-surface-2 px-3 py-1.5 text-xs text-text-muted transition-colors hover:border-brand-500/50 hover:text-text-main disabled:opacity-50"
            >
              {suggestion}
            </button>
          {/each}
        </div>
      </div>
    {:else}
      {#each messages as message, i (i)}
        {#if message.role === 'user'}
          <div class="flex flex-col items-end">
            <div
              class="max-w-[88%] whitespace-pre-wrap rounded-brand-lg bg-surface-2 px-3.5 py-2.5 text-sm text-text-main ring-1 ring-border-subtle sm:max-w-[75%]"
            >
              {message.content}
            </div>
          </div>
        {:else}
          <div class="flex flex-col gap-1.5">
            <div class="flex items-center gap-1.5 text-xs font-medium text-text-subtle">
              <span class="material-symbols-outlined text-[15px] text-brand-500">smart_toy</span>
              Assistant
            </div>
            {#if isPending(i) && !message.content}
              <div class="flex items-center gap-2 py-1 text-sm text-text-muted" role="status">
                <span class="flex gap-1">
                  <span class="pg-dot"></span><span class="pg-dot"></span><span class="pg-dot"></span>
                </span>
                Waiting for the model… {elapsed.toFixed(1)}s
              </div>
            {:else}
              <div class="pg-body text-sm leading-relaxed text-text-main">
                <!-- eslint-disable-next-line svelte/no-at-html-tags -- renderer escapes all model text first -->
                {@html renderMarkdown(message.content)}
                {#if isPending(i)}
                  <span class="pg-cursor"></span>
                {/if}
              </div>
            {/if}
            {#if message.meta}
              <div class="flex items-center gap-2">
                <span class="text-xs text-text-subtle">{message.meta}</span>
                <button
                  type="button"
                  onclick={() => copyReply(i, message.content)}
                  class="flex items-center gap-1 rounded-brand px-1.5 py-0.5 text-xs text-text-subtle transition-colors hover:bg-surface-2 hover:text-text-main"
                  title="Copy reply"
                >
                  <span class="material-symbols-outlined text-[14px]">
                    {copiedIdx === i ? 'check' : 'content_copy'}
                  </span>
                  {copiedIdx === i ? 'Copied' : 'Copy'}
                </button>
              </div>
            {/if}
          </div>
        {/if}
      {/each}
    {/if}
  </div>

  <div class="border-t border-border-subtle p-3 sm:px-5">
    <div
      class="flex items-end gap-2 rounded-brand-lg border border-border bg-surface-2 px-2 py-1.5 focus-within:border-brand-500"
    >
      <textarea
        bind:this={composer}
        bind:value={draft}
        oninput={resizeComposer}
        onkeydown={onComposerKeydown}
        rows="1"
        placeholder={disabled ? 'No model available' : 'Message the model…'}
        disabled={disabled}
        class="max-h-40 flex-1 resize-none bg-transparent px-1.5 py-1 text-sm text-text-main outline-none disabled:opacity-50"
      ></textarea>
      {#if sending}
        <button
          type="button"
          onclick={onStop}
          class="flex shrink-0 items-center gap-1.5 rounded-brand bg-danger px-3 py-2 text-sm font-medium text-white transition-opacity hover:opacity-90"
        >
          <span class="material-symbols-outlined text-[17px]">stop_circle</span>
          Stop
        </button>
      {:else}
        <button
          type="button"
          onclick={submit}
          disabled={disabled || !draft.trim()}
          class="flex size-8 shrink-0 items-center justify-center rounded-brand bg-primary text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"
          title="Send (Enter)"
        >
          <span class="material-symbols-outlined text-[18px]">arrow_upward</span>
        </button>
      {/if}
    </div>
    <p class="mt-1.5 hidden text-xs text-text-subtle sm:block">
      Enter to send · Shift+Enter for a new line
    </p>
  </div>
</div>

<style>
  .pg-dot {
    width: 6px;
    height: 6px;
    border-radius: 9999px;
    background: currentColor;
    opacity: 0.35;
    animation: pg-pulse 1.2s ease-in-out infinite;
  }
  .pg-dot:nth-child(2) {
    animation-delay: 0.15s;
  }
  .pg-dot:nth-child(3) {
    animation-delay: 0.3s;
  }
  @keyframes pg-pulse {
    0%,
    100% {
      opacity: 0.25;
      transform: translateY(0);
    }
    50% {
      opacity: 0.9;
      transform: translateY(-2px);
    }
  }
  .pg-cursor {
    display: inline-block;
    width: 7px;
    height: 14px;
    margin-left: 2px;
    vertical-align: -2px;
    background: currentColor;
    opacity: 0.6;
    animation: pg-blink 0.9s steps(1) infinite;
  }
  @keyframes pg-blink {
    50% {
      opacity: 0;
    }
  }
  .pg-body {
    /* Long lines, URLs and code never escape the chat column: every
       block wraps inside the thread instead of overflowing it. */
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .pg-body :global(p) {
    margin: 0 0 0.5rem;
  }
  .pg-body :global(p:last-child) {
    margin-bottom: 0;
  }
  .pg-body :global(.pg-pre) {
    margin: 0.5rem 0;
    padding: 0.6rem 0.75rem;
    border-radius: 6px;
    max-width: 100%;
    background: var(--color-surface-2, rgba(127, 127, 127, 0.12));
    font-family: ui-monospace, monospace;
    font-size: 12px;
    line-height: 1.5;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .pg-body :global(.pg-quote) {
    margin: 0.5rem 0;
    padding: 0.35rem 0.75rem;
    border-left: 3px solid var(--color-brand-500, #06b6d4);
    background: var(--color-surface-2, rgba(127, 127, 127, 0.08));
    border-radius: 0 6px 6px 0;
    color: inherit;
    opacity: 0.92;
  }
  .pg-body :global(.pg-code) {
    padding: 0.05rem 0.3rem;
    border-radius: 4px;
    background: var(--color-surface-2, rgba(127, 127, 127, 0.12));
    font-family: ui-monospace, monospace;
    font-size: 12px;
  }
  .pg-body :global(.pg-ul) {
    margin: 0 0 0.5rem;
    padding-left: 1.1rem;
    list-style: disc;
  }
  .pg-body :global(.pg-ol) {
    margin: 0 0 0.5rem;
    padding-left: 1.1rem;
    list-style: decimal;
  }
  .pg-body :global(.pg-h1),
  .pg-body :global(.pg-h2),
  .pg-body :global(.pg-h3) {
    font-weight: 600;
  }
</style>
