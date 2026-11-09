<script lang="ts">
  import { api, type APIKey, type APIKeyPolicy, type Plan } from '../../api/client'
  import { isNavEntryHidden } from '../../lib/navVisibility.svelte'
  import { pushEscapeHandler } from '../../lib/ui/escapeStack'
  import ModelPickerModal from '../combos/ModelPickerModal.svelte'
  import { Layers, Loader2, Plus, X } from 'lucide-svelte'

  interface Props {
    isOpen: boolean
    apiKeys?: APIKey[]
    onClose: () => void
    onCreated: (key: string) => void
    onRefresh?: () => void
  }

  let { isOpen, onClose, onCreated, onRefresh }: Props = $props()

  // --- Single Key Form State ---
  let name = $state('')
  let planId = $state('')
  let plans = $state<Plan[]>([])
  let plansHidden = $derived(isNavEntryHidden('plans'))

  let rpm = $state(0)
  // TPM in M (Millions of tokens). e.g. 1 = 1M = 1,000,000 tokens
  let tpmM = $state(0)
  let concurrency = $state(0)
  // Lifetime token quota in M (Millions of tokens). e.g. 50 = 50M = 50,000,000 tokens
  let tokenQuotaM = $state(0)
  let queueTimeoutMs = $state(0)

  let expiresLocal = $state('')
  let metadata = $state('')
  let models = $state<string[]>([])
  let modelDraft = $state('')
  let isModelPickerOpen = $state(false)

  let isSubmittingKey = $state(false)
  let singleError = $state<string | null>(null)

  $effect(() => {
    if (isOpen && !plansHidden && plans.length === 0) {
      void loadPlans()
    }
  })

  async function loadPlans() {
    try {
      const res = await api.getPlans()
      plans = res.plans ?? []
    } catch {
      plans = []
    }
  }

  function toRFC3339(local: string): string {
    if (!local) return ''
    const parsed = new Date(local)
    if (Number.isNaN(parsed.getTime())) return ''
    return parsed.toISOString()
  }

  function nonNegative(value: number): number {
    return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
  }

  function addModelPattern() {
    const value = modelDraft.trim()
    if (!value || models.includes(value)) return
    models = [...models, value]
    modelDraft = ''
  }

  function resetForm() {
    name = ''
    planId = ''
    rpm = 0
    tpmM = 0
    concurrency = 0
    tokenQuotaM = 0
    queueTimeoutMs = 0
    expiresLocal = ''
    metadata = ''
    models = []
    modelDraft = ''
    singleError = null
  }

  function handleClose() {
    if (isSubmittingKey) return
    resetForm()
    onClose()
  }

  async function handleSingleSubmit(e: SubmitEvent) {
    e.preventDefault()
    singleError = null
    const trimmedName = name.trim()
    if (!trimmedName) {
      singleError = 'Key Name is required'
      return
    }

    const metadataValue = metadata.trim()
    if (metadataValue) {
      try {
        const parsed = JSON.parse(metadataValue)
        if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
          singleError = 'Metadata must be a JSON object, e.g. {"customerId":"acme"}'
          return
        }
      } catch {
        singleError = 'Metadata is not valid JSON'
        return
      }
    }

    let createdKeyId: string | null = null
    try {
      isSubmittingKey = true
      // 1. Create API key
      const res = await api.createApiKey({ name: trimmedName })
      if (!res.key || !res.id) {
        throw new Error('Failed to create key: server returned no key or id')
      }
      createdKeyId = res.id

      // 2. Set policy (Rate limits in M converted to raw tokens, Plan, Expiry, Metadata)
      const policy: APIKeyPolicy = {
        name: trimmedName,
        planId: planId || undefined,
        rateLimitRpm: nonNegative(rpm),
        rateLimitTpm: Math.round(nonNegative(tpmM) * 1_000_000),
        rateLimitConcurrency: nonNegative(concurrency),
        tokenQuota: Math.round(nonNegative(tokenQuotaM) * 1_000_000),
        queueTimeoutMs: nonNegative(queueTimeoutMs),
        expiresAt: toRFC3339(expiresLocal),
        metadata: metadataValue,
      }
      await api.updateApiKeyPolicy(res.id, policy)

      // 3. Set allowed models if any were selected
      if (models.length > 0) {
        await api.setApiKeyModels(res.id, models)
      }

      const mintedKey = res.key
      createdKeyId = null
      resetForm()
      onCreated(mintedKey)
      onRefresh?.()
      onClose()
    } catch (err) {
      // Roll back the partially created key so a rejected policy or allowlist
      // never leaves an unrestricted orphan key in the database.
      if (createdKeyId) {
        await api.deleteApiKey(createdKeyId).catch(() => {})
      }
      singleError = err instanceof Error ? err.message : String(err)
    } finally {
      isSubmittingKey = false
    }
  }

  $effect(() => {
    if (!isOpen) return
    return pushEscapeHandler(() => handleClose())
  })
</script>

{#if isOpen}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="fixed inset-0 z-[100] flex items-center justify-center bg-black/80 p-4"
    onclick={(e) => {
      if (e.target === e.currentTarget) handleClose()
    }}
  >
    <div
      class="w-full max-w-2xl max-h-[92vh] overflow-y-auto rounded-2xl bg-surface border border-border shadow-2xl flex flex-col"
    >
      <!-- Header -->
      <div class="sticky top-0 bg-surface border-b border-border px-6 py-4 flex items-center justify-between z-10">
        <div class="min-w-0">
          <h2 class="font-headline text-base font-bold text-text-main">Create API Key</h2>
          <p class="text-xs text-text-subtle mt-0.5">
            Configure full policy, token limits in M, and model access.
          </p>
        </div>
        <button
          type="button"
          onclick={handleClose}
          aria-label="Close dialog"
          class="p-1.5 rounded-lg text-text-subtle hover:text-text-main hover:bg-surface-2 transition cursor-pointer"
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <div class="p-6 space-y-5">
        <!-- Single Key Full-Featured Form -->
        <form onsubmit={handleSingleSubmit} class="space-y-5 text-xs">
          <!-- Name & Plan -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div class="space-y-1.5">
              <label for="new-key-name" class="block font-semibold text-text-main">
                Key Name <span class="text-danger">*</span>
              </label>
              <input
                id="new-key-name"
                type="text"
                bind:value={name}
                required
                placeholder="e.g. Production Client Key"
                class="w-full bg-surface-2 border border-border rounded-lg px-3 py-2 text-xs text-text-main focus:outline-none focus:border-brand-500"
              />
            </div>

            {#if !plansHidden}
              <div class="space-y-1.5">
                <label for="new-key-plan" class="block font-semibold text-text-muted">
                  Plan Template
                </label>
                <select
                  id="new-key-plan"
                  bind:value={planId}
                  class="w-full bg-surface-2 border border-border rounded-lg px-3 py-2 text-xs text-text-main focus:outline-none focus:border-brand-500"
                >
                  <option value="">No plan — direct limits only</option>
                  {#each plans as plan (plan.id)}
                    <option value={plan.id}>{plan.name}</option>
                  {/each}
                </select>
              </div>
            {/if}
          </div>

          <!-- Rate Limits with M units -->
          <div class="rounded-xl border border-border/80 bg-surface-2/30 p-4 space-y-3">
            <div class="flex items-center justify-between">
              <div>
                <h3 class="font-headline text-xs font-bold text-text-main">Rate Limits & Budget</h3>
                <p class="text-[11px] text-text-subtle">
                  Set limits per key. Token inputs use <span class="font-bold text-brand-500 font-mono">M (Millions)</span> for easy entry. 0 = unlimited.
                </p>
              </div>
            </div>

            <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <label for="create-rpm" class="block font-semibold text-text-muted mb-1 text-[11px]">
                  Requests / min
                </label>
                <input
                  id="create-rpm"
                  type="number"
                  min="0"
                  step="1"
                  bind:value={rpm}
                  class="w-full bg-surface border border-border rounded-lg px-3 py-1.5 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
                />
                <p class="text-text-subtle text-[10px] mt-1 font-mono">
                  {rpm > 0 ? `${rpm.toLocaleString()} req/min` : '0 = Unlimited'}
                </p>
              </div>

              <div>
                <label for="create-tpm" class="block font-semibold text-text-muted mb-1 text-[11px] flex items-center justify-between">
                  <span>Tokens / min</span>
                  <span class="px-1 py-0.2 rounded bg-brand-500/10 text-brand-500 font-bold text-[10px]">M</span>
                </label>
                <div class="relative">
                  <input
                    id="create-tpm"
                    type="number"
                    min="0"
                    step="0.01"
                    bind:value={tpmM}
                    class="w-full bg-surface border border-border rounded-lg pl-3 pr-8 py-1.5 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
                  />
                  <span class="absolute right-2.5 top-1/2 -translate-y-1/2 font-bold text-text-muted text-[11px] pointer-events-none">M</span>
                </div>
                <p class="text-text-subtle text-[10px] mt-1 font-mono">
                  {tpmM > 0 ? `${Math.round(tpmM * 1_000_000).toLocaleString()} tokens/min` : '0 = Unlimited'}
                </p>
              </div>

              <div>
                <label for="create-conc" class="block font-semibold text-text-muted mb-1 text-[11px]">
                  Concurrency
                </label>
                <input
                  id="create-conc"
                  type="number"
                  min="0"
                  step="1"
                  bind:value={concurrency}
                  class="w-full bg-surface border border-border rounded-lg px-3 py-1.5 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
                />
                <p class="text-text-subtle text-[10px] mt-1 font-mono">
                  {concurrency > 0 ? `${concurrency} concurrent` : '0 = Unlimited'}
                </p>
              </div>
            </div>

            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
              <div>
                <label for="create-quota" class="block font-semibold text-text-muted mb-1 text-[11px] flex items-center justify-between">
                  <span>Lifetime token quota</span>
                  <span class="px-1 py-0.2 rounded bg-brand-500/10 text-brand-500 font-bold text-[10px]">M</span>
                </label>
                <div class="relative">
                  <input
                    id="create-quota"
                    type="number"
                    min="0"
                    step="0.1"
                    bind:value={tokenQuotaM}
                    class="w-full bg-surface border border-border rounded-lg pl-3 pr-8 py-1.5 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
                  />
                  <span class="absolute right-2.5 top-1/2 -translate-y-1/2 font-bold text-text-muted text-[11px] pointer-events-none">M</span>
                </div>
                <p class="text-text-subtle text-[10px] mt-1 font-mono">
                  {tokenQuotaM > 0 ? `${Math.round(tokenQuotaM * 1_000_000).toLocaleString()} tokens lifetime` : '0 = Unlimited'}
                </p>
              </div>

              <div>
                <label for="create-queue" class="block font-semibold text-text-muted mb-1 text-[11px]">
                  Queue timeout (ms)
                </label>
                <input
                  id="create-queue"
                  type="number"
                  min="0"
                  step="1"
                  bind:value={queueTimeoutMs}
                  class="w-full bg-surface border border-border rounded-lg px-3 py-1.5 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
                />
                <p class="text-text-subtle text-[10px] mt-1 font-mono">
                  {queueTimeoutMs > 0 ? `${queueTimeoutMs} ms` : '0 = Server default'}
                </p>
              </div>
            </div>
          </div>

          <!-- Model Access (Allowlist) with ModelPickerModal -->
          <div class="rounded-xl border border-border/80 bg-surface-2/30 p-4 space-y-3">
            <div class="flex items-start justify-between gap-3">
              <div>
                <h3 class="font-headline text-xs font-bold text-text-main">Model Access</h3>
                <p class="text-[11px] text-text-subtle mt-0.5">
                  Select allowed models via the picker or type custom patterns with wildcard (<span class="font-code">*</span>). Empty = allow all models.
                </p>
              </div>
              <button
                type="button"
                onclick={() => (isModelPickerOpen = true)}
                class="px-2.5 py-1.5 rounded-lg bg-brand-500/10 border border-brand-500/30 text-[11px] font-semibold text-brand-500 hover:bg-brand-500/20 transition flex items-center gap-1.5 cursor-pointer shrink-0"
              >
                <Layers class="w-3.5 h-3.5" />
                <span>Browse Models</span>
              </button>
            </div>

            <div class="flex gap-2">
              <input
                type="text"
                bind:value={modelDraft}
                onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), addModelPattern())}
                placeholder="e.g. anthropic/* or e2e/allowed-*"
                class="flex-1 bg-surface border border-border rounded-lg px-3 py-1.5 font-code text-[11px] text-text-main focus:outline-none focus:border-brand-500"
              />
              <button
                type="button"
                onclick={addModelPattern}
                class="px-3 py-1.5 rounded-lg bg-surface-3 border border-border text-[11px] text-text-main hover:border-brand-500/40 transition cursor-pointer"
              >
                Add Pattern
              </button>
            </div>

            {#if models.length > 0}
              <div class="space-y-1.5">
                <div class="flex items-center justify-between text-[11px] text-text-muted">
                  <span>Selected Models ({models.length}):</span>
                  <button
                    type="button"
                    onclick={() => (models = [])}
                    class="text-danger hover:underline cursor-pointer text-[10px]"
                  >
                    Clear all
                  </button>
                </div>
                <div class="flex flex-wrap gap-1.5 max-h-36 overflow-y-auto p-1 bg-surface/50 border border-border/50 rounded-lg">
                  {#each models as pattern, i (pattern)}
                    <span
                      class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md bg-brand-500/10 border border-brand-500/25 font-code text-[11px] text-brand-500"
                    >
                      {pattern}
                      <button
                        type="button"
                        onclick={() => (models = models.filter((_, idx) => idx !== i))}
                        aria-label="Remove {pattern}"
                        class="hover:text-danger transition cursor-pointer"
                      >
                        <X class="w-3 h-3" />
                      </button>
                    </span>
                  {/each}
                </div>
              </div>
            {:else}
              <p class="text-text-subtle font-code text-[11px]">
                No restrictions — every model is allowed.
              </p>
            {/if}
          </div>

          <!-- Expiry & Resale Metadata -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div class="space-y-1.5">
              <label for="create-expiry" class="block font-semibold text-text-main">
                Contract Expiry
              </label>
              <input
                id="create-expiry"
                type="datetime-local"
                bind:value={expiresLocal}
                class="w-full bg-surface-2 border border-border rounded-lg px-3 py-1.5 font-code text-xs text-text-main focus:outline-none focus:border-brand-500"
              />
              <p class="text-text-subtle text-[10px]">
                Leave blank for a key that never expires.
              </p>
            </div>

            <div class="space-y-1.5">
              <label for="create-meta" class="block font-semibold text-text-main">
                Resale Metadata (JSON)
              </label>
              <textarea
                id="create-meta"
                bind:value={metadata}
                rows={2}
                spellcheck="false"
                placeholder={'{"customerId": "client-1", "priceCents": 5000}'}
                class="w-full bg-surface-2 border border-border rounded-lg px-3 py-1.5 font-code text-[11px] text-text-main focus:outline-none focus:border-brand-500 resize-y"
              ></textarea>
            </div>
          </div>

          {#if singleError}
            <div class="rounded-lg border border-danger/30 bg-danger/10 px-3 py-2 text-danger text-xs">
              {singleError}
            </div>
          {/if}

          <div class="flex justify-end gap-2 pt-3 border-t border-border">
            <button
              type="button"
              onclick={handleClose}
              class="px-4 py-2 rounded-lg text-text-muted hover:text-text-main transition cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!name.trim() || isSubmittingKey}
              class="flex items-center gap-1.5 px-5 py-2 rounded-lg bg-brand-500 hover:bg-brand-600 disabled:opacity-50 text-white font-bold shadow-md shadow-brand-500/25 transition cursor-pointer"
            >
              {#if isSubmittingKey}
                <Loader2 class="w-3.5 h-3.5 animate-spin" />
                <span>Creating Key...</span>
              {:else}
                <Plus class="w-3.5 h-3.5" />
                <span>Create API Key</span>
              {/if}
            </button>
          </div>
        </form>
      </div>
    </div>
  </div>
{/if}

{#if isModelPickerOpen}
  <ModelPickerModal
    isOpen={isModelPickerOpen}
    target="api-key"
    addedModelValues={models}
    onSelect={(val) => {
      if (!models.includes(val)) {
        models = [...models, val]
      }
    }}
    onDeselect={(val) => {
      models = models.filter((m) => m !== val)
    }}
    onClose={() => (isModelPickerOpen = false)}
  />
{/if}
