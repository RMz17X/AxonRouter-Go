<script lang="ts">
  // The Deploy Relay cluster: the header menu plus all six deploy dialogs
  // (Vercel / Cloudflare / Deno / Netlify, single + bulk). Extracted from
  // ProxyPoolsView — its only link back is onDeployed, fired after a deploy
  // lands so the parent refetches the pool list.
  import Badge from '../../lib/ui/Badge.svelte'
  import Button from '../../lib/ui/Button.svelte'
  import Input from '../../lib/ui/Input.svelte'
  import Modal from '../../lib/ui/Modal.svelte'
  import { api } from '../../api/client'
  import { notifications } from '../../lib/notifications'

  interface Props {
    onDeployed: () => void
  }

  let { onDeployed }: Props = $props()

  let showVercelModal = $state(false)
  let showCloudflareModal = $state(false)
  let showDenoModal = $state(false)
  let showNetlifyModal = $state(false)
  let showRelayMenu = $state(false)

  let vercelForm = $state({ vercelToken: '', projectName: 'vercel-relay' })
  let cloudflareForm = $state({ accountId: '', apiToken: '', projectName: 'cloudflare-relay' })
  let denoForm = $state({ denoToken: '', orgDomain: '', projectName: '' })
  let netlifyForm = $state({ netlifyToken: '', projectName: 'netlify-relay' })

  interface CloudflareBulkResult {
    lineNumber: number
    label: string
    accountId: string
    projectName: string
    status: 'pending' | 'running' | 'success' | 'failed'
    deployUrl?: string
    error?: string
  }

  let showCloudflareBulkModal = $state(false)
  let cloudflareBulkPoolName = $state('cloudflare-relay')
  let cloudflareBulkText = $state('')
  let cloudflareBulkResults = $state<CloudflareBulkResult[]>([])
  let cloudflareBulkProgress = $state({
    total: 0,
    completed: 0,
    success: 0,
    failed: 0,
    currentBatch: 0,
    totalBatches: 0
  })
  let bulkCloudflareDeploying = $state(false)

  interface VercelBulkResult {
    lineNumber: number
    label: string
    tokenMasked: string
    projectName: string
    status: 'pending' | 'running' | 'success' | 'failed'
    deployUrl?: string
    error?: string
  }

  let showVercelBulkModal = $state(false)
  let vercelBulkPoolName = $state('vercel-relay')
  let vercelBulkText = $state('')
  let vercelBulkResults = $state<VercelBulkResult[]>([])
  let vercelBulkProgress = $state({
    total: 0,
    completed: 0,
    success: 0,
    failed: 0,
    currentBatch: 0,
    totalBatches: 0
  })
  let bulkVercelDeploying = $state(false)

  let deploying = $state(false)

  let relayMenuRef = $state<HTMLDivElement | null>(null)
  // Close the Deploy Relay menu on outside click (upstream parity).
  $effect(() => {
    if (!showRelayMenu || typeof document === 'undefined') return
    const onDown = (e: MouseEvent) => {
      if (relayMenuRef && !relayMenuRef.contains(e.target as Node)) showRelayMenu = false
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  })

  function openVercelModal() {
    vercelForm = { vercelToken: '', projectName: 'vercel-relay' }
    showVercelModal = true
  }

  function closeVercelModal() {
    if (deploying) return
    showVercelModal = false
  }

  function openCloudflareModal() {
    cloudflareForm = { accountId: '', apiToken: '', projectName: 'cloudflare-relay' }
    showCloudflareModal = true
  }

  function closeCloudflareModal() {
    if (deploying) return
    showCloudflareModal = false
  }

  function openCloudflareBulkModal() {
    cloudflareBulkPoolName = 'cloudflare-relay'
    cloudflareBulkText = ''
    cloudflareBulkResults = []
    cloudflareBulkProgress = {
      total: 0,
      completed: 0,
      success: 0,
      failed: 0,
      currentBatch: 0,
      totalBatches: 0
    }
    showCloudflareBulkModal = true
  }

  function closeCloudflareBulkModal() {
    if (bulkCloudflareDeploying) return
    showCloudflareBulkModal = false
  }

  function openVercelBulkModal() {
    vercelBulkPoolName = 'vercel-relay'
    vercelBulkText = ''
    vercelBulkResults = []
    vercelBulkProgress = {
      total: 0,
      completed: 0,
      success: 0,
      failed: 0,
      currentBatch: 0,
      totalBatches: 0
    }
    showVercelBulkModal = true
  }

  function closeVercelBulkModal() {
    if (bulkVercelDeploying) return
    showVercelBulkModal = false
  }

  function openDenoModal() {
    denoForm = { denoToken: '', orgDomain: '', projectName: '' }
    showDenoModal = true
  }

  function closeDenoModal() {
    if (deploying) return
    showDenoModal = false
  }

  function openNetlifyModal() {
    netlifyForm = { netlifyToken: '', projectName: 'netlify-relay' }
    showNetlifyModal = true
  }

  function closeNetlifyModal() {
    if (deploying) return
    showNetlifyModal = false
  }

  // One entry per deploy flow in the Deploy Relay menu — the menu renders
  // this list instead of six hand-written buttons.
  const relayMenuItems = [
    { label: 'Cloudflare Relay', icon: 'cloud', open: () => openCloudflareModal() },
    { label: 'CF Bulk', icon: 'playlist_add', open: () => openCloudflareBulkModal() },
    { label: 'Vercel Relay', icon: 'cloud_upload', open: () => openVercelModal() },
    { label: 'Vercel Bulk', icon: 'playlist_add', open: () => openVercelBulkModal() },
    { label: 'Deno Relay', icon: 'terminal', open: () => openDenoModal() },
    { label: 'Netlify Relay', icon: 'deployed_code', open: () => openNetlifyModal() },
  ]

  async function handleVercelDeploy() {
    if (!vercelForm.vercelToken.trim()) return
    deploying = true
    try {
      const data = await api.deployVercelRelay({
        vercelToken: vercelForm.vercelToken.trim(),
        projectName: vercelForm.projectName.trim() || undefined,
      })
      await onDeployed()
      closeVercelModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Vercel relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleCloudflareDeploy() {
    if (!cloudflareForm.accountId.trim() || !cloudflareForm.apiToken.trim()) return
    deploying = true
    try {
      const data = await api.deployCloudflareRelay({
        accountId: cloudflareForm.accountId.trim(),
        apiToken: cloudflareForm.apiToken.trim(),
        projectName: cloudflareForm.projectName.trim() || undefined,
        name: cloudflareForm.projectName.trim() || undefined,
      })
      await onDeployed()
      closeCloudflareModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Cloudflare relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleCloudflareBulkDeploy() {
    const poolBase =
      cloudflareBulkPoolName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 32) || 'cloudflare-relay'

    // Worker name = pool base. Number only appended when the same Cloudflare
    // account appears more than once (script name is unique per account, NOT
    // globally), so distinct accounts keep the clean identical name.
    const accountCounts = new Map<string, number>()
    const entries = cloudflareBulkText
      .split(/\r?\n/)
      .map((line, index) => ({ line: line.trim(), lineNumber: index + 1 }))
      .filter(({ line }) => line.length > 0)
      .map(({ line, lineNumber }) => {
        const [label = '', accountId = '', apiToken = '', ...extra] = line
          .split('|')
          .map((part) => part.trim())
        const seen = (accountCounts.get(accountId) ?? 0) + 1
        accountCounts.set(accountId, seen)
        return {
          lineNumber,
          label,
          accountId,
          apiToken,
          projectName: seen === 1 ? poolBase : `${poolBase}-${seen}`,
          valid: Boolean(label && accountId && apiToken && !extra.length)
        }
      })

    if (!entries.length) return

    const maskAccountId = (id: string) =>
      id.length > 8 ? `${id.slice(0, 4)}…${id.slice(-4)}` : '••••'

    cloudflareBulkResults = entries.map((entry) => ({
      lineNumber: entry.lineNumber,
      label: entry.label || `Line ${entry.lineNumber}`,
      accountId: entry.accountId ? maskAccountId(entry.accountId) : '—',
      projectName: entry.projectName,
      status: entry.valid ? 'pending' : 'failed',
      error: entry.valid ? undefined : 'Format harus name/email|accountID|apiToken'
    }))

    bulkCloudflareDeploying = true
    const validEntries = entries
      .map((entry, index) => ({ ...entry, originalIndex: index }))
      .filter((entry) => entry.valid)
    const BATCH_SIZE = 10
    const totalBatches = Math.ceil(validEntries.length / BATCH_SIZE) || 1

    let successCount = 0
    let failedCount = entries.filter((entry) => !entry.valid).length

    cloudflareBulkProgress = {
      total: entries.length,
      completed: failedCount,
      success: 0,
      failed: failedCount,
      currentBatch: validEntries.length > 0 ? 1 : 0,
      totalBatches
    }

    try {
      for (let batchIndex = 0; batchIndex < totalBatches; batchIndex++) {
        const batch = validEntries.slice(batchIndex * BATCH_SIZE, (batchIndex + 1) * BATCH_SIZE)
        if (!batch.length) continue

        cloudflareBulkProgress.currentBatch = batchIndex + 1

        const runningIndices = new Set(batch.map((item) => item.originalIndex))
        cloudflareBulkResults = cloudflareBulkResults.map((result, idx) =>
          runningIndices.has(idx) ? { ...result, status: 'running' } : result
        )

        await Promise.all(
          batch.map(async (entry) => {
            const index = entry.originalIndex
            try {
              const res = await api.deployCloudflareRelay({
                accountId: entry.accountId,
                apiToken: entry.apiToken,
                projectName: entry.projectName,
                name: entry.projectName,
                email: entry.label
              })

              successCount += 1
              cloudflareBulkResults = cloudflareBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'success', deployUrl: res.deployUrl || res.proxyUrl || 'Deployed' }
                  : result
              )
              cloudflareBulkProgress.completed += 1
              cloudflareBulkProgress.success += 1
            } catch (error) {
              failedCount += 1
              cloudflareBulkResults = cloudflareBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'failed', error: error instanceof Error ? error.message : 'Deploy failed' }
                  : result
              )
              cloudflareBulkProgress.completed += 1
              cloudflareBulkProgress.failed += 1
            }
          })
        )
      }

      await onDeployed()
      if (failedCount > 0) {
        notifications.error(`${successCount} deployed, ${failedCount} failed`)
      } else {
        notifications.success(`${successCount} Cloudflare relays deployed`)
      }
    } catch (error) {
      notifications.error(error instanceof Error ? error.message : 'Bulk deploy failed')
    } finally {
      bulkCloudflareDeploying = false
    }
  }

  async function handleVercelBulkDeploy() {
    const poolBase =
      vercelBulkPoolName
        .trim()
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 32) || 'vercel-relay'

    const tokenCounts = new Map<string, number>()
    const entries = vercelBulkText
      .split(/\r?\n/)
      .map((line, index) => ({ line: line.trim(), lineNumber: index + 1 }))
      .filter(({ line }) => line.length > 0)
      .map(({ line, lineNumber }, index) => {
        let label = `Token ${index + 1}`
        let token = line
        if (line.includes('|')) {
          const parts = line.split('|').map((p) => p.trim())
          label = parts[0] || `Token ${index + 1}`
          token = parts[1] || ''
        }
        const seen = (tokenCounts.get(token) ?? 0) + 1
        tokenCounts.set(token, seen)
        return {
          lineNumber,
          label,
          token,
          projectName: seen === 1 ? poolBase : `${poolBase}-${seen}`,
          valid: Boolean(token.length > 0)
        }
      })

    if (!entries.length) return

    const maskToken = (tok: string) =>
      tok.length > 8 ? `${tok.slice(0, 4)}…${tok.slice(-4)}` : '••••'

    vercelBulkResults = entries.map((entry) => ({
      lineNumber: entry.lineNumber,
      label: entry.label,
      tokenMasked: entry.token ? maskToken(entry.token) : '—',
      projectName: entry.projectName,
      status: entry.valid ? 'pending' : 'failed',
      error: entry.valid ? undefined : 'Token tidak boleh kosong'
    }))

    bulkVercelDeploying = true
    const validEntries = entries
      .map((entry, index) => ({ ...entry, originalIndex: index }))
      .filter((entry) => entry.valid)
    const BATCH_SIZE = 10
    const totalBatches = Math.ceil(validEntries.length / BATCH_SIZE) || 1

    let successCount = 0
    let failedCount = entries.filter((entry) => !entry.valid).length

    vercelBulkProgress = {
      total: entries.length,
      completed: failedCount,
      success: 0,
      failed: failedCount,
      currentBatch: validEntries.length > 0 ? 1 : 0,
      totalBatches
    }

    try {
      for (let batchIndex = 0; batchIndex < totalBatches; batchIndex++) {
        const batch = validEntries.slice(batchIndex * BATCH_SIZE, (batchIndex + 1) * BATCH_SIZE)
        if (!batch.length) continue

        vercelBulkProgress.currentBatch = batchIndex + 1

        const runningIndices = new Set(batch.map((item) => item.originalIndex))
        vercelBulkResults = vercelBulkResults.map((result, idx) =>
          runningIndices.has(idx) ? { ...result, status: 'running' } : result
        )

        await Promise.all(
          batch.map(async (entry) => {
            const index = entry.originalIndex
            try {
              const res = await api.deployVercelRelay({
                vercelToken: entry.token,
                projectName: entry.projectName
              })

              successCount += 1
              vercelBulkResults = vercelBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'success', deployUrl: res.deployUrl || res.proxyUrl || 'Deployed' }
                  : result
              )
              vercelBulkProgress.completed += 1
              vercelBulkProgress.success += 1
            } catch (error) {
              failedCount += 1
              vercelBulkResults = vercelBulkResults.map((result, resultIndex) =>
                resultIndex === index
                  ? { ...result, status: 'failed', error: error instanceof Error ? error.message : 'Deploy failed' }
                  : result
              )
              vercelBulkProgress.completed += 1
              vercelBulkProgress.failed += 1
            }
          })
        )
      }

      await onDeployed()
      if (failedCount > 0) {
        notifications.error(`${successCount} deployed, ${failedCount} failed`)
      } else {
        notifications.success(`${successCount} Vercel relays deployed`)
      }
    } catch (error) {
      notifications.error(error instanceof Error ? error.message : 'Bulk deploy failed')
    } finally {
      bulkVercelDeploying = false
    }
  }

  async function handleDenoDeploy() {
    if (!denoForm.denoToken.trim() || !denoForm.orgDomain.trim()) return
    deploying = true
    try {
      const data = await api.deployDenoRelay({
        denoToken: denoForm.denoToken.trim(),
        orgDomain: denoForm.orgDomain.trim(),
        projectName: denoForm.projectName.trim() || undefined,
      })
      await onDeployed()
      closeDenoModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Deno relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }

  async function handleNetlifyDeploy() {
    if (!netlifyForm.netlifyToken.trim()) return
    deploying = true
    try {
      const data = await api.deployNetlifyRelay({
        netlifyToken: netlifyForm.netlifyToken.trim(),
        projectName: netlifyForm.projectName.trim() || undefined,
      })
      await onDeployed()
      closeNetlifyModal()
      notifications.success(`Deployed: ${data.deployUrl || data.proxyUrl || ''}`)
    } catch (err) {
      console.log('Error deploying Netlify relay:', err)
      notifications.error(err instanceof Error ? err.message : 'Deploy failed')
    } finally {
      deploying = false
    }
  }
</script>

        <div class="relative" bind:this={relayMenuRef}>
          <Button
            size="sm"
            variant="secondary"
            onclick={() => (showRelayMenu = !showRelayMenu)}
          >
            <span class="material-symbols-outlined text-[18px]">rocket_launch</span>
            Deploy Relay
            <span class="material-symbols-outlined ml-1 text-[18px]">
              {showRelayMenu ? 'expand_less' : 'expand_more'}
            </span>
          </Button>

          {#if showRelayMenu}
            <div
              class="absolute left-0 top-full z-50 mt-1 w-48 rounded-xl border border-border bg-surface p-1 shadow-[var(--shadow-elev)] sm:left-auto sm:right-0"
            >
              {#each relayMenuItems as item (item.label)}
                <button
                  type="button"
                  onclick={() => {
                    item.open()
                    showRelayMenu = false
                  }}
                  class="flex w-full cursor-pointer items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-main transition-colors hover:bg-surface-2"
                >
                  <span class="material-symbols-outlined text-[20px] text-text-muted">{item.icon}</span>
                  {item.label}
                </button>
              {/each}
            </div>
          {/if}
        </div>

    <Modal isOpen={showVercelModal} title="Deploy Vercel Relay" onClose={closeVercelModal}>
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-blue-500/5 border border-blue-500/10 p-3 flex flex-col gap-1.5">
          <p class="text-sm text-text-main font-medium">What is Vercel Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys an edge relay function to Vercel. All AI provider requests will be forwarded
            through Vercel's edge network, masking your real IP from providers.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>
              Your IP is replaced by Vercel's dynamic edge IPs (hundreds of IPs across 20+ global
              regions)
            </li>
            <li>
              Vercel serves millions of apps — providers can't block Vercel IPs without affecting
              legitimate traffic
            </li>
            <li>Free tier: 100GB bandwidth/month, 500K edge invocations</li>
            <li>Deploy multiple relays on different accounts for more IP diversity</li>
          </ul>
        </div>
        <div>
          <Input
            label="Vercel API Token"
            type="password"
            bind:value={vercelForm.vercelToken}
            placeholder="your-vercel-api-token"
          />
          <p class="text-xs text-text-muted mt-1">
            Token is used once for deployment and not stored.
            <a
              href="https://vercel.com/account/tokens"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline"
            >
              Get token →
            </a>
          </p>
        </div>
        <Input
          label="Project Name"
          bind:value={vercelForm.projectName}
          placeholder="my-relay"
          hint="Unique name for your Vercel project. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleVercelDeploy}
            disabled={!vercelForm.vercelToken.trim() || deploying}
          >
            {deploying ? 'Deploying... (may take ~1 min)' : 'Deploy'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeVercelModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showVercelBulkModal}
      title="Vercel Bulk"
      size="3xl"
      onClose={closeVercelBulkModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-sky-500/10 border border-sky-500/20 p-3 flex flex-col gap-1">
          <p class="text-sm font-medium text-text-main">One account / token per line (Batch of 10 concurrent)</p>
          <p class="text-xs text-text-muted">
            Format: <code class="font-mono text-sky-500">name/email|vercelToken</code> atau langsung <code class="font-mono text-sky-500">vercelToken</code>. Project name otomatis memakai Pool Name + urutan batch.
          </p>
        </div>

        <Input
          label="Pool Name / Project Prefix"
          bind:value={vercelBulkPoolName}
          disabled={bulkVercelDeploying}
          placeholder="e.g. vercel-relay, vc-indo, vc-pool"
          hint="Prefix untuk nama pool dan project Vercel (contoh: vercel-relay-1, vercel-relay-2)."
        />

        <div class="flex flex-col gap-1.5">
          <label for="vercel-bulk-textarea" class="text-xs font-medium text-text-muted">Tokens List</label>
          <textarea
            id="vercel-bulk-textarea"
            bind:value={vercelBulkText}
            disabled={bulkVercelDeploying}
            placeholder={"team-a|vcp_xxxxxxxx...\nteam-b|vcp_yyyyyyyy...\nvcp_zzzzzzzz..."}
            spellcheck="false"
            rows={10}
            class="min-h-[220px] w-full resize-y rounded-lg border border-border bg-surface px-3 py-2 font-mono text-xs text-text-main outline-none focus:border-brand-500 disabled:opacity-50"
          ></textarea>
        </div>

        {#if vercelBulkProgress.total > 0}
          <div class="rounded-lg border border-border bg-surface p-3 flex flex-col gap-2">
            <div class="flex items-center justify-between text-xs font-medium">
              <span class="text-text-main flex items-center gap-1.5">
                {#if bulkVercelDeploying}
                  <span class="inline-block size-2 rounded-full bg-brand-500 animate-pulse"></span>
                {/if}
                {bulkVercelDeploying
                  ? `Deploying Batch ${vercelBulkProgress.currentBatch} of ${vercelBulkProgress.totalBatches}...`
                  : 'Deployment Complete'}
              </span>
              <span class="font-mono text-text-muted">
                {vercelBulkProgress.completed} / {vercelBulkProgress.total} ({Math.round(
                  (vercelBulkProgress.completed / vercelBulkProgress.total) * 100
                ) || 0}%)
              </span>
            </div>

            <div class="flex h-2 w-full overflow-hidden rounded-full bg-surface-2">
              <div
                class="bg-emerald-500 transition-all duration-300"
                style="width: {(vercelBulkProgress.success / vercelBulkProgress.total) * 100}%"
                title="{vercelBulkProgress.success} succeeded"
              ></div>
              <div
                class="bg-red-500 transition-all duration-300"
                style="width: {(vercelBulkProgress.failed / vercelBulkProgress.total) * 100}%"
                title="{vercelBulkProgress.failed} failed"
              ></div>
            </div>

            <div class="flex items-center justify-between text-[11px] text-text-muted pt-0.5">
              <div class="flex items-center gap-3">
                <span class="flex items-center gap-1 text-emerald-500 font-medium">
                  <span class="size-1.5 rounded-full bg-emerald-500"></span>
                  {vercelBulkProgress.success} Success
                </span>
                <span class="flex items-center gap-1 text-red-500 font-medium">
                  <span class="size-1.5 rounded-full bg-red-500"></span>
                  {vercelBulkProgress.failed} Failed
                </span>
              </div>
              <span>Concurrency: 10/batch</span>
            </div>
          </div>
        {/if}

        {#if vercelBulkResults.length > 0}
          <div class="max-h-60 overflow-y-auto rounded-lg border border-border divide-y divide-border">
            {#each vercelBulkResults as result}
              <div class="flex items-start gap-3 p-2.5 text-xs">
                <span class="w-6 shrink-0 text-text-muted font-mono">#{result.lineNumber}</span>
                <div class="min-w-0 flex-1 flex flex-col gap-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="font-medium text-text-main">{result.label}</span>
                    <Badge
                      size="sm"
                      variant={result.status === 'success'
                        ? 'success'
                        : result.status === 'failed'
                          ? 'error'
                          : result.status === 'running'
                            ? 'warning'
                            : 'neutral'}
                    >
                      {result.status}
                    </Badge>
                  </div>
                  <p class="font-mono text-text-muted text-[11px]">
                    {result.tokenMasked} · {result.projectName}
                  </p>
                  {#if result.deployUrl}
                    <p class="break-all text-emerald-500 text-[11px] font-mono">{result.deployUrl}</p>
                  {/if}
                  {#if result.error}
                    <p class="break-words text-red-500 text-[11px]">{result.error}</p>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleVercelBulkDeploy}
            disabled={!vercelBulkText.trim() || bulkVercelDeploying}
          >
            {bulkVercelDeploying
              ? `Deploying (${vercelBulkProgress.completed}/${vercelBulkProgress.total})...`
              : 'Deploy All'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeVercelBulkModal} disabled={bulkVercelDeploying}>
            Close
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showCloudflareModal}
      title="Deploy Cloudflare Relay"
      onClose={closeCloudflareModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-orange-500/5 border border-orange-500/10 p-3 flex flex-col gap-1.5">
          <p class="text-sm text-text-main font-medium">What is Cloudflare Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a Cloudflare Worker as a proxy relay. All AI provider requests will be forwarded
            through Cloudflare's global edge network.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>High performance global routing and IP masking via Cloudflare Workers</li>
            <li>Free tier: 100,000 requests per day</li>
            <li>Requires Cloudflare Account ID and a Workers API Token (Edit Workers permission)</li>
          </ul>
          <div class="mt-2 pt-2 border-t border-orange-500/10 text-xs text-text-muted">
            <p class="font-medium text-text-main mb-1">How to generate your API Token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>My Profile</b> → <b>API Tokens</b> → <b>Create Token</b></li>
              <li>Scroll down to <b>Custom Token</b> and click <b>Get started</b></li>
              <li>Under <b>Permissions</b>: Account | Workers Scripts | Edit</li>
              <li>
                Under <b>Account Resources</b>: Include | Account | <i>Your Account Name</i>
              </li>
              <li>Click <b>Continue to summary</b> → <b>Create Token</b></li>
            </ol>
          </div>
        </div>
        <Input
          label="Account ID"
          bind:value={cloudflareForm.accountId}
          placeholder="your-cloudflare-account-id"
          hint="Found on the right side of the Cloudflare dashboard overview page."
        />
        <div>
          <Input
            label="API Token"
            type="password"
            bind:value={cloudflareForm.apiToken}
            placeholder="your-cloudflare-api-token"
          />
          <p class="text-xs text-text-muted mt-1">
            Requires "Workers Scripts: Edit" permission.
            <a
              href="https://dash.cloudflare.com/profile/api-tokens"
              target="_blank"
              rel="noopener noreferrer"
              class="text-primary hover:underline"
            >
              Get token →
            </a>
          </p>
        </div>
        <Input
          label="Worker Name"
          bind:value={cloudflareForm.projectName}
          placeholder="my-relay"
          hint="Unique name for your Cloudflare Worker. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleCloudflareDeploy}
            disabled={!cloudflareForm.accountId.trim() ||
              !cloudflareForm.apiToken.trim() ||
              deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Worker'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeCloudflareModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showCloudflareBulkModal}
      title="CF Bulk"
      size="3xl"
      onClose={closeCloudflareBulkModal}
    >
      <div class="flex flex-col gap-4">
        <div class="rounded-lg bg-amber-500/10 border border-amber-500/20 p-3 flex flex-col gap-1">
          <p class="text-sm font-medium text-text-main">One account per line (Batch of 10 concurrent)</p>
          <p class="text-xs text-text-muted">
            Format: <code class="font-mono text-amber-500">name/email|accountID|apiToken</code>. Nama worker/subdomain otomatis memakai Pool Name + urutan batch (bukan nama/email akun).
          </p>
        </div>

        <Input
          label="Pool Name / Worker Prefix"
          bind:value={cloudflareBulkPoolName}
          disabled={bulkCloudflareDeploying}
          placeholder="e.g. cloudflare-relay, cf-indo, cf-pool"
          hint="Prefix untuk nama pool dan subdomain worker Cloudflare (contoh: cloudflare-relay-1, cloudflare-relay-2)."
        />

        <div class="flex flex-col gap-1.5">
          <label for="cf-bulk-textarea" class="text-xs font-medium text-text-muted">Accounts List</label>
          <textarea
            id="cf-bulk-textarea"
            bind:value={cloudflareBulkText}
            disabled={bulkCloudflareDeploying}
            placeholder={"email@example.com|account-id|api-token\nteam-account|account-id|api-token"}
            spellcheck="false"
            rows={10}
            class="min-h-[220px] w-full resize-y rounded-lg border border-border bg-surface px-3 py-2 font-mono text-xs text-text-main outline-none focus:border-brand-500 disabled:opacity-50"
          ></textarea>
        </div>

        {#if cloudflareBulkProgress.total > 0}
          <div class="rounded-lg border border-border bg-surface p-3 flex flex-col gap-2">
            <div class="flex items-center justify-between text-xs font-medium">
              <span class="text-text-main flex items-center gap-1.5">
                {#if bulkCloudflareDeploying}
                  <span class="inline-block size-2 rounded-full bg-brand-500 animate-pulse"></span>
                {/if}
                {bulkCloudflareDeploying
                  ? `Deploying Batch ${cloudflareBulkProgress.currentBatch} of ${cloudflareBulkProgress.totalBatches}...`
                  : 'Deployment Complete'}
              </span>
              <span class="font-mono text-text-muted">
                {cloudflareBulkProgress.completed} / {cloudflareBulkProgress.total} ({Math.round(
                  (cloudflareBulkProgress.completed / cloudflareBulkProgress.total) * 100
                ) || 0}%)
              </span>
            </div>

            <div class="flex h-2 w-full overflow-hidden rounded-full bg-surface-2">
              <div
                class="bg-emerald-500 transition-all duration-300"
                style="width: {(cloudflareBulkProgress.success / cloudflareBulkProgress.total) * 100}%"
                title="{cloudflareBulkProgress.success} succeeded"
              ></div>
              <div
                class="bg-red-500 transition-all duration-300"
                style="width: {(cloudflareBulkProgress.failed / cloudflareBulkProgress.total) * 100}%"
                title="{cloudflareBulkProgress.failed} failed"
              ></div>
            </div>

            <div class="flex items-center justify-between text-[11px] text-text-muted pt-0.5">
              <div class="flex items-center gap-3">
                <span class="flex items-center gap-1 text-emerald-500 font-medium">
                  <span class="size-1.5 rounded-full bg-emerald-500"></span>
                  {cloudflareBulkProgress.success} Success
                </span>
                <span class="flex items-center gap-1 text-red-500 font-medium">
                  <span class="size-1.5 rounded-full bg-red-500"></span>
                  {cloudflareBulkProgress.failed} Failed
                </span>
              </div>
              <span>Concurrency: 10/batch</span>
            </div>
          </div>
        {/if}

        {#if cloudflareBulkResults.length > 0}
          <div class="max-h-60 overflow-y-auto rounded-lg border border-border divide-y divide-border">
            {#each cloudflareBulkResults as result}
              <div class="flex items-start gap-3 p-2.5 text-xs">
                <span class="w-6 shrink-0 text-text-muted font-mono">#{result.lineNumber}</span>
                <div class="min-w-0 flex-1 flex flex-col gap-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="font-medium text-text-main">{result.label}</span>
                    <Badge
                      size="sm"
                      variant={result.status === 'success'
                        ? 'success'
                        : result.status === 'failed'
                          ? 'error'
                          : result.status === 'running'
                            ? 'warning'
                            : 'neutral'}
                    >
                      {result.status}
                    </Badge>
                  </div>
                  <p class="font-mono text-text-muted text-[11px]">
                    {result.accountId} · {result.projectName}
                  </p>
                  {#if result.deployUrl}
                    <p class="break-all text-emerald-500 text-[11px] font-mono">{result.deployUrl}</p>
                  {/if}
                  {#if result.error}
                    <p class="break-words text-red-500 text-[11px]">{result.error}</p>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}

        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleCloudflareBulkDeploy}
            disabled={!cloudflareBulkText.trim() || bulkCloudflareDeploying}
          >
            {bulkCloudflareDeploying
              ? `Deploying (${cloudflareBulkProgress.completed}/${cloudflareBulkProgress.total})...`
              : 'Deploy All'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeCloudflareBulkModal} disabled={bulkCloudflareDeploying}>
            Close
          </Button>
        </div>
      </div>
    </Modal>

    <Modal isOpen={showDenoModal} title="Deploy Deno Relay" onClose={closeDenoModal}>
      <div class="flex flex-col gap-4">
        <div
          class="rounded-lg bg-black/5 dark:bg-white/5 border border-black/10 dark:border-white/10 p-3 flex flex-col gap-1.5"
        >
          <p class="text-sm text-text-main font-medium">What is Deno Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a relay worker to Deno Deploy's global edge network. All AI provider requests
            are forwarded through Deno's edge, masking your real IP.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>Deno Deploy v2 runs on a high-performance global edge network</li>
            <li>Free tier: 1M requests & 100GiB outbound traffic per month</li>
            <li>No per-request CPU time limits (unlike Vercel/Cloudflare)</li>
            <li>Support up to 20 active apps & 50 custom domains</li>
            <li>Deploy multiple relays for maximum IP diversity</li>
          </ul>
          <div
            class="mt-2 pt-2 border-t border-black/10 dark:border-white/10 text-xs text-text-muted"
          >
            <p class="font-medium text-text-main mb-1">How to generate API token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>console.deno.com</b></li>
              <li>Select your <b>Organization</b> → <b>Settings</b> → <b>Organization Tokens</b></li>
              <li>Create a <b>Organization Token</b> (prefix <b>ddo_</b>)</li>
            </ol>
          </div>
        </div>
        <div>
          <Input
            label="Deno Deploy API Token"
            type="password"
            bind:value={denoForm.denoToken}
            placeholder="ddo_xxxxxxxxxxxxxxxx"
          />
          <p class="text-xs text-text-muted mt-1">
            Token is used once for deployment, not stored. Found in Organization Settings.
          </p>
        </div>
        <Input
          label="Organization Domain"
          bind:value={denoForm.orgDomain}
          placeholder="your-org.deno.net"
          hint="Organization's default domain. Your relay URL will be in the format: https://my-relay.your-org.deno.net"
        />
        <Input
          label="App Name"
          bind:value={denoForm.projectName}
          placeholder="deno-relay"
          hint="Unique app name. Leave empty for auto-generated name."
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleDenoDeploy}
            disabled={!denoForm.denoToken.trim() || !denoForm.orgDomain.trim() || deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Relay'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeDenoModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>

    <Modal
      isOpen={showNetlifyModal}
      title="Deploy Netlify Relay"
      onClose={closeNetlifyModal}
    >
      <div class="flex flex-col gap-4">
        <div
          class="rounded-lg bg-black/5 dark:bg-white/5 border border-black/10 dark:border-white/10 p-3 flex flex-col gap-1.5"
        >
          <p class="text-sm text-text-main font-medium">What is Netlify Relay?</p>
          <p class="text-xs text-text-muted">
            Deploys a serverless relay function to Netlify's global edge network. All AI provider
            requests are forwarded through Netlify's edge, masking your real IP.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-4 space-y-0.5">
            <li>
              Runs on Netlify Functions with a streaming response and a 30-second execution limit
            </li>
            <li>Free tier: 125,000 function invocations and 100GB bandwidth per month</li>
            <li>Relay URL format: https://your-site.netlify.app/.netlify/functions/relay</li>
            <li>Deploy multiple relays on different accounts for more IP diversity</li>
          </ul>
          <div
            class="mt-2 pt-2 border-t border-black/10 dark:border-white/10 text-xs text-text-muted"
          >
            <p class="font-medium text-text-main mb-1">How to generate API token:</p>
            <ol class="list-decimal pl-4 space-y-0.5">
              <li>Go to <b>User Settings</b> → <b>Applications</b> → <b>Personal access tokens</b></li>
              <li>Select <b>New access token</b> and generate a token</li>
              <li>Copy the token — you won't see it again after leaving the page</li>
            </ol>
          </div>
        </div>
        <div>
          <Input
            label="Netlify API Token"
            type="password"
            bind:value={netlifyForm.netlifyToken}
            placeholder="nfp_xxxxxxxxxxxxxxxx"
          />
          <p class="text-xs text-text-muted mt-1">Token is used once for deployment, not stored.</p>
        </div>
        <Input
          label="Site Name"
          bind:value={netlifyForm.projectName}
          placeholder="netlify-relay"
          hint="Unique site name (lowercase letters, numbers, hyphens). Your relay URL will be https://site-name.netlify.app/.netlify/functions/relay"
        />
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <Button
            fullWidth
            onclick={handleNetlifyDeploy}
            disabled={!netlifyForm.netlifyToken.trim() || deploying}
          >
            {deploying ? 'Deploying...' : 'Deploy Relay'}
          </Button>
          <Button fullWidth variant="ghost" onclick={closeNetlifyModal} disabled={deploying}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>
