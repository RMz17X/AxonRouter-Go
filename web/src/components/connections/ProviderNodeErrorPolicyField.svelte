<script lang="ts">
  import type { CustomErrorRule, ErrorPolicy } from '../../api/client'

  interface Props {
    policy?: ErrorPolicy | null
    onChange: (policy: ErrorPolicy | null) => void
  }

  let { policy = null, onChange }: Props = $props()

  let open = $state(false)
  let rules = $state<CustomErrorRule[]>([])

  // Seed on initialization or policy change
  $effect(() => {
    if (policy && policy.rules && policy.rules.length > 0) {
      rules = JSON.parse(JSON.stringify(policy.rules))
      open = true
    } else {
      rules = []
    }
  })

  function notifyChange() {
    if (rules.length === 0) {
      onChange(null)
    } else {
      onChange({ rules: JSON.parse(JSON.stringify(rules)) })
    }
  }

  function addRule(rule?: Partial<CustomErrorRule>) {
    rules = [
      ...rules,
      {
        status: rule?.status !== undefined ? rule.status : 401,
        text: rule?.text || '',
        action: rule?.action || 'deactivate_key',
        cooldownSec: rule?.cooldownSec || 0,
        description: rule?.description || '',
      },
    ]
    notifyChange()
  }

  function removeRule(index: number) {
    rules = rules.filter((_, i) => i !== index)
    notifyChange()
  }

  function updateRuleStatus(index: number, val: string) {
    const num = parseInt(val, 10)
    rules[index].status = isNaN(num) || num <= 0 ? undefined : num
    notifyChange()
  }

  function updateRuleText(index: number, val: string) {
    rules[index].text = val
    notifyChange()
  }

  function updateRuleAction(index: number, action: CustomErrorRule['action']) {
    rules[index].action = action
    notifyChange()
  }

  function updateRuleCooldown(index: number, val: string) {
    const num = parseInt(val, 10)
    rules[index].cooldownSec = isNaN(num) || num < 0 ? 0 : num
    notifyChange()
  }

  function updateRuleDesc(index: number, val: string) {
    rules[index].description = val
    notifyChange()
  }
</script>

<div class="border-t border-border pt-4">
  <button
    type="button"
    class="flex w-full items-start justify-between gap-3 text-left cursor-pointer focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/40 rounded-md"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    <div class="flex flex-col gap-0.5">
      <div class="flex items-center gap-2">
        <span class="text-sm font-medium text-text-main">Custom Error & Cooldown Policy</span>
        {#if rules.length > 0}
          <span class="px-2 py-0.5 text-xs font-semibold rounded bg-brand-500/15 text-brand-400 border border-brand-500/30">
            {rules.length} {rules.length === 1 ? 'rule' : 'rules'}
          </span>
        {:else}
          <span class="text-xs text-text-muted">Standard behavior</span>
        {/if}
      </div>
      <span class="text-xs text-text-muted">
        Define custom matching rules to deactivate revoked keys or handle custom cooldowns.
      </span>
    </div>
    <span class="text-text-muted text-xs font-mono select-none pt-0.5">
      {open ? 'Hide' : 'Configure'}
    </span>
  </button>

  {#if open}
    <div class="mt-4 flex flex-col gap-3">
      {#if rules.length === 0}
        <div class="p-3 rounded-brand bg-surface-2/60 border border-border text-xs text-text-muted flex flex-col gap-2">
          <span>No custom error rules defined. Standard classification will apply for this provider.</span>
          <div class="flex flex-wrap gap-2 pt-1">
            <button
              type="button"
              class="px-2.5 py-1 text-xs rounded bg-surface-3 hover:bg-surface-3/80 text-text-main border border-border cursor-pointer transition-colors"
              onclick={() => addRule({ status: 401, action: 'deactivate_key', description: 'Auto-deactivate expired/revoked key' })}
            >
              + Quick Preset: 401 Auto-Deactivate
            </button>
            <button
              type="button"
              class="px-2.5 py-1 text-xs rounded bg-surface-3 hover:bg-surface-3/80 text-text-main border border-border cursor-pointer transition-colors"
              onclick={() => addRule({ status: 402, text: 'insufficient_balance', action: 'exhaust_account', description: 'Zero balance / trial expired' })}
            >
              + Quick Preset: 402 Exhaust Account
            </button>
          </div>
        </div>
      {:else}
        <div class="flex flex-col gap-2.5">
          {#each rules as rule, index}
            <div class="p-3 rounded-brand bg-surface-2 border border-border flex flex-col gap-2.5 relative">
              <div class="flex items-center justify-between gap-2">
                <span class="text-xs font-semibold text-text-main uppercase tracking-wider">
                  Rule #{index + 1}
                </span>
                <button
                  type="button"
                  class="text-xs text-red-400 hover:text-red-300 px-2 py-0.5 rounded hover:bg-red-500/10 transition-colors cursor-pointer"
                  onclick={() => removeRule(index)}
                  title="Remove rule"
                >
                  Delete
                </button>
              </div>

              <div class="grid grid-cols-1 md:grid-cols-2 gap-2">
                <label class="block">
                  <span class="text-xs font-medium text-text-muted mb-1 block">HTTP Status Code</span>
                  <input
                    type="number"
                    value={rule.status ?? ''}
                    placeholder="Any (or e.g. 401, 402, 429)"
                    class="w-full py-1.5 px-2.5 text-xs text-text-main bg-surface-1 border border-border rounded appearance-none focus:outline-none focus:ring-1 focus:ring-brand-500"
                    oninput={(e) => updateRuleStatus(index, (e.target as HTMLInputElement).value)}
                  />
                </label>
                <label class="block">
                  <span class="text-xs font-medium text-text-muted mb-1 block">Action</span>
                  <select
                    value={rule.action}
                    class="w-full py-1.5 px-2.5 text-xs text-text-main bg-surface-1 border border-border rounded appearance-none focus:outline-none focus:ring-1 focus:ring-brand-500"
                    onchange={(e) => updateRuleAction(index, (e.target as HTMLSelectElement).value as CustomErrorRule['action'])}
                  >
                    <option value="deactivate_key">Deactivate Key (Disable & Fallback)</option>
                    <option value="exhaust_account">Exhaust Account (Balance 0 / Month)</option>
                    <option value="cooldown">Cooldown Key (Custom duration)</option>
                    <option value="model_lock">Model Lock (Cooldown only for this model)</option>
                    <option value="request_level">Request Level (No cooldown, pass to client)</option>
                  </select>
                </label>
              </div>

              <div class="grid grid-cols-1 md:grid-cols-2 gap-2">
                <label class="block">
                  <span class="text-xs font-medium text-text-muted mb-1 block">Error Text Contains (Optional)</span>
                  <input
                    type="text"
                    value={rule.text || ''}
                    placeholder="e.g. invalid_api_key or insufficient_balance"
                    class="w-full py-1.5 px-2.5 text-xs text-text-main bg-surface-1 border border-border rounded font-mono appearance-none focus:outline-none focus:ring-1 focus:ring-brand-500"
                    oninput={(e) => updateRuleText(index, (e.target as HTMLInputElement).value)}
                  />
                </label>

                {#if rule.action === 'cooldown' || rule.action === 'exhaust_account' || rule.action === 'model_lock'}
                  <label class="block">
                    <span class="text-xs font-medium text-text-muted mb-1 block">
                      Cooldown (Seconds, 0 = default)
                    </span>
                    <input
                      type="number"
                      value={rule.cooldownSec || 0}
                      placeholder="0 (auto default)"
                      class="w-full py-1.5 px-2.5 text-xs text-text-main bg-surface-1 border border-border rounded appearance-none focus:outline-none focus:ring-1 focus:ring-brand-500"
                      oninput={(e) => updateRuleCooldown(index, (e.target as HTMLInputElement).value)}
                    />
                  </label>
                {:else}
                  <label class="block">
                    <span class="text-xs font-medium text-text-muted mb-1 block">Description / Note</span>
                    <input
                      type="text"
                      value={rule.description || ''}
                      placeholder="e.g. Apodex key revocation"
                      class="w-full py-1.5 px-2.5 text-xs text-text-main bg-surface-1 border border-border rounded appearance-none focus:outline-none focus:ring-1 focus:ring-brand-500"
                      oninput={(e) => updateRuleDesc(index, (e.target as HTMLInputElement).value)}
                    />
                  </label>
                {/if}
              </div>

              {#if rule.action === 'cooldown' || rule.action === 'exhaust_account' || rule.action === 'model_lock'}
                <label class="block">
                  <span class="text-xs font-medium text-text-muted mb-1 block">Description / Note</span>
                  <input
                    type="text"
                    value={rule.description || ''}
                    placeholder="e.g. Rate limit backoff rule"
                    class="w-full py-1.5 px-2.5 text-xs text-text-main bg-surface-1 border border-border rounded appearance-none focus:outline-none focus:ring-1 focus:ring-brand-500"
                    oninput={(e) => updateRuleDesc(index, (e.target as HTMLInputElement).value)}
                  />
                </label>
              {/if}
            </div>
          {/each}
        </div>
      {/if}

      <div class="flex items-center gap-2 pt-1">
        <button
          type="button"
          class="px-3 py-1.5 text-xs font-medium rounded-brand bg-surface-2 hover:bg-surface-3 text-text-main border border-border cursor-pointer transition-colors"
          onclick={() => addRule()}
        >
          + Add Rule
        </button>
      </div>
    </div>
  {/if}
</div>
