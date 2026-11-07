<script lang="ts">
  import { api, type JailbreakMatch } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Modal from '../../lib/ui/Modal.svelte'

  interface Props {
    onClose: () => void
  }

  let { onClose }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // #3 Test-match: type a model id, see which preset wins and why. The panel
  // is a modal, not a card of its own: it is used occasionally, and a full
  // card cost as much vertical space as the preset library above it.
  let testMatchId = $state('')
  let testMatchLoading = $state(false)
  let testMatchResult = $state<JailbreakMatch | null>(null)
  let testMatchError = $state('')
  // #2 Payload preview: the exact outbound body one request would carry.
  let previewOpen = $state(false)
  let previewLoading = $state(false)
  let previewError = $state('')
  let previewData = $state<{
    injected: boolean
    reason: string
    presetId?: string
    presetName?: string
    prompt?: string
    messages: { role: string; content: string }[]
  } | null>(null)

  // #3 Test-match: ask the gateway which preset wins for a typed id.
  async function runTestMatch() {
    const id = testMatchId.trim()
    if (!id) {
      testMatchError = 'Enter a model, provider, or combo id.'
      return
    }
    testMatchLoading = true
    testMatchError = ''
    testMatchResult = null
    try {
      testMatchResult = await api.explainJailbreak(id)
    } catch (e) {
      testMatchError = errorMessage(e)
    } finally {
      testMatchLoading = false
    }
  }
  // #2 Payload preview: build the exact outbound body for the tested id.
  async function togglePreview() {
    if (previewOpen) {
      previewOpen = false
      return
    }
    previewOpen = true
    if (!testMatchId.trim()) return
    previewLoading = true
    previewError = ''
    previewData = null
    try {
      previewData = await api.previewJailbreak(testMatchId.trim(), [
        { role: 'user', content: 'Example user message.' },
      ])
    } catch (e) {
      previewError = errorMessage(e)
    } finally {
      previewLoading = false
    }
  }
</script>

<Modal isOpen={true} onClose={onClose} title="Test match" size="md" icon="troubleshoot">
  <!-- One rhythm for the whole panel: the input row, the verdict card, and
       the payload preview are siblings, so they are spaced as siblings
       rather than left touching. -->
  <div class="space-y-4">
    <div class="flex flex-wrap items-center gap-3">
      <input
        type="text"
        bind:value={testMatchId}
        placeholder="providerx/deepseek-4-pro, or a combo name"
        aria-label="Model id to test"
        onkeydown={(e) => e.key === 'Enter' && runTestMatch()}
        class="min-w-[280px] flex-1 rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 font-code text-sm text-text-main placeholder:text-text-subtle"
      />
      <Button variant="primary" size="md" onclick={runTestMatch} disabled={testMatchLoading}>
        {testMatchLoading ? 'Checking…' : 'Check'}
      </Button>
    </div>
    {#if testMatchError}<p class="text-xs text-error">{testMatchError}</p>{/if}
    {#if testMatchResult}
      <div
        class="space-y-1.5 rounded-brand border p-3 {testMatchResult.injected
          ? 'border-success/40 bg-success/5'
          : 'border-border bg-surface-2'}"
      >
        <div class="flex items-center gap-2">
          <span class="material-symbols-outlined text-[16px] {testMatchResult.injected ? 'text-success' : 'text-text-subtle'}">
            {testMatchResult.injected ? 'check_circle' : 'block'}
          </span>
          <span class="text-sm font-medium text-text-main">
            {testMatchResult.injected ? 'Injected' : 'Not injected'}
          </span>
          <span class="font-code text-[11px] text-text-subtle">{testMatchResult.reason}</span>
        </div>
        {#if testMatchResult.presetName || testMatchResult.presetId}
          <p class="text-xs text-text-muted">
            Preset: <span class="text-text-main">{testMatchResult.presetName || testMatchResult.presetId}</span>
          </p>
        {/if}
        {#if testMatchResult.keyword}
          <p class="text-xs text-text-muted">
            Keyword: <span class="font-code text-text-main">{testMatchResult.keyword}</span>
          </p>
        {/if}
        {#if testMatchResult.matchedId}
          <p class="text-xs text-text-muted">
            Matched id: <span class="font-code text-text-main">{testMatchResult.matchedId}</span>
          </p>
        {/if}
        {#if testMatchResult.detail}
          <p class="text-[11px] text-text-subtle">{testMatchResult.detail}</p>
        {/if}
      </div>
      {#if testMatchResult?.injected}
        <div class="space-y-2">
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-text-muted">Outbound payload preview</span>
            <button
              type="button"
              onclick={togglePreview}
              class="text-[11px] text-text-muted hover:text-brand-500 hover:underline"
            >
              {previewOpen ? 'Hide' : 'Show payload'}
            </button>
          </div>
          {#if previewOpen}
            {#if previewLoading}
              <p class="text-xs text-text-muted">Building preview…</p>
            {:else if previewError}
              <p class="text-xs text-error">{previewError}</p>
            {:else if previewData}
              <div class="space-y-2">
                {#if previewData.prompt}
                  <div class="rounded-brand border border-border-subtle bg-surface-2 p-3">
                    <div class="mb-1 text-[10px] font-semibold text-text-subtle uppercase">
                      Injected system prompt {previewData.presetName ? `· ${previewData.presetName}` : ''}
                    </div>
                    <pre class="max-h-40 overflow-y-auto font-code text-xs break-all whitespace-pre-wrap text-text-main">{previewData.prompt}</pre>
                  </div>
                {/if}
                <div class="rounded-brand border border-border-subtle bg-surface-2 p-3">
                  <div class="mb-1 text-[10px] font-semibold text-text-subtle uppercase">Final messages array</div>
                  <pre class="max-h-60 overflow-y-auto font-code text-xs break-all whitespace-pre-wrap text-text-main">{JSON.stringify(previewData.messages, null, 2)}</pre>
                </div>
              </div>
            {/if}
          {/if}
        </div>
      {/if}
    {/if}
  </div>
</Modal>
