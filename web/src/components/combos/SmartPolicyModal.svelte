<script lang="ts">
  import { Zap, Brain, Wand2, RotateCcw, Sparkles, Eye, Cpu } from 'lucide-svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Badge from '../../lib/ui/Badge.svelte'
  import { hasReasoning, hasVision, isLikelyEasyModel, isLikelyHardModel } from './types'

  interface Props {
    isOpen: boolean
    onClose: () => void
    comboName: string
    models?: string[]
    currentPolicy?: string
    currentThreshold?: number
    currentRouterModel?: string
    currentEasyModels?: string[]
    currentHardModels?: string[]
    onSave: (cfg: {
      smartPolicy: string
      sessionSticky: boolean
      confidenceThreshold: number
      routerModel?: string
      easyModels?: string[]
      hardModels?: string[]
    }) => void
    onOpenRouterPicker?: () => void
  }

  let {
    isOpen,
    onClose,
    comboName,
    models = [],
    currentPolicy = 'smart',
    currentThreshold = 0.8,
    currentRouterModel = '',
    currentEasyModels = [],
    currentHardModels = [],
    onSave,
    onOpenRouterPicker,
  }: Props = $props()

  const QUICK_THRESHOLDS = [60, 70, 75, 80, 85, 90]

  const ROUTER_PRESETS = [
    { id: 'liquid/d1', label: 'Liquid d1 (Recommended)' },
    { id: 'liquid/d1.1', label: 'Liquid d1.1' },
    { id: 'liquid/d1-ultra', label: 'Liquid d1-ultra' },
    { id: 'oc/jev-1.13-free', label: 'OpenCode jev-1.13-free' },
    { id: 'gemini-2.5-flash', label: 'Gemini 2.5 Flash' },
  ]

  let selectedPolicy = $state('smart')
  let threshold = $state(80)
  let routerModel = $state('')
  let easyModels = $state<string[]>([])
  let hardModels = $state<string[]>([])
  let activeTab = $state<'pools' | 'router' | 'policy'>('pools')

  $effect(() => {
    if (isOpen) {
      let initial = currentPolicy || 'smart'
      if (initial.startsWith('smart-switch')) initial = 'smart'
      selectedPolicy = initial
      const pct = Math.round((currentThreshold || 0.8) * (currentThreshold <= 1 ? 100 : 1))
      threshold = pct || 80
      routerModel = currentRouterModel || ''
      easyModels = currentEasyModels ? [...currentEasyModels] : []
      hardModels = currentHardModels ? [...currentHardModels] : []
    }
  })

  function toggleModelTier(model: string, tier: 'easy' | 'hard' | 'auto') {
    easyModels = easyModels.filter((m) => m !== model)
    hardModels = hardModels.filter((m) => m !== model)
    if (tier === 'easy') {
      easyModels = [...easyModels, model]
    } else if (tier === 'hard') {
      hardModels = [...hardModels, model]
    }
  }

  function autoDetectPools() {
    const newEasy: string[] = []
    const newHard: string[] = []
    for (const m of models) {
      if (isLikelyEasyModel(m)) {
        newEasy.push(m)
      } else if (isLikelyHardModel(m)) {
        newHard.push(m)
      }
    }
    easyModels = newEasy
    hardModels = newHard
  }

  function clearPools() {
    easyModels = []
    hardModels = []
  }

  function handleSave() {
    const finalThreshold =
      selectedPolicy === 'custom' ? threshold / 100 : selectedPolicy === 'smart' ? 0.8 : 0
    onSave({
      smartPolicy: selectedPolicy,
      sessionSticky: selectedPolicy !== 'per-turn',
      confidenceThreshold: finalThreshold,
      routerModel: routerModel.trim() || undefined,
      easyModels: easyModels.length > 0 ? easyModels : undefined,
      hardModels: hardModels.length > 0 ? hardModels : undefined,
    })
    onClose()
  }

  const policies = $derived([
    {
      id: 'smart',
      title: 'Smart Switch',
      badge: 'Adaptive ≥80%',
      badgeVariant: 'success',
      icon: 'auto_mode',
      recommended: true,
      desc: 'Preserves warm prompt cache across turns. Seamlessly switches models if conversation topic shifts with high confidence (≥80%).',
    },
    {
      id: 'strict',
      title: 'Strict Lock',
      badge: '1 Task = 1 Session',
      badgeVariant: 'default',
      icon: 'lock',
      desc: 'Evaluates System One on turn 1, then locks that model for entire session. Zero extra router calls (+0ms latency).',
    },
    {
      id: 'per-turn',
      title: 'Per-Turn',
      badge: 'Dynamic',
      badgeVariant: 'default',
      icon: 'refresh',
      desc: 'Evaluates System One independently on every prompt without session stickiness.',
    },
    {
      id: 'custom',
      title: 'Custom Threshold',
      badge: `Adaptive ≥${threshold}%`,
      badgeVariant: 'default',
      icon: 'tune',
      desc: 'Configure custom confidence threshold required to trigger a model switch.',
    },
  ])

  let autoCount = $derived(
    models.filter((m) => !easyModels.includes(m) && !hardModels.includes(m)).length
  )
</script>

<Modal {isOpen} {onClose} title="Smart Router — {comboName || 'Combo'}" size="xl">
  <div class="flex flex-col gap-4">
    <!-- Navigation Tabs -->
    <div class="flex items-center gap-1 border-b border-border/60 pb-2">
      <button
        type="button"
        onclick={() => (activeTab = 'pools')}
        class={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
          activeTab === 'pools'
            ? 'bg-brand-500/10 text-brand-500 border border-brand-500/30'
            : 'text-text-muted hover:text-text-main hover:bg-surface-2'
        }`}
      >
        <Zap class="w-3.5 h-3.5" />
        <span>Easy & Hard Pools</span>
        {#if easyModels.length > 0 || hardModels.length > 0}
          <span class="ml-1 px-1.5 py-0.2 rounded text-[10px] bg-brand-500/20 text-brand-400 font-mono">
            {easyModels.length}E / {hardModels.length}H
          </span>
        {/if}
      </button>

      <button
        type="button"
        onclick={() => (activeTab = 'router')}
        class={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
          activeTab === 'router'
            ? 'bg-brand-500/10 text-brand-500 border border-brand-500/30'
            : 'text-text-muted hover:text-text-main hover:bg-surface-2'
        }`}
      >
        <Cpu class="w-3.5 h-3.5" />
        <span>Decision Router Model</span>
        {#if routerModel}
          <span class="ml-1 text-[10px] text-text-muted truncate max-w-[100px]">
            ({routerModel})
          </span>
        {/if}
      </button>

      <button
        type="button"
        onclick={() => (activeTab = 'policy')}
        class={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors cursor-pointer ${
          activeTab === 'policy'
            ? 'bg-brand-500/10 text-brand-500 border border-brand-500/30'
            : 'text-text-muted hover:text-text-main hover:bg-surface-2'
        }`}
      >
        <span class="material-symbols-outlined text-[15px]">tune</span>
        <span>Switching Policy</span>
      </button>
    </div>

    <!-- TAB 1: MODEL POOLS (EASY VS HARD) -->
    {#if activeTab === 'pools'}
      <div class="flex flex-col gap-3">
        <div class="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2 bg-surface-2/40 border border-border/70 rounded-xl p-3">
          <div class="flex flex-col gap-0.5">
            <span class="text-xs font-semibold text-text-main flex items-center gap-1.5">
              <span>Model Complexity Classification</span>
            </span>
            <p class="text-[11px] text-text-muted leading-relaxed">
              Assign models into <strong>⚡ Easy Pool</strong> (fast, cheap, straightforward prompts) and <strong>🧠 Hard Pool</strong> (deep logic, math, multi-step algorithms). The System One model dynamically picks the best tier.
            </p>
          </div>

          <div class="flex items-center gap-1.5 shrink-0">
            <button
              type="button"
              onclick={autoDetectPools}
              class="inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded-lg bg-brand-500/10 text-brand-500 border border-brand-500/20 hover:bg-brand-500/20 transition-colors cursor-pointer"
              title="Automatically classify models based on keywords and reasoning flags"
            >
              <Wand2 class="w-3.5 h-3.5" />
              <span>Auto-Detect</span>
            </button>
            <button
              type="button"
              onclick={clearPools}
              class="inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded-lg bg-surface-2 text-text-muted hover:text-text-main border border-border hover:bg-surface-3 transition-colors cursor-pointer"
              title="Reset all assignments back to automatic heuristics"
            >
              <RotateCcw class="w-3 h-3" />
              <span>Reset</span>
            </button>
          </div>
        </div>

        <!-- Summary Strip -->
        <div class="flex items-center gap-2 text-xs">
          <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 font-medium text-[11px]">
            <Zap class="w-3 h-3" />
            <span>Easy Pool: {easyModels.length}</span>
          </span>
          <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border border-purple-500/30 bg-purple-500/10 text-purple-600 dark:text-purple-400 font-medium text-[11px]">
            <Brain class="w-3 h-3" />
            <span>Hard Pool: {hardModels.length}</span>
          </span>
          <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border border-border bg-surface-2 text-text-muted text-[11px]">
            <span>Auto / Unassigned: {autoCount}</span>
          </span>
        </div>

        <!-- Models Classification List -->
        {#if models.length === 0}
          <div class="p-6 text-center text-xs text-text-muted border border-dashed border-border rounded-xl">
            No models in this combo yet. Add models to the combo first to assign tiers.
          </div>
        {:else}
          <div class="max-h-[340px] overflow-y-auto space-y-1.5 pr-1 custom-scrollbar">
            {#each models as model (model)}
              {@const isEasy = easyModels.includes(model)}
              {@const isHard = hardModels.includes(model)}
              {@const isAuto = !isEasy && !isHard}
              <div class="flex items-center justify-between gap-3 p-2.5 rounded-lg border border-border/70 bg-surface hover:border-border transition-colors">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="font-mono text-xs text-text-main truncate font-medium">{model}</span>
                  {#if hasVision(model)}
                    <Eye class="w-3.5 h-3.5 text-blue-500 shrink-0" role="img" aria-label="Vision capable" />
                  {/if}
                  {#if hasReasoning(model)}
                    <Sparkles class="w-3.5 h-3.5 text-amber-500 shrink-0" role="img" aria-label="Reasoning capable" />
                  {/if}
                </div>

                <!-- 3-way tier segmented toggle -->
                <div class="inline-flex rounded-lg border border-border bg-surface-2/60 p-0.5 shrink-0">
                  <button
                    type="button"
                    onclick={() => toggleModelTier(model, 'easy')}
                    class={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium transition-colors cursor-pointer ${
                      isEasy
                        ? 'bg-emerald-500 text-white shadow-xs'
                        : 'text-text-muted hover:text-text-main'
                    }`}
                  >
                    <Zap class="w-3 h-3" />
                    <span>Easy</span>
                  </button>

                  <button
                    type="button"
                    onclick={() => toggleModelTier(model, 'hard')}
                    class={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium transition-colors cursor-pointer ${
                      isHard
                        ? 'bg-purple-600 text-white shadow-xs'
                        : 'text-text-muted hover:text-text-main'
                    }`}
                  >
                    <Brain class="w-3 h-3" />
                    <span>Hard</span>
                  </button>

                  <button
                    type="button"
                    onclick={() => toggleModelTier(model, 'auto')}
                    class={`inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium transition-colors cursor-pointer ${
                      isAuto
                        ? 'bg-surface border border-border text-text-main shadow-xs'
                        : 'text-text-muted hover:text-text-main'
                    }`}
                  >
                    <span>Auto</span>
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB 2: ROUTER MODEL (SYSTEM ONE) -->
    {#if activeTab === 'router'}
      <div class="flex flex-col gap-3">
        <div class="flex flex-col gap-1 bg-surface-2/40 border border-border/70 rounded-xl p-3">
          <span class="text-xs font-semibold text-text-main flex items-center gap-1.5">
            <Cpu class="w-3.5 h-3.5 text-brand-500" />
            <span>System One Decision Model</span>
          </span>
          <p class="text-[11px] text-text-muted leading-relaxed">
            The decision model analyzes the user request and predicts whether it requires an Easy model (fast response, simple logic) or a Hard model (complex reasoning).
          </p>
        </div>

        <div class="flex flex-col gap-2">
          <label class="flex flex-col gap-1">
            <span class="text-xs font-medium text-text-muted">Selected Router Model:</span>
            <div class="flex items-center gap-2">
              <input
                type="text"
                bind:value={routerModel}
                placeholder="liquid/d1 (Default)"
                class="flex-1 bg-surface border border-border rounded-lg px-3 py-1.5 text-xs font-mono text-text-main focus:outline-none focus:border-brand-500"
              />
              {#if onOpenRouterPicker}
                <Button
                  variant="secondary"
                  size="sm"
                  onclick={() => {
                    onClose()
                    onOpenRouterPicker?.()
                  }}
                >
                  Browse Catalog
                </Button>
              {/if}
            </div>
          </label>
        </div>

        <!-- Quick Presets -->
        <div class="flex flex-col gap-1.5 pt-1">
          <span class="text-[11px] font-medium text-text-muted">Quick Presets:</span>
          <div class="flex flex-wrap gap-1.5">
            {#each ROUTER_PRESETS as preset (preset.id)}
              {@const isSelected = routerModel === preset.id || (!routerModel && preset.id === 'liquid/d1')}
              <button
                type="button"
                onclick={() => (routerModel = preset.id)}
                class={`px-2.5 py-1 rounded-lg text-xs font-mono transition-colors border cursor-pointer ${
                  isSelected
                    ? 'bg-brand-500/15 border-brand-500/40 text-brand-400 font-semibold'
                    : 'bg-surface-2 border-border/70 text-text-muted hover:text-text-main hover:bg-surface-3'
                }`}
              >
                {preset.label}
              </button>
            {/each}
          </div>
        </div>
      </div>
    {/if}

    <!-- TAB 3: SWITCHING POLICY -->
    {#if activeTab === 'policy'}
      <div class="flex flex-col gap-3">
        <p class="text-xs text-text-muted">
          Choose how the System One router manages model selection across conversation turns:
        </p>

        <div class="flex flex-col gap-2.5">
          {#each policies as p (p.id)}
            {@const isSelected = selectedPolicy === p.id}
            <div
              role="radio"
              aria-checked={isSelected}
              tabindex="0"
              onclick={() => (selectedPolicy = p.id)}
              onkeydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  selectedPolicy = p.id
                }
              }}
              class={`relative flex flex-col rounded-xl border p-3 cursor-pointer transition-all ${
                isSelected
                  ? 'border-brand-500 bg-brand-500/[0.04] shadow-xs'
                  : 'border-border/60 hover:border-border hover:bg-black/[0.01] dark:hover:bg-white/[0.01]'
              }`}
            >
              <div class="flex items-center justify-between gap-2">
                <div class="flex items-center gap-2">
                  <span
                    class={`size-4 rounded-full border flex items-center justify-center shrink-0 transition-all ${
                      isSelected ? 'border-brand-500 bg-brand-500 text-white' : 'border-border-subtle bg-surface'
                    }`}
                  >
                    {#if isSelected}<span class="size-1.5 rounded-full bg-white"></span>{/if}
                  </span>
                  <span class="size-5 rounded flex items-center justify-center shrink-0 text-text-muted">
                    <span class="material-symbols-outlined text-[15px] leading-none select-none inline-block">{p.icon}</span>
                  </span>
                  <span class="text-sm font-medium text-text-main">{p.title}</span>
                  {#if p.recommended}
                    <span class="text-[10px] uppercase tracking-wide font-semibold text-brand-500 bg-brand-500/10 px-1.5 py-0.5 rounded">
                      Recommended
                    </span>
                  {/if}
                </div>
                <Badge variant={p.badgeVariant as 'success' | 'default'} size="sm">{p.badge}</Badge>
              </div>

              <p class="text-xs text-text-muted mt-1.5 pl-6 leading-relaxed">{p.desc}</p>

              {#if p.id === 'custom' && isSelected}
                <div
                  class="mt-3 pl-6 pt-3 border-t border-border/40 flex flex-col gap-2.5"
                  role="presentation"
                  onclick={(e) => e.stopPropagation()}
                  onkeydown={(e) => e.stopPropagation()}
                >
                  <div class="flex items-center justify-between text-xs">
                    <span class="font-medium text-text-muted">Switch Confidence Threshold:</span>
                    <span class="font-mono font-semibold text-brand-500 text-sm">{threshold}%</span>
                  </div>

                  <input
                    type="range"
                    min="50"
                    max="95"
                    step="1"
                    bind:value={threshold}
                    class="w-full accent-brand-500 cursor-pointer h-1.5 bg-black/10 dark:bg-white/10 rounded-lg appearance-none"
                    aria-label="Switch confidence threshold percent"
                  />

                  <div class="flex items-center gap-1.5 pt-1">
                    <span class="text-[10px] text-text-muted mr-1">Presets:</span>
                    {#each QUICK_THRESHOLDS as pct (pct)}
                      <button
                        type="button"
                        onclick={() => (threshold = pct)}
                        class={`px-2 py-0.5 rounded text-[11px] font-mono transition-colors cursor-pointer ${
                          threshold === pct
                            ? 'bg-brand-500 text-white font-semibold'
                            : 'bg-black/5 dark:bg-white/5 text-text-muted hover:text-text-main'
                        }`}
                      >
                        {pct}%
                      </button>
                    {/each}
                  </div>
                </div>
              {/if}
            </div>
          {/each}
        </div>

        <div class="rounded-xl border border-brand-500/20 bg-brand-500/[0.03] p-3 text-xs flex items-start gap-2.5">
          <span class="material-symbols-outlined text-brand-500 text-[18px] shrink-0 mt-0.5">bolt</span>
          <div class="text-text-muted leading-relaxed">
            <span class="font-medium text-text-main">Prompt Cache Benefits: </span>
            Retaining the same model across session turns keeps upstream prompt caches warm, reducing input token costs by up to <strong class="text-text-main">90%</strong> and cutting latency in half.
          </div>
        </div>
      </div>
    {/if}

    <!-- Footer Actions -->
    <div class="flex items-center justify-between pt-2 border-t border-border/40">
      <div class="text-[11px] text-text-muted">
        {#if easyModels.length > 0 || hardModels.length > 0}
          <span class="text-brand-500 font-medium">✓ Pools configured</span> ({easyModels.length} Easy, {hardModels.length} Hard)
        {:else}
          <span>Standard auto heuristics active</span>
        {/if}
      </div>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" onclick={onClose}>Cancel</Button>
        <Button variant="primary" size="sm" onclick={handleSave}>Save Smart Settings</Button>
      </div>
    </div>
  </div>
</Modal>
