<script lang="ts">
  import { api, type JailbreakImportEntry, type JailbreakImportResult } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'

  interface Props {
    /** Which import surface to show — the editor modal's From files / From URL tab. */
    tab: 'files' | 'source'
    /** Reload presets + bindings + usage after an import lands. */
    onImported: () => Promise<void> | void
  }

  let { tab, onImported }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  // Repository / URL import. Several URLs may be pasted at once, one per
  // line; each becomes its own preset. The first line is also what the
  // single-line field shows, so pasting many lines is not a separate mode.
  let importTarget = $state('')
  let importing = $state(false)
  let importStatus = $state('')
  let importResult = $state<JailbreakImportResult | null>(null)
  let importFileName = $state('')
  // Picked .md files waiting to be uploaded. Each entry keeps the File so the
  // upload can read it, and the list is rendered with a remove button per row.
  let pickedFiles = $state<File[]>([])
  let uploading = $state(false)
  let dragging = $state(false)

  // The URLs the operator pasted, one per line. Blank lines are dropped, so a
  // trailing newline from a copy-paste does not become an empty target.
  let importUrls = $derived(
    importTarget
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean),
  )
  // A suggested file name for the first URL, derived from its host and path:
  // github.com/owner/repo -> repo.md, paste.example.com/abc123 -> abc123.md.
  // It fills the placeholder only while the field is empty, so an operator who
  // typed a name keeps it.
  let importNameSuggestion = $derived.by(() => {
    const first = importUrls[0]
    if (!first) return ''
    try {
      const u = new URL(first)
      const seg = u.pathname.split('/').filter(Boolean).pop()
      const base = (seg || u.hostname).replace(/\.[a-z]{2,4}$/i, '')
      return base ? base + '.md' : u.hostname + '.md'
    } catch {
      return ''
    }
  })
  // Import log rendered as one line per file, so a 34-file import reads like a
  // terminal transcript rather than a nested list.
  let importLogText = $derived.by(() => {
    if (!importResult) return ''
    const lines: string[] = []
    for (const e of importResult.imported) {
      const kw = e.keywords?.length ? `  matches: ${e.keywords.join(', ')}` : ''
      const kind = e.kind ? `  [${e.kind}]` : ''
      lines.push(`+ ${e.name}${kind}  ${e.path}${kw}`)
    }
    for (const f of importResult.refused) {
      lines.push(`- ${f.path}  ${f.reason}`)
    }
    return lines.join('\n')
  })

  async function importFromGithub() {
    const urls = importUrls
    if (urls.length === 0) {
      importStatus = 'Enter a repository or URL first, e.g. togg53192-cmd/jailbreaks'
      return
    }
    importing = true
    importStatus = ''
    importResult = null
    const imported: JailbreakImportEntry[] = []
    const refused: { path: string; reason: string }[] = []
    for (const url of urls) {
      try {
        // The custom name applies to the first URL only: with several URLs
        // each preset is named after its own source, which is the only way to
        // tell them apart.
        const name = urls.length === 1 ? importFileName : ''
        const res = await api.importJailbreak(url, name)
        imported.push(...res.imported)
        refused.push(...res.refused)
      } catch (e) {
        refused.push({ path: url, reason: errorMessage(e) })
      }
    }
    importResult = { ok: refused.length === 0, source: urls.join(', '), imported, refused }
    importStatus =
      refused.length === 0
        ? `Imported ${imported.length} preset(s).`
        : `Imported ${imported.length}, skipped ${refused.length}.`
    importing = false
    await onImported()
  }
  // Picking files only queues them; the Upload button sends the whole queue.
  // A file already queued under the same name+size is skipped, so picking the
  // same file twice does not upload it twice.
  function queueFiles(fileList: FileList | null) {
    if (!fileList) return
    for (const file of Array.from(fileList)) {
      const dupe = pickedFiles.some((f) => f.name === file.name && f.size === file.size)
      if (!dupe) pickedFiles = [...pickedFiles, file]
    }
  }
  function removePickedFile(index: number) {
    pickedFiles = pickedFiles.filter((_, i) => i !== index)
  }
  function clearPickedFiles() {
    pickedFiles = []
  }
  async function uploadPickedFiles() {
    if (pickedFiles.length === 0) return
    uploading = true
    importStatus = ''
    importResult = null
    const imported: JailbreakImportEntry[] = []
    const refused: { path: string; reason: string }[] = []
    for (const file of pickedFiles) {
      try {
        const res = await api.uploadJailbreakPreset(file)
        imported.push(res.preset)
      } catch (e) {
        refused.push({ path: file.name, reason: errorMessage(e) })
      }
    }
    importResult = { ok: refused.length === 0, source: 'upload', imported, refused }
    importStatus =
      refused.length === 0
        ? `Uploaded ${imported.length} preset(s).`
        : `Uploaded ${imported.length}, skipped ${refused.length}.`
    pickedFiles = []
    uploading = false
    await onImported()
  }
  // The drop zone accepts files (queued for upload) or a URL (imported directly).
  function onDrop(event: DragEvent) {
    event.preventDefault()
    dragging = false
    if (event.dataTransfer?.files?.length) {
      queueFiles(event.dataTransfer.files)
      return
    }
    const text = event.dataTransfer?.getData('text/uri-list') || event.dataTransfer?.getData('text/plain') || ''
    const url = text.split('\n')[0].trim()
    if (url) {
      importTarget = url
      void importFromGithub()
    }
  }
</script>

{#if tab === 'files'}
<div
  class="space-y-3"
  role="group"
  aria-label="Import from files"
  ondragover={(e) => {
    e.preventDefault()
    dragging = true
  }}
  ondragleave={(e) => {
    if (e.currentTarget === e.target) dragging = false
  }}
  ondrop={onDrop}
>
  <p class="text-xs text-text-subtle">
    Pick or drop several files at once. The file name becomes the preset name and its keyword
    chain, so a preset covers that model on every provider and combo leg.
  </p>
  <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-text-subtle">
    <span class="font-medium">Accepted:</span>
    <span class="font-code text-text-muted">.md</span>
    <span class="font-code text-text-muted">.markdown</span>
    <span class="font-code text-text-muted">.txt</span>
    <span class="text-border">·</span>
    <span>instruction files such as</span>
    <span class="font-code text-text-muted">SOUL.md</span>
    <span class="font-code text-text-muted">AGENTS.md</span>
    <span class="font-code text-text-muted">CLAUDE.md</span>
    <span class="font-code text-text-muted">PERSONA.md</span>
    <span class="font-code text-text-muted">LOAD.md</span>
  </div>
  <label
    class="relative flex h-[calc(6*1.5rem+1rem+2px)] w-full cursor-pointer flex-col items-center justify-center gap-1 rounded-brand border border-dashed border-border bg-surface-2 px-3 py-2 text-center transition-colors {dragging
      ? 'border-brand-500 bg-brand-500/10'
      : 'hover:border-border'} {uploading ? 'opacity-50' : ''}"
  >
    <input
      type="file"
      accept=".md,.markdown,.txt"
      multiple
      onchange={(e) => {
        queueFiles(e.currentTarget.files)
        e.currentTarget.value = ''
      }}
      disabled={uploading}
      aria-label="Pick prompt files"
      class="absolute inset-0 h-full w-full cursor-pointer opacity-0"
    />
    <span class="material-symbols-outlined text-[22px] text-text-subtle">upload_file</span>
    <span class="text-xs font-medium text-text-main">
      {dragging ? 'Drop to queue' : 'Pick or drop several files'}
    </span>
    <span class="text-[11px] text-text-subtle">.md .markdown .txt — one Upload sends the whole queue</span>
  </label>
  {#if pickedFiles.length > 0}
    <ul class="divide-y divide-border rounded-brand border border-border-subtle">
      {#each pickedFiles as file, index (file.name + '|' + file.size)}
        <li class="flex items-center gap-3 px-3 py-2">
          <span class="material-symbols-outlined shrink-0 text-[16px] text-text-subtle">description</span>
          <span class="flex-1 truncate text-xs text-text-main">{file.name}</span>
          <span class="shrink-0 text-[11px] text-text-subtle">{(file.size / 1024).toFixed(1)} KB</span>
          <button
            type="button"
            onclick={() => removePickedFile(index)}
            aria-label={`Remove ${file.name}`}
            class="shrink-0 rounded-brand p-1 leading-none text-text-subtle transition-colors hover:bg-surface-2 hover:text-error"
          >
            <span class="material-symbols-outlined text-[16px]">close</span>
          </button>
        </li>
      {/each}
    </ul>
  {/if}
  <div class="flex flex-wrap items-center justify-center gap-2 border-t border-border-subtle pt-4">
    <Button variant="primary" size="sm" class="min-w-[140px]" onclick={uploadPickedFiles} disabled={uploading || pickedFiles.length === 0}>
      {uploading ? 'Uploading…' : pickedFiles.length > 1 ? `Upload ${pickedFiles.length} files` : 'Upload'}
    </Button>
    {#if pickedFiles.length > 0}
      <button type="button" onclick={clearPickedFiles} class="text-xs text-text-muted hover:text-error hover:underline">
        Clear all
      </button>
    {/if}
  </div>
  {#if importStatus}<p class="text-center text-xs text-text-muted">{importStatus}</p>{/if}
</div>
{:else}
<div class="space-y-4">
  <p class="text-xs text-text-subtle">
    One URL per line. A GitHub repository pulls every prompt file in it; any other http(s)
    address — GitLab, pastebin, hastebin, a raw file — is read as one prompt.
  </p>
  <textarea
    bind:value={importTarget}
    rows="6"
    placeholder={'owner/repo, owner/repo@branch\nhttps://github.com/owner/repo\nhttps://gitlab.com/owner/repo/-/raw/main/SOUL.md\nhttps://paste.example.com/abc123'}
    aria-label="Repositories or URLs to import, one per line"
    class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 font-code text-sm text-text-main outline-none placeholder:text-text-subtle focus:border-brand-500 resize-y"
  ></textarea>
  <div class="flex flex-wrap items-center justify-center gap-2">
    <input
      type="text"
      bind:value={importFileName}
      placeholder={importNameSuggestion || 'my-prompt.md (optional)'}
      aria-label="Custom file name for imported preset"
      class="w-[240px] rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 text-sm text-text-main placeholder:text-text-subtle"
    />
    <Button variant="primary" size="sm" class="min-w-[140px]" onclick={importFromGithub} disabled={importing}>
      {importing ? 'Importing…' : importUrls.length > 1 ? `Import ${importUrls.length} URLs` : 'Import'}
    </Button>
  </div>
  {#if importUrls.length > 1}
    <p class="text-center text-[11px] text-text-subtle">Each URL becomes its own preset, named after its source.</p>
  {/if}
  {#if importStatus}<p class="text-center text-xs text-text-muted">{importStatus}</p>{/if}
</div>
{/if}

{#if importResult}
  <div class="mt-4 space-y-1 border-t border-border-subtle pt-4">
    <h3 class="text-xs font-semibold text-text-main">
      Import log
      <span class="font-normal text-text-subtle">
        ({importResult.imported.length} imported, {importResult.refused.length} skipped)
      </span>
    </h3>
    <textarea
      readonly
      value={importLogText}
      rows={Math.min(12, Math.max(4, importResult.imported.length + importResult.refused.length))}
      aria-label="Import log"
      class="w-full rounded-brand border border-border-subtle bg-surface-2 px-3 py-2 font-code text-[11px] leading-relaxed text-text-main outline-none resize-y"
    ></textarea>
  </div>
{/if}
