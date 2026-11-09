<script lang="ts">
  import { onMount } from 'svelte'
  import {
    api,
    type JailbreakDoc,
    type JailbreakMatch,
    type PlaygroundModelEntry,
  } from '../../api/client'
  import Modal from '../../lib/ui/Modal.svelte'
  import ModelPicker from './ModelPicker.svelte'
  import PromptPanel from './PromptPanel.svelte'
  import ParamsPanel from './ParamsPanel.svelte'
  import ChatThread from './ChatThread.svelte'
  import type { PlaygroundMessage } from './types'

  let entries = $state<PlaygroundModelEntry[]>([])
  let presets = $state<JailbreakDoc[]>([])
  let standingMatch = $state<JailbreakMatch | null>(null)
  let loadError = $state('')

  let selectedModel = $state('')
  let systemPrompt = $state('')
  let presetId = $state('')
  let applyStandingPersona = $state(true)
  let temperature = $state(1)
  let maxTokens = $state('')

  let messages = $state<PlaygroundMessage[]>([])
  let sending = $state(false)
  let controller: AbortController | null = null

  let settingsOpen = $state(false)

  // Fit the shell exactly: the view must fill <main>'s content box so
  // the page itself never scrolls — the chat thread is the only
  // scroller. The shell's TopBar sits above <main>, so a viewport calc
  // overshoots by its height; measure the real box instead (and lock
  // main's own scrolling while mounted, restored on the way out).
  let rootEl = $state<HTMLDivElement | null>(null)
  let viewH = $state(0)
  $effect(() => {
    const main = rootEl?.closest('main')
    if (!main) return
    const prevOverflow = main.style.overflow
    main.style.overflow = 'hidden'
    const update = () => {
      viewH = Math.max(0, main.clientHeight - 24)
    }
    update()
    const ro = new ResizeObserver(update)
    ro.observe(main)
    return () => {
      ro.disconnect()
      main.style.overflow = prevOverflow
    }
  })

  // Live clock for the waiting indicator: ticks while a send is in
  // flight so the thread can show how long the model has been thinking
  // (first tokens are routinely slow on this gateway — a silent wait
  // reads as a hang).
  let sendStartedAt = $state(0)
  let tickNow = $state(0)
  $effect(() => {
    if (!sending) return
    tickNow = Date.now()
    const iv = setInterval(() => (tickNow = Date.now()), 100)
    return () => clearInterval(iv)
  })
  const elapsed = $derived(
    sending && sendStartedAt ? Math.max(0, (tickNow - sendStartedAt) / 1000) : 0
  )

  const selectedEntry = $derived(entries.find((e) => e.model === selectedModel))

  // The standing Persona resolution for the selected model, asked from
  // the same resolver the serving path uses (keyword rules + excludes),
  // refreshed whenever the selection changes. A failed lookup is
  // cosmetic: the chip simply reads as no standing prompt.
  let standingSeq = 0
  $effect(() => {
    const model = selectedModel
    const seq = ++standingSeq
    if (!model) {
      standingMatch = null
      return
    }
    api
      .explainJailbreak(model)
      .then((m) => {
        if (seq === standingSeq) standingMatch = m
      })
      .catch(() => {
        if (seq === standingSeq) standingMatch = null
      })
  })
  const standingPresetName = $derived.by<string | null>(() => {
    if (!standingMatch?.injected) return null
    return standingMatch.presetName || standingMatch.presetId || null
  })

  // The toolbar's prompt chip names the layer that will actually fire:
  // session prompt > chosen preset > standing Persona > none.
  const promptChipLabel = $derived.by(() => {
    if (systemPrompt.trim()) return 'Session prompt'
    if (presetId) return presets.find((p) => p.id === presetId)?.name || 'Preset'
    if (standingPresetName && applyStandingPersona) return `Standing: ${standingPresetName}`
    return 'No prompt'
  })
  const promptChipActive = $derived(promptChipLabel !== 'No prompt')
  const paramsChipLabel = $derived(
    `Temp ${temperature.toFixed(1)}${maxTokens.trim() ? ` · Max ${maxTokens.trim()}` : ''}`
  )

  onMount(load)

  async function load() {
    loadError = ''
    const [modelsRes, presetsRes] = await Promise.allSettled([
      api.getPlaygroundModels(),
      api.listJailbreakPresets(),
    ])
    if (modelsRes.status === 'fulfilled') {
      entries = modelsRes.value
      if (!selectedModel && entries.length > 0) selectedModel = entries[0].model
    } else {
      loadError = 'Failed to load the model inventory.'
    }
    if (presetsRes.status === 'fulfilled') {
      presets = presetsRes.value.filter((p) => p.id)
    }
  }

  /** Extract the assistant text (or an error) from one SSE payload. */
  function chunkText(payload: string): { text: string; error: string } {
    if (payload === '[DONE]') return { text: '', error: '' }
    let chunk: {
      choices?: { delta?: { content?: string }; message?: { content?: string } }[]
      error?: { message?: string }
    }
    try {
      chunk = JSON.parse(payload)
    } catch {
      return { text: '', error: '' }
    }
    if (chunk.error?.message) return { text: '', error: chunk.error.message }
    const choice = chunk.choices?.[0]
    return { text: choice?.delta?.content ?? choice?.message?.content ?? '', error: '' }
  }

  async function send(text: string) {
    if (sending || !selectedModel) return
    const history: PlaygroundMessage[] = [...messages, { role: 'user', content: text }]
    messages = [...history, { role: 'assistant', content: '' }]
    const reply = messages[messages.length - 1]
    sending = true
    sendStartedAt = Date.now()
    controller = new AbortController()
    const started = performance.now()
    const parsedMax = Number(maxTokens)
    try {
      const { response, requestId } = await api.playgroundChatStream(
        {
          model: selectedModel,
          messages: history.map((m) => ({ role: m.role, content: m.content })),
          systemPrompt: systemPrompt.trim() ? systemPrompt : undefined,
          presetId: presetId || undefined,
          applyStandingPersona,
          temperature,
          maxTokens: maxTokens.trim() && parsedMax > 0 ? parsedMax : undefined,
          stream: true,
        },
        controller.signal
      )
      const reader = response.body?.getReader()
      if (!reader) throw new Error('The gateway returned no stream.')
      const decoder = new TextDecoder()
      let buffer = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        const frames = buffer.split('\n')
        buffer = frames.pop() || ''
        for (const frame of frames) {
          const line = frame.trim()
          if (!line.startsWith('data:')) continue
          const { text: delta, error } = chunkText(line.slice(5).trim())
          if (error) throw new Error(error)
          if (delta) reply.content += delta
        }
      }
      if (!reply.content) reply.content = '(empty response)'
      const seconds = ((performance.now() - started) / 1000).toFixed(1)
      reply.meta = [selectedModel, `${seconds}s`, requestId ? `req ${requestId}` : '']
        .filter(Boolean)
        .join(' · ')
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        const seconds = ((performance.now() - started) / 1000).toFixed(1)
        reply.meta = [selectedModel, `${seconds}s`, 'stopped'].filter(Boolean).join(' · ')
      } else {
        reply.content =
          (reply.content ? reply.content + '\n\n' : '') +
          `Error: ${err instanceof Error ? err.message : 'request failed'}`
      }
    } finally {
      sending = false
      controller = null
    }
  }

  function stop() {
    controller?.abort()
  }

  function clear() {
    if (!sending) messages = []
  }
</script>

<div
  bind:this={rootEl}
  class="flex flex-col gap-2.5"
  style:height={viewH ? `${viewH}px` : 'calc(100dvh - 6rem)'}
>
  <div class="flex items-center gap-2">
    <ModelPicker entries={entries} bind:value={selectedModel} disabled={sending} />
    <button
      type="button"
      onclick={() => (settingsOpen = true)}
      class="flex shrink-0 items-center gap-1.5 rounded-brand-lg border px-2.5 py-2 text-xs transition-colors {promptChipActive
        ? 'border-brand-500/50 bg-primary/10 text-primary'
        : 'border-border bg-surface text-text-muted hover:text-text-main'}"
      title="Session prompt and Persona preset"
    >
      <span class="material-symbols-outlined text-[16px]">edit_note</span>
      <span class="hidden max-w-44 truncate md:inline">{promptChipLabel}</span>
    </button>
    <button
      type="button"
      onclick={() => (settingsOpen = true)}
      class="flex shrink-0 items-center gap-1.5 rounded-brand-lg border border-border bg-surface px-2.5 py-2 text-xs text-text-muted transition-colors hover:text-text-main"
      title="Sampling parameters"
    >
      <span class="material-symbols-outlined text-[16px]">tune</span>
      <span class="hidden md:inline">{paramsChipLabel}</span>
    </button>
    <div class="flex-1"></div>
    <button
      type="button"
      onclick={clear}
      disabled={sending || messages.length === 0}
      class="flex shrink-0 items-center gap-1.5 rounded-brand-lg px-2.5 py-2 text-xs text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main disabled:cursor-not-allowed disabled:opacity-40"
      title="Clear the conversation"
    >
      <span class="material-symbols-outlined text-[16px]">delete_sweep</span>
      <span class="hidden sm:inline">Clear</span>
    </button>
  </div>

  {#if loadError}
    <p class="text-xs text-danger">{loadError}</p>
  {:else if selectedEntry?.type === 'model' && selectedEntry.connectionName}
    <p class="truncate text-xs text-text-subtle">
      Served by {selectedEntry.provider} — the gateway picks the account by its routing strategy.
    </p>
  {/if}

  <div
    class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-brand-lg border border-border-subtle bg-surface"
  >
    <ChatThread
      {messages}
      {sending}
      {elapsed}
      disabled={entries.length === 0 || !selectedModel}
      onSend={send}
      onStop={stop}
    />
  </div>
</div>

<Modal
  isOpen={settingsOpen}
  onClose={() => (settingsOpen = false)}
  title="Session settings"
  icon="tune"
  size="lg"
>
  <div class="flex max-h-[70vh] flex-col gap-5 overflow-y-auto pr-1">
    <section class="flex flex-col gap-2">
      <h3 class="text-xs font-medium uppercase tracking-wide text-text-subtle">Prompt</h3>
      <PromptPanel
        bind:systemPrompt
        {presets}
        bind:presetId
        {standingPresetName}
        bind:applyStandingPersona
      />
    </section>
    <section class="flex flex-col gap-2 border-t border-border-subtle pt-4">
      <h3 class="text-xs font-medium uppercase tracking-wide text-text-subtle">Parameters</h3>
      <ParamsPanel bind:temperature bind:maxTokens />
    </section>
  </div>
</Modal>
