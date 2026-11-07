<script lang="ts">
  // Port of upstream src/app/(dashboard)/dashboard/providers/[id]/BulkImportJwtModal.js
  // CodeBuddy-family bulk import: paste many JWTs / account JSON at once;
  // the server parses, dedupes and creates one connection per token
  // (POST /api/oauth/{provider}/bulk-jwt), answering counts plus a
  // per-entry result list. Entries carrying both tokens become OAuth
  // connections with auto-refresh; the rest stay plain token connections.
  import Button from '../../lib/ui/Button.svelte'
  import { api, type BulkJwtImportResult } from '../../api/client'

  interface Props {
    isOpen: boolean
    providerId: string
    providerName?: string
    onClose: () => void
    /** Called after an import that created at least one connection. */
    onImported?: () => void
  }

  let { isOpen, providerId, providerName = '', onClose, onImported }: Props = $props()

  let text = $state('')
  let importing = $state(false)
  let error = $state('')
  let result = $state<BulkJwtImportResult | null>(null)

  let failedItems = $derived(result ? result.results.filter((r) => !r.ok) : [])

  // Reset the form each time the modal opens (upstream resets on open).
  $effect(() => {
    if (isOpen) {
      text = ''
      error = ''
      result = null
      importing = false
    }
  })

  async function handleImport() {
    if (!text.trim() || importing) return
    importing = true
    error = ''
    result = null
    try {
      const res = await api.bulkImportJwt(providerId, text)
      result = res
      if (res.success > 0) onImported?.()
    } catch (err) {
      error = err instanceof Error ? err.message : 'Import failed'
    } finally {
      importing = false
    }
  }
</script>

{#if isOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div
      class="absolute inset-0 bg-black/50 fade-in"
      onclick={onClose}
      role="presentation"
    ></div>
    <div class="relative w-full bg-surface border border-border rounded-brand-lg shadow-[var(--shadow-elev)] fade-in max-w-lg">
      <div class="flex items-center justify-between p-2 border-b border-border">
        <div class="flex items-center">
          <h2 class="text-sm font-semibold text-text-main">Bulk Import — JWT / Token</h2>
        </div>
        <button
          type="button"
          onclick={onClose}
          aria-label="Close"
          class="p-1.5 rounded-sm text-text-muted hover:bg-surface-2 hover:text-text-main transition-colors cursor-pointer"
        >
          <span class="material-symbols-outlined text-[20px]">close</span>
        </button>
      </div>

      <div class="p-6 max-h-[calc(85vh-100px)] overflow-y-auto custom-scrollbar">
        <div class="flex flex-col gap-4">
          <p class="text-xs text-text-muted">
            {providerName || providerId} · paste many tokens at once. One connection is created per
            token (priority 3). Duplicates already in the provider are skipped.
          </p>
          <ul class="text-xs text-text-muted list-disc pl-5 flex flex-col gap-1">
            <li><code>name|token</code> per line, or a bare token per line</li>
            <li>
              Account JSON — object, array, or <code>{'{"accounts": [...]}'}</code> with
              <code>accessToken</code> / <code>refreshToken</code>
            </li>
            <li>Entries with both tokens become OAuth connections with auto-refresh</li>
          </ul>

          <textarea
            class="w-full rounded border border-accent/30 bg-sidebar p-2 text-sm font-mono resize-y min-h-[180px] text-text-main focus:outline-none focus:ring-1 focus:ring-primary"
            placeholder={'{"accessToken":"eyJ...","refreshToken":"eyJ..."}\nname|eyJ...\neyJ...'}
            bind:value={text}
          ></textarea>

          {#if error}
            <p class="text-sm text-red-500">{error}</p>
          {/if}

          {#if result}
            <div class="flex flex-col gap-2">
              <p
                class="text-sm font-medium {result.failed > 0 && result.success === 0
                  ? 'text-red-500'
                  : result.failed > 0
                    ? 'text-yellow-500'
                    : 'text-green-500'}"
              >
                ✓ {result.success} added{#if result.skipped}
                  · ⊘ {result.skipped} duplicates skipped{/if}{#if result.failed}
                  · ✗ {result.failed} failed{/if}
              </p>
              {#if failedItems.length > 0}
                <ul class="text-xs text-red-500 list-disc pl-5 max-h-32 overflow-y-auto custom-scrollbar">
                  {#each failedItems as item (item.index)}
                    <li>[{item.index}] {item.error || 'failed'}</li>
                  {/each}
                </ul>
              {/if}
            </div>
          {/if}

          <div class="flex gap-2">
            <Button onclick={handleImport} fullWidth disabled={importing || !text.trim()}>
              {importing ? 'Importing...' : 'Import Tokens'}
            </Button>
            <Button onclick={onClose} variant="ghost" fullWidth>Cancel</Button>
          </div>
        </div>
      </div>
    </div>
  </div>
{/if}
