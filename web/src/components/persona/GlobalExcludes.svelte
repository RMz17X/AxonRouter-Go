<script lang="ts">
  import { api, type JailbreakKeyword } from '../../api/client'
  import Button from '../../lib/ui/Button.svelte'
  import Card from '../../lib/ui/Card.svelte'
  import KeywordRules from '../KeywordRules.svelte'

  interface Props {
    /** The one global deny list that overrides every preset and binding. */
    excludes: JailbreakKeyword[]
  }

  let { excludes = $bindable() }: Props = $props()

  function errorMessage(e: unknown): string {
    return e instanceof Error ? e.message : String(e)
  }

  let globalSaving = $state(false)
  let globalStatus = $state('')

  async function saveGlobalExcludes() {
    globalSaving = true
    globalStatus = ''
    try {
      const res = await api.saveJailbreakExcludes(excludes)
      excludes = res?.excludes || excludes
      globalStatus = 'Global exclude list saved.'
    } catch (e) {
      globalStatus = 'Save failed: ' + errorMessage(e)
    } finally {
      globalSaving = false
    }
  }
</script>

<!-- Global exclude list. The save action sits in the header beside the
     title, the same place New preset sits in the segment bar, so the page
     reads with one convention. The rules below own the full width. -->
<Card padding="sm" class="bg-surface-2/60">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <div class="space-y-1">
      <h2 class="text-sm font-bold text-text-main">Global exclude list</h2>
      <p class="text-[11px] text-text-subtle">Overrides every preset and binding.</p>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      {#if globalStatus}<span class="text-xs text-text-muted">{globalStatus}</span>{/if}
      <Button variant="primary" size="sm" onclick={saveGlobalExcludes} disabled={globalSaving} icon="save">
        {globalSaving ? 'Saving…' : 'Save exclude list'}
      </Button>
    </div>
  </div>
  <p class="pt-2 text-xs text-text-subtle">
    A model id matching one of these rules is never injected by a keyword rule, whatever any
    preset says. Use it for the handful of models that must stay untouched.
  </p>
  <div class="pt-3">
    <KeywordRules label="Excluded models" rules={excludes} onchange={(next) => (excludes = next)} />
  </div>
</Card>
