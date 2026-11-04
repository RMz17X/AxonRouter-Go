<script lang="ts">
  // AxonRouter's "Request Stream" card: one column of ten blocks per minute
  // over the last 30 minutes, filled from the bottom in proportion to the
  // busiest minute. Buckets come from stats.last10Minutes (per-minute counts
  // merged from the stats read and the SSE stream); minutes the payload does
  // not cover render as empty buckets so the window is always 30 wide.
  import { onMount } from 'svelte'

  interface MinuteBucket {
    timestamp: string
    requests: number
  }

  interface Props {
    last10Minutes?: MinuteBucket[]
  }

  let { last10Minutes = [] }: Props = $props()

  // The window ends at the current minute, so it has to slide even when no
  // new stats arrive; a 30 s tick is enough for a per-minute display.
  let nowMs = $state(Date.now())
  onMount(() => {
    const t = setInterval(() => (nowMs = Date.now()), 30_000)
    return () => clearInterval(t)
  })

  function hhmm(d: Date): string {
    return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }

  interface Column {
    key: number
    label: string
    requests: number
    filled: number
  }

  let columns = $derived.by((): Column[] => {
    const byMinute = new Map<number, number>()
    for (const b of last10Minutes || []) {
      const t = new Date(b.timestamp).getTime()
      if (!Number.isFinite(t)) continue
      const minute = Math.floor(t / 60_000)
      byMinute.set(minute, (byMinute.get(minute) ?? 0) + (b.requests || 0))
    }
    const endMinute = Math.floor(nowMs / 60_000)
    const raw: { key: number; label: string; requests: number }[] = []
    for (let i = 29; i >= 0; i--) {
      const minute = endMinute - i
      raw.push({
        key: minute,
        label: hhmm(new Date(minute * 60_000)),
        requests: byMinute.get(minute) ?? 0,
      })
    }
    const max = raw.reduce((m, c) => Math.max(m, c.requests), 0)
    return raw.map((c) => ({
      ...c,
      filled: max > 0 ? Math.round((c.requests / max) * 10) : 0,
    }))
  })

  let peak = $derived(columns.reduce((m, c) => Math.max(m, c.requests), 0))
  let total = $derived(columns.reduce((s, c) => s + c.requests, 0))
</script>

<div class="bg-surface border border-border-subtle rounded-brand-lg shadow-[var(--shadow-soft)] flex min-w-0 flex-col gap-3 p-4">
  <div class="min-w-0">
    <h3 class="text-sm font-semibold text-text-main">Request Stream</h3>
    <p class="text-xs text-text-muted">Requests per minute across the last 30 minutes</p>
  </div>

  <div
    class="flex min-w-0 items-end gap-[3px]"
    role="img"
    aria-label="Request stream for the last 30 minutes. {total} requests total, peak {peak} requests per minute."
  >
    {#each columns as col (col.key)}
      <div class="flex min-w-0 flex-1 flex-col-reverse gap-[2px]">
        {#each { length: 10 } as _, i}
          <span
            class="h-2.5 w-full rounded-[2px] {i < col.filled ? 'bg-primary' : 'bg-surface-2'}"
            title="{col.label} · {col.requests} requests"
          ></span>
        {/each}
      </div>
    {/each}
  </div>

  <p class="font-mono text-[11px] text-text-muted">Peak {peak} requests/min</p>
</div>
