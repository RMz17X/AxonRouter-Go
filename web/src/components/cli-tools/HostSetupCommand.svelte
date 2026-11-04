<script lang="ts">
  /**
   * HostSetupCommand — the installer-era replacement for host-side
   * apply buttons (batch 5, P7a). The gateway no longer writes a
   * client's config on its own host; instead the dashboard composes
   * the setup-script URL from the gateway origin, the user-chosen
   * client API key and the page's model-mapping selections, and the
   * user pastes the command into a terminal on their own machine,
   * where GET /api/cli-tools/setup/<toolId> serves a script that
   * writes the config there. Mirrors Axon's HostSetupCommand in
   * CLIToolsPageClient (bash + PowerShell forms).
   */
  import { Check, Copy } from 'lucide-svelte'
  import { copyToClipboard } from '../../lib/clipboard'

  interface Props {
    toolId: string
    /** Gateway origin the client should point at (no trailing /v1). */
    baseUrl: string
    /** The client API key the user picked for this tool. */
    apiKey?: string
    /** Model-mapping selections (model, subagentModel, sonnet, …). */
    params?: Record<string, string>
  }

  let { toolId, baseUrl, apiKey = '', params = {} }: Props = $props()

  let copied = $state<'bash' | 'ps1' | ''>('')

  function buildUrl(format?: string) {
    const parts: [string, string][] = [
      ['baseUrl', baseUrl],
      ['apiKey', apiKey],
    ]
    for (const [key, value] of Object.entries(params)) {
      if (value && value.trim()) parts.push([key, value.trim()])
    }
    if (format) parts.push(['format', format])
    const query = parts
      .filter(([, v]) => v !== '')
      .map(([k, v]) => `${k}=${encodeURIComponent(v)}`)
      .join('&')
    return `${baseUrl}/api/cli-tools/setup/${toolId}?${query}`
  }

  let bashCommand = $derived(`curl -fsSL "${buildUrl()}" | bash`)
  let psCommand = $derived(`irm "${buildUrl('ps1')}" | iex`)

  async function copy(text: string, which: 'bash' | 'ps1') {
    if (await copyToClipboard(text)) {
      copied = which
      setTimeout(() => (copied = ''), 2000)
    }
  }
</script>

<div class="space-y-2">
  <div class="space-y-1.5">
    <div class="flex items-center justify-between">
      <span class="text-xs font-bold text-text-main">Run on your machine — macOS / Linux</span>
      <button
        type="button"
        onclick={() => copy(bashCommand, 'bash')}
        class="flex items-center gap-1 text-xs font-semibold text-brand-500 hover:opacity-80 cursor-pointer"
      >
        {#if copied === 'bash'}
          <Check class="w-3.5 h-3.5 text-success" /><span>Copied!</span>
        {:else}
          <Copy class="w-3.5 h-3.5" /><span>Copy</span>
        {/if}
      </button>
    </div>
    <pre class="p-3 rounded-xl bg-bg border border-border font-mono text-xs text-text-main whitespace-pre-wrap break-all select-all">{bashCommand}</pre>
  </div>

  <div class="space-y-1.5">
    <div class="flex items-center justify-between">
      <span class="text-xs font-bold text-text-main">Windows (PowerShell)</span>
      <button
        type="button"
        onclick={() => copy(psCommand, 'ps1')}
        class="flex items-center gap-1 text-xs font-semibold text-brand-500 hover:opacity-80 cursor-pointer"
      >
        {#if copied === 'ps1'}
          <Check class="w-3.5 h-3.5 text-success" /><span>Copied!</span>
        {:else}
          <Copy class="w-3.5 h-3.5" /><span>Copy</span>
        {/if}
      </button>
    </div>
    <pre class="p-3 rounded-xl bg-bg border border-border font-mono text-xs text-text-main whitespace-pre-wrap break-all select-all">{psCommand}</pre>
  </div>

  <p class="text-[11px] text-text-muted leading-relaxed">
    The script writes this tool's config on the machine where you run it, pointing it at this gateway with the selected key and model mapping.
  </p>
</div>
