<script lang="ts">
  import { onMount } from 'svelte'
  import {
    api,
    type JailbreakDoc,
    type JailbreakModelConfig,
    type PlaygroundModelEntry,
  } from '../../api/client'
  import Card from '../../lib/ui/Card.svelte'
  import ModelPicker from './ModelPicker.svelte'
  import PromptPanel from './PromptPanel.svelte'
  import ParamsPanel from './ParamsPanel.svelte'
  import ChatThread from './ChatThread.svelte'
  import type { PlaygroundMessage } from './types'

  let entries = $state<PlaygroundModelEntry[]>([])
  let presets = $state<JailbreakDoc[]>([])
  let modelConfigs = $state<Record<string, JailbreakModelConfig>>({})
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

  const selectedEntry = $derived(entries.find((e) => e.model === selectedModel))

  // The standing Persona binding for the selected model: the Persona tab
  // keys its map by the id the operator bound (wire id or bare model), so
  // try the wire id first, then the bare segment.
  const standingConfig = $derived.by<JailbreakModelConfig | null>(() => {
    if (!selectedModel) return null
    const bare = selectedModel.includes('/')
      ? selectedModel.slice(selectedModel.lastIndexOf('/') + 1)
      : ''
    for (const key of [selectedModel, bare]) {
      const cfg = key ? modelConfigs[key] : undefined
      if (cfg?.enabled && cfg.presetId) return cfg
    }
    return null
  })
  const standingPresetName = $derived.by<string | null>(() => {
    if (!standingConfig) return null
    const preset = presets.find((p) => p.id === standingConfig.presetId)
    return preset?.name || standingConfig.presetId
  })

  onMount(load)

  async function load() {
    loadError = ''
    const [modelsRes, presetsRes, configsRes] = await Promise.allSettled([
      api.getPlaygroundModels(),
      api.listJailbreakPresets(),
      api.listJailbreakModels(),
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
    if (configsRes.status === 'fulfilled') {
      modelConfigs = configsRes.value
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

<div class="flex flex-col gap-4 lg:flex-row lg:items-start">
  <div class="flex w-full shrink-0 flex-col gap-4 lg:w-80">
    <Card title="Model" padding="sm">
      <ModelPicker entries={entries} bind:value={selectedModel} disabled={sending} />
      {#if loadError}
        <p class="mt-2 text-xs text-danger">{loadError}</p>
      {/if}
      {#if selectedEntry?.type === 'model' && selectedEntry.connectionName}
        <p class="mt-2 text-xs text-text-subtle">
          Served by {selectedEntry.provider} — the gateway picks the account by its routing
          strategy.
        </p>
      {/if}
    </Card>

    <Card title="Prompt" padding="sm">
      <PromptPanel
        bind:systemPrompt
        {presets}
        bind:presetId
        {standingPresetName}
        bind:applyStandingPersona
      />
    </Card>

    <Card title="Parameters" padding="sm">
      <ParamsPanel bind:temperature bind:maxTokens />
    </Card>
  </div>

  <Card class="flex min-w-0 flex-1 flex-col" padding="sm">
    <ChatThread
      {messages}
      {sending}
      disabled={entries.length === 0 || !selectedModel}
      onSend={send}
      onStop={stop}
      onClear={clear}
    />
  </Card>
</div>
