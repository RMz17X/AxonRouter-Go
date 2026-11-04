<script lang="ts">
  // AxonRouter's Provider Topology, reimplemented in Svelte/SVG (no React
  // Flow): a provider is drawn while it has active requests and for five
  // minutes after its last activity; the router sits at the center of an
  // ellipse and each visible provider gets one Bezier edge whose rendering
  // encodes its state — electric beam when active, amber when last used,
  // danger on error, faint border color when idle.
  import { onMount, untrack } from 'svelte'
  import { getIconPath, getProviderGlyph } from '../connections/types'
  import { providerDisplayName, type ActiveRequestItem, type RecentRequestItem } from './types'

  interface ProviderMetaItem {
    id: string
    name?: string
    color?: string
    alias?: string
    type?: string
  }

  interface Props {
    providerNodes?: ProviderMetaItem[]
    activeRequests?: ActiveRequestItem[]
    recentRequests?: RecentRequestItem[]
    errorProvider?: string
  }

  let {
    providerNodes = [],
    activeRequests = [],
    recentRequests = [],
    errorProvider = '',
  }: Props = $props()

  const RETENTION_MS = 5 * 60_000

  // Retention stamps: provider id (as reported) -> last activity in ms. A
  // provider is stamped whenever it is active, last used, in error, or shows
  // up in the recent-requests feed; the 3 s tick below prunes stamps older
  // than the retention window, which is what takes nodes off the map.
  let retention = $state(new Map<string, number>())
  let nowMs = $state(Date.now())
  let reducedMotion = $state(false)
  let imageErrors = $state<Record<string, boolean>>({})

  $effect(() => {
    // Props are the tracked inputs; the retention map is written untracked so
    // stamping cannot retrigger this effect through its own write.
    const actives = activeRequests
    const recents = recentRequests
    const err = errorProvider
    untrack(() => {
      const now = Date.now()
      let changed = false
      const stamp = (id?: string | null) => {
        if (id && id.trim()) {
          retention.set(id, now)
          changed = true
        }
      }
      for (const r of actives) stamp(r.provider)
      for (const r of recents) stamp(r.provider)
      stamp(err)
      if (changed) retention = new Map(retention)
    })
  })

  onMount(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    reducedMotion = mq.matches
    const onMq = (e: MediaQueryListEvent) => (reducedMotion = e.matches)
    mq.addEventListener('change', onMq)
    const t = setInterval(() => {
      nowMs = Date.now()
      let changed = false
      for (const [id, ts] of retention) {
        if (nowMs - ts > RETENTION_MS) {
          retention.delete(id)
          changed = true
        }
      }
      if (changed) retention = new Map(retention)
    }, 3000)
    return () => {
      clearInterval(t)
      mq.removeEventListener('change', onMq)
    }
  })

  function metaFor(id: string): ProviderMetaItem | undefined {
    const t = id.toLowerCase().trim()
    return providerNodes.find(
      (p) =>
        p.id?.toLowerCase().trim() === t ||
        p.alias?.toLowerCase().trim() === t ||
        p.name?.toLowerCase().trim() === t
    )
  }

  function matches(id: string, target?: string | null): boolean {
    if (!target) return false
    const t = target.toLowerCase().trim()
    if (!t) return false
    const meta = metaFor(id)
    return (
      t === id.toLowerCase().trim() ||
      t === (meta?.alias || '').toLowerCase().trim() ||
      t === (meta?.name || '').toLowerCase().trim()
    )
  }

  let activeCount = $derived(activeRequests.reduce((sum, r) => sum + (r.count || 1), 0))
  let lastProvider = $derived(recentRequests[0]?.provider || '')

  type NodeState = 'active' | 'error' | 'last' | 'idle'
  interface TopoNode {
    key: string
    id: string
    name: string
    color: string
    initials: string
    iconSrc: string
    x: number
    y: number
    state: NodeState
    path: string
  }

  let nodes = $derived.by((): TopoNode[] => {
    const cutoff = nowMs - RETENTION_MS
    const ids: string[] = []
    for (const [id, ts] of retention) {
      if (ts >= cutoff) ids.push(id)
    }
    const count = ids.length
    const rx = Math.max(320, (204 * count) / (2 * Math.PI))
    const ry = Math.max(200, rx * 0.55)

    return ids.map((id, idx) => {
      const angle = -Math.PI / 2 + (count > 0 ? (2 * Math.PI * idx) / count : 0)
      const x = rx * Math.cos(angle)
      const y = ry * Math.sin(angle)
      const meta = metaFor(id)
      const glyph = getProviderGlyph(meta?.id || id)
      const name = providerDisplayName(meta?.id || id, providerNodes)
      const color = meta?.color || glyph.color || '#6b7280'

      const isActive = activeRequests.some((r) => matches(id, r.provider))
      const isError = !isActive && matches(id, errorProvider)
      const isLast = !isActive && !isError && matches(id, lastProvider)
      const state: NodeState = isActive ? 'active' : isError ? 'error' : isLast ? 'last' : 'idle'

      // Edge handles leave the router through the side facing the node and
      // land on the node's facing edge, like the previous map's geometry.
      let sourceX = 0
      let sourceY = 0
      let targetX = x
      let targetY = y
      let vertical = false
      if (Math.abs(angle + Math.PI / 2) < Math.PI / 4 || Math.abs(angle - (3 * Math.PI) / 2) < Math.PI / 4) {
        sourceY = -22
        targetY = y + 17
        vertical = true
      } else if (Math.abs(angle - Math.PI / 2) < Math.PI / 4) {
        sourceY = 22
        targetY = y - 17
        vertical = true
      } else if (angle > -Math.PI / 2 && angle < Math.PI / 2) {
        sourceX = 75
        targetX = x - 92
      } else {
        sourceX = -75
        targetX = x + 92
      }
      const path = vertical
        ? `M ${sourceX} ${sourceY} C ${sourceX} ${(sourceY + targetY) / 2}, ${targetX} ${(sourceY + targetY) / 2}, ${targetX} ${targetY}`
        : `M ${sourceX} ${sourceY} C ${(sourceX + targetX) / 2} ${sourceY}, ${(sourceX + targetX) / 2} ${targetY}, ${targetX} ${targetY}`

      return {
        key: id,
        id: meta?.id || id,
        name,
        color,
        initials: glyph.initials,
        iconSrc: getIconPath(meta?.id || id, meta?.type),
        x,
        y,
        state,
        path,
      }
    })
  })

  let geometry = $derived.by(() => {
    const count = nodes.length
    const rx = Math.max(320, (204 * count) / (2 * Math.PI))
    const ry = Math.max(200, rx * 0.55)
    // Fit box: the ellipse plus room for the node boxes, with Axon's 0.2
    // fit-view padding around it.
    const fitW = (rx + 110) * 2 * 1.2
    const fitH = (ry + 50) * 2 * 1.2
    return { rx, ry, fitW, fitH }
  })

  // Pan/zoom state: the current viewBox. Null means "fit", which is also the
  // reset target — changing the node count refits, per Axon's behavior.
  let vb = $state<{ x: number; y: number; w: number; h: number } | null>(null)
  let view = $derived(
    vb ?? { x: -geometry.fitW / 2, y: -geometry.fitH / 2, w: geometry.fitW, h: geometry.fitH }
  )
  let viewBox = $derived(`${view.x} ${view.y} ${view.w} ${view.h}`)

  let prevCount = -1
  $effect(() => {
    const count = nodes.length
    if (count !== prevCount) {
      prevCount = count
      vb = null
    }
  })

  let svgEl = $state<SVGSVGElement | null>(null)
  let dragging = $state(false)
  let dragStart = { px: 0, py: 0, x: 0, y: 0, w: 0, h: 0 }

  function zoomBy(factor: number, anchorU = 0.5, anchorV = 0.5) {
    const cur = view
    const w = Math.min(geometry.fitW * 10, Math.max(geometry.fitW / 2, cur.w * factor))
    const h = (w / cur.w) * cur.h
    const ax = cur.x + anchorU * cur.w
    const ay = cur.y + anchorV * cur.h
    vb = { x: ax - anchorU * w, y: ay - anchorV * h, w, h }
  }

  function handleWheel(e: WheelEvent) {
    e.preventDefault()
    if (!svgEl) return
    const rect = svgEl.getBoundingClientRect()
    const u = rect.width > 0 ? (e.clientX - rect.left) / rect.width : 0.5
    const v = rect.height > 0 ? (e.clientY - rect.top) / rect.height : 0.5
    zoomBy(e.deltaY < 0 ? 0.9 : 1.1, u, v)
  }

  function handlePointerDown(e: PointerEvent) {
    if ((e.target as HTMLElement).closest('button')) return
    dragging = true
    dragStart = { px: e.clientX, py: e.clientY, x: view.x, y: view.y, w: view.w, h: view.h }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }

  function handlePointerMove(e: PointerEvent) {
    if (!dragging || !svgEl) return
    const rect = svgEl.getBoundingClientRect()
    if (rect.width === 0 || rect.height === 0) return
    const dx = ((e.clientX - dragStart.px) / rect.width) * dragStart.w
    const dy = ((e.clientY - dragStart.py) / rect.height) * dragStart.h
    vb = { ...view, x: dragStart.x - dx, y: dragStart.y - dy }
  }

  function handlePointerUp(e: PointerEvent) {
    if (!dragging) return
    dragging = false
    try {
      ;(e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId)
    } catch {}
  }

  let ariaSummary = $derived.by(() => {
    const names = (state: NodeState) => nodes.filter((n) => n.state === state).map((n) => n.name)
    const join = (list: string[]) => (list.length ? list.join(', ') : 'none')
    return `Provider topology graph. ${activeCount} active requests. active: ${join(names('active'))} error: ${join(names('error'))} last used: ${join(names('last'))} idle: ${join(names('idle'))}`
  })

  const ORB_COLORS = ['#fde047', '#67e8f9', '#ffffff']
</script>

<div class="flex min-w-0 flex-col gap-2">
  <div
    role="img"
    aria-label={ariaSummary}
    class="relative h-[320px] w-full min-w-0 select-none overflow-hidden rounded-sm border border-border bg-surface-2 sm:h-[480px] {dragging ? 'cursor-grabbing' : 'cursor-grab'}"
    style="touch-action: none"
    onpointerdown={handlePointerDown}
    onpointermove={handlePointerMove}
    onpointerup={handlePointerUp}
    onpointercancel={handlePointerUp}
    onwheel={handleWheel}
  >
    <svg bind:this={svgEl} class="h-full w-full" {viewBox} aria-hidden="true">
      <defs>
        <filter id="axon-topo-electric" x="-20%" y="-20%" width="140%" height="140%">
          <feTurbulence type="fractalNoise" baseFrequency="0.8" numOctaves="2" result="noise">
            {#if !reducedMotion}
              <animate attributeName="baseFrequency" values="0.8;1.4;0.8" dur="0.25s" repeatCount="indefinite" />
            {/if}
          </feTurbulence>
          <feDisplacementMap in="SourceGraphic" in2="noise" scale="3.5" />
        </filter>
        <linearGradient id="axon-topo-router-grad" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0%" stop-color="var(--color-primary)" stop-opacity="0.3" />
          <stop offset="50%" stop-color="#facc15" stop-opacity="0.2" />
          <stop offset="100%" stop-color="#22d3ee" stop-opacity="0.25" />
        </linearGradient>
      </defs>

      <!-- Edges: router -> provider, one per visible provider -->
      {#each nodes as node (node.key)}
        {#if node.state === 'active'}
          <g>
            <path d={node.path} fill="none" stroke="#22d3ee" stroke-width="10" stroke-opacity="0.35" stroke-linecap="round" filter="url(#axon-topo-electric)" class={reducedMotion ? '' : 'topology-edge-halo'} />
            <path d={node.path} fill="none" stroke="#4ade80" stroke-width="5" stroke-opacity="0.85" stroke-linecap="round" class={reducedMotion ? '' : 'topology-edge-plasma'} />
            <path d={node.path} fill="none" stroke="#f8fafc" stroke-width="2.2" class={reducedMotion ? '' : 'topology-edge-kame'} />
            {#if !reducedMotion}
              {#each { length: 6 } as _, i}
                <circle r={i % 2 === 0 ? 4 : 2.5} fill={ORB_COLORS[i % ORB_COLORS.length]}>
                  <animateMotion dur={`${0.4 + i * 0.08}s`} begin={`${-i * 0.07}s`} repeatCount="indefinite" path={node.path} />
                </circle>
              {/each}
              {#each { length: 5 } as _, i}
                <circle r="1.6" fill={i % 2 === 0 ? '#67e8f9' : '#fde047'} opacity="0.9">
                  <animateMotion dur={`${0.3 + i * 0.05}s`} begin={`${-i * 0.06}s`} repeatCount="indefinite" path={node.path} />
                </circle>
              {/each}
            {/if}
          </g>
        {:else if node.state === 'error'}
          <path d={node.path} fill="none" stroke="var(--color-danger)" stroke-width="2.5" opacity="0.9" />
        {:else if node.state === 'last'}
          <path d={node.path} fill="none" stroke="#f59e0b" stroke-width="2" opacity="0.7" />
        {:else}
          <path d={node.path} fill="none" stroke="var(--color-border)" stroke-width="1" opacity="0.3" />
        {/if}
      {/each}

      <!-- Provider nodes -->
      {#each nodes as node (node.key)}
        <g transform="translate({node.x}, {node.y})">
          <title>{node.name}</title>
          {#if node.state === 'active'}
            <rect x="-91" y="-16" width="182" height="32" rx="5" fill="none" stroke={node.color} stroke-width="6" opacity="0.22" />
          {/if}
          <rect
            x="-90"
            y="-15"
            width="180"
            height="30"
            rx="4"
            fill="var(--color-surface)"
            stroke={node.state === 'active' ? node.color : node.state === 'last' ? '#f59e0b' : node.state === 'error' ? 'var(--color-danger)' : 'var(--color-border)'}
            stroke-width={node.state === 'active' ? 2 : 1.5}
          />
          {#if !imageErrors[node.key]}
            <image href={node.iconSrc} x="-84" y="-12" width="24" height="24" onerror={() => (imageErrors = { ...imageErrors, [node.key]: true })} />
          {:else}
            <rect x="-84" y="-12" width="24" height="24" rx="4" fill="{node.color}15" />
            <text x="-72" y="1" text-anchor="middle" dominant-baseline="middle" font-size="10" font-weight="700" fill={node.color}>{node.initials}</text>
          {/if}
          <text
            x="-52"
            y="1"
            dominant-baseline="middle"
            font-size="12"
            font-weight="500"
            fill={node.state === 'active' ? node.color : 'var(--color-text-main)'}
          >
            {node.name}
          </text>
          {#if node.state === 'active'}
            {#if !reducedMotion}
              <circle cx="80" cy="0" r="3.5" fill={node.color}>
                <animate attributeName="r" values="3.5;8" dur="1.2s" repeatCount="indefinite" />
                <animate attributeName="opacity" values="0.7;0" dur="1.2s" repeatCount="indefinite" />
              </circle>
            {/if}
            <circle cx="80" cy="0" r="3.5" fill={node.color} />
          {/if}
        </g>
      {/each}

      <!-- Router node (center) -->
      <g class={activeCount > 0 && !reducedMotion ? 'topo-router-pulse' : ''}>
        <rect
          x="-75"
          y="-22"
          width="150"
          height="44"
          rx="4"
          fill={activeCount > 0 ? 'url(#axon-topo-router-grad)' : 'var(--color-surface)'}
          stroke={activeCount > 0 ? '#fde047' : 'var(--color-primary)'}
          stroke-width="2"
        />
        <image href="/favicon.svg" x="-63" y="-12" width="24" height="24" />
        <text
          x="-30"
          y="1"
          dominant-baseline="middle"
          font-size="14"
          font-weight="700"
          fill={activeCount > 0 ? '#fde047' : 'var(--color-primary)'}
          class={activeCount > 0 && !reducedMotion ? 'topology-router-label' : ''}
        >
          9router-go
        </text>
        {#if activeCount > 0}
          <g transform="translate(75, -22)">
            <circle r="9" fill="#facc15" />
            <text y="1" text-anchor="middle" dominant-baseline="middle" font-size="10" font-weight="700" fill="#000">{activeCount}</text>
          </g>
        {/if}
      </g>
    </svg>

    {#if nodes.length === 0}
      <div class="pointer-events-none absolute bottom-4 left-1/2 z-10 -translate-x-1/2 rounded-full border border-border bg-surface px-3 py-1 text-[11px] whitespace-nowrap text-text-muted">
        Idle • All providers standby (awaiting requests)
      </div>
    {/if}

    <!-- Zoom controls, bottom-right (Axon's React Flow Controls position) -->
    <div class="absolute right-4 bottom-4 z-20 flex flex-col overflow-hidden rounded-md border border-border bg-surface shadow-md">
      <button
        type="button"
        onclick={() => zoomBy(0.8)}
        class="flex cursor-pointer items-center justify-center border-b border-border p-1.5 text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main"
        title="Zoom In"
        aria-label="Zoom In"
      >
        <span class="material-symbols-outlined text-[16px]">add</span>
      </button>
      <button
        type="button"
        onclick={() => zoomBy(1.25)}
        class="flex cursor-pointer items-center justify-center border-b border-border p-1.5 text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main"
        title="Zoom Out"
        aria-label="Zoom Out"
      >
        <span class="material-symbols-outlined text-[16px]">remove</span>
      </button>
      <button
        type="button"
        onclick={() => (vb = null)}
        class="flex cursor-pointer items-center justify-center p-1.5 text-text-muted transition-colors hover:bg-surface-2 hover:text-text-main"
        title="Fit View"
        aria-label="Fit View"
      >
        <span class="material-symbols-outlined text-[16px]">crop_free</span>
      </button>
    </div>

    <!-- Screen-reader status table: the SVG is decorative to AT, this is not. -->
    <table class="sr-only">
      <caption>Provider topology status</caption>
      <thead>
        <tr><th>Provider</th><th>State</th></tr>
      </thead>
      <tbody>
        {#each nodes as node (node.key)}
          <tr><td>{node.name}</td><td>{node.state}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <!-- Legend -->
  <div class="flex flex-wrap items-center gap-4 px-1 text-[11px] text-text-muted">
    <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-full bg-info"></span>Active</span>
    <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-full bg-warning"></span>Last used</span>
    <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-full bg-danger"></span>Error</span>
    <span class="inline-flex items-center gap-1.5"><span class="h-2 w-2 rounded-full bg-border"></span>Idle</span>
  </div>
</div>

<style>
  /* Pulse for the router group while requests are in flight. transform-box
     keeps the scale centered on the node instead of the SVG origin. The
     edge/router text animations themselves live in index.css (topology-*). */
  .topo-router-pulse {
    transform-box: fill-box;
    transform-origin: center;
    animation: topo-router-pulse 0.75s ease-in-out infinite;
  }
  @keyframes topo-router-pulse {
    0%,
    100% {
      transform: scale(1);
    }
    50% {
      transform: scale(1.06);
    }
  }
</style>
