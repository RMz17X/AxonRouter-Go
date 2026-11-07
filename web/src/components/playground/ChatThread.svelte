<script lang="ts">
  import Button from '../../lib/ui/Button.svelte'
  import type { PlaygroundMessage } from './types'

  interface Props {
    messages: PlaygroundMessage[]
    sending?: boolean
    /** Composer disabled (e.g. no model selected). */
    disabled?: boolean
    onSend: (text: string) => void
    onStop: () => void
    onClear: () => void
  }

  let { messages, sending = false, disabled = false, onSend, onStop, onClear }: Props = $props()

  let draft = $state('')
  let scroller = $state<HTMLDivElement | null>(null)

  // Keep the newest content in view while a reply streams in.
  $effect(() => {
    void messages.map((m) => m.content).join('')
    if (scroller) scroller.scrollTop = scroller.scrollHeight
  })

  function submit() {
    const text = draft.trim()
    if (!text || sending || disabled) return
    draft = ''
    onSend(text)
  }

  function onComposerKeydown(event: KeyboardEvent) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault()
      submit()
    }
  }
</script>

<div class="flex min-h-0 flex-1 flex-col gap-3">
  <div
    bind:this={scroller}
    class="flex min-h-[320px] flex-1 flex-col gap-3 overflow-y-auto rounded-brand border border-border-subtle bg-surface-2 p-3"
  >
    {#if messages.length === 0}
      <p class="m-auto text-sm text-text-subtle">
        Pick a model and send a message — it runs through the same serving path as client traffic.
      </p>
    {:else}
      {#each messages as message, i (i)}
        <div class="flex flex-col {message.role === 'user' ? 'items-end' : 'items-start'}">
          <div
            class="max-w-[85%] whitespace-pre-wrap rounded-brand-lg px-3 py-2 text-sm {message.role ===
            'user'
              ? 'bg-primary text-white'
              : 'bg-surface text-text-main border border-border-subtle'}"
          >
            {message.content || '…'}
          </div>
          {#if message.role === 'assistant' && message.meta}
            <span class="mt-1 text-xs text-text-subtle">{message.meta}</span>
          {/if}
        </div>
      {/each}
    {/if}
  </div>

  <div class="flex items-end gap-2">
    <textarea
      bind:value={draft}
      onkeydown={onComposerKeydown}
      rows="2"
      placeholder={disabled ? 'No model available' : 'Message… (Enter to send, Shift+Enter for a new line)'}
      disabled={disabled}
      class="flex-1 px-3 py-2 rounded-brand bg-surface-2 border border-border-subtle text-text-main text-sm outline-none focus:border-brand-500 resize-none disabled:opacity-50"
    ></textarea>
    {#if sending}
      <Button variant="danger" size="md" onclick={onStop}>Stop</Button>
    {:else}
      <Button variant="primary" size="md" onclick={submit} disabled={disabled || !draft.trim()}>
        Send
      </Button>
    {/if}
    <Button variant="ghost" size="md" onclick={onClear} disabled={sending || messages.length === 0}>
      Clear
    </Button>
  </div>
</div>
