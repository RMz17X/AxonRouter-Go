<script lang="ts">
  import { onMount } from 'svelte'
  import type { ActiveRequestItem } from './types'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'
  import { getAnimationLevel } from '../../lib/displayPrefs'
  import {
    createTopologySlotAllocator,
    topologyRingRadii,
    topologySlotAngle
  } from './topologyLayout'

  interface ProviderNodeItem {
    id: string
    name: string
    color?: string
    type?: string
    alias?: string
  }

  interface Props {
    providers?: ProviderNodeItem[]
    activeRequests?: ActiveRequestItem[]
    pulseProvider?: string
    lastProvider?: string
    errorProvider?: string
    onRefresh?: () => void
  }

  let {
    providers = [],
    activeRequests = [],
    pulseProvider = '',
    lastProvider = '',
    errorProvider = ''
  }: Props = $props()

  // Upstream parity (#4615): an empty list renders an empty state, it does not
  // fall back to a hardcoded free-provider roster. Those nodes have no
  // connection behind them, and upstream #4615 exists precisely because
  // drawing them unconditionally made an idle map look busy — so a catalog
  // fallback here would re-introduce the very bug that PR removed, on the one
  // screen where nothing is actually in use.
  let displayProviders = $derived(providers)

  let activeProviderIds = $derived.by(() => {
    const set = new Set<string>()
    for (const r of activeRequests) {
      const p = r.provider?.toLowerCase().trim()
      if (p) set.add(p)
    }
    return set
  })

  let hasPulse = $derived(Boolean(pulseProvider))
  let activeCount = $derived(
    Math.max(activeRequests.reduce((sum, r) => sum + (r.count || 1), 0), hasPulse ? 1 : 0)
  )
  // Container sizing and fitView state
  let containerEl = $state<HTMLDivElement | null>(null)
  let containerWidth = $state(800)
  let containerHeight = $state(480)
  let animLevel = $state<'default' | 'minimal' | 'off'>('default')

  // Zoom & Pan transforms
  let zoom = $state(0.85)
  let panX = $state(0)
  let panY = $state(0)
  let isDragging = $state(false)
  let dragStart = { x: 0, y: 0 }

  // Track image load errors to fallback to stylish initials
  let imageErrors = $state<Record<string, boolean>>({})

  // Ring placement is slot-based, not list-based: each provider owns a
  // numbered slot for the session (see topologyLayout.ts), so a provider
  // arriving, leaving, or being reordered in the activity-ordered prop
  // list moves nobody else, and the ring radii follow the slot capacity
  // instead of the live count. The allocator lives as long as the card
  // stays mounted; an unmount forgets every assignment. sync() is
  // idempotent for an unchanged provider set, which is what makes it
  // safe to reconcile inside this derivation.
  const topologySlots = createTopologySlotAllocator()

  let geometry = $derived.by(() => {
    const keys = displayProviders.map((p) => p.id.toLowerCase().trim())
    topologySlots.sync(keys)
    const capacity = topologySlots.capacity
    const { rx, ry } = topologyRingRadii(capacity)

    const nodes = displayProviders.map((p, idx) => {
      const pid = keys[idx]
      // slotOf is total after sync; the index fallback is unreachable in
      // practice and only guards a future allocator change.
      const slot = topologySlots.slotOf(pid) ?? idx
      const angle = topologySlotAngle(slot, capacity)
      const x = rx * Math.cos(angle)
      const y = ry * Math.sin(angle)

      const pname = (p.name || '').toLowerCase().trim()
      const palias = (p.alias || '').toLowerCase().trim()
      const isProviderMatch = (target: string) => {
        if (!target) return false
        const t = target.toLowerCase().trim()
        return t === pid || t === pname || (Boolean(palias) && t === palias)
      }

      const isActive =
        activeProviderIds.has(pid) ||
        activeProviderIds.has(pname) ||
        (Boolean(palias) && activeProviderIds.has(palias)) ||
        (hasPulse && isProviderMatch(pulseProvider))
      const isLast = !isActive && Boolean(lastProvider) && isProviderMatch(lastProvider)
      const isError = !isActive && Boolean(errorProvider) && isProviderMatch(errorProvider)
      // Edge handle coordinates based on angle
      let sourceX = 0
      let sourceY = 0
      let targetX = x
      let targetY = y
      let isVertical = false

      if (
        Math.abs(angle + Math.PI / 2) < Math.PI / 4 ||
        Math.abs(angle - (3 * Math.PI) / 2) < Math.PI / 4
      ) {
        // Top quadrant
        sourceX = 0
        sourceY = -22
        targetX = x
        targetY = y + 26
        isVertical = true
      } else if (Math.abs(angle - Math.PI / 2) < Math.PI / 4) {
        // Bottom quadrant
        sourceX = 0
        sourceY = 22
        targetX = x
        targetY = y - 26
        isVertical = true
      } else if (angle > -Math.PI / 2 && angle < Math.PI / 2) {
        // Right quadrant
        sourceX = 65
        sourceY = 0
        targetX = x - 75
        targetY = y
        isVertical = false
      } else {
        // Left quadrant
        sourceX = -65
        sourceY = 0
        targetX = x + 75
        targetY = y
        isVertical = false
      }

      const path = isVertical
        ? `M ${sourceX} ${sourceY} C ${sourceX} ${(sourceY + targetY) / 2}, ${targetX} ${(sourceY + targetY) / 2}, ${targetX} ${targetY}`
        : `M ${sourceX} ${sourceY} C ${(sourceX + targetX) / 2} ${sourceY}, ${(sourceX + targetX) / 2} ${targetY}, ${targetX} ${targetY}`

      const returnPath = isVertical
        ? `M ${targetX} ${targetY} C ${targetX} ${(sourceY + targetY) / 2}, ${sourceX} ${(sourceY + targetY) / 2}, ${sourceX} ${sourceY}`
        : `M ${targetX} ${targetY} C ${(sourceX + targetX) / 2} ${targetY}, ${(sourceX + targetX) / 2} ${sourceY}, ${sourceX} ${sourceY}`

      const textIcon = (p.name || p.id || '?').slice(0, 2).toUpperCase()

      return {
        ...p,
        x,
        y,
        isActive,
        isLast,
        isError,
        path,
        returnPath,
        textIcon,
        color: p.color || '#6b7280'
      }
    })

    return { rx, ry, nodes }
  })

  function fitView() {
    if (!containerWidth || !containerHeight) return
    const requiredWidth = 2 * geometry.rx + 220
    const requiredHeight = 2 * geometry.ry + 120
    const scaleX = (containerWidth - 32) / requiredWidth
    const scaleY = (containerHeight - 32) / requiredHeight
    zoom = Math.min(1.05, Math.max(0.15, Math.min(scaleX, scaleY)))
    panX = 0
    panY = 0
  }

  // Refit only when the ring itself changes size (a capacity tier step).
  // Providers entering or leaving inside a tier leave rx/ry untouched,
  // and refitting then would reset the operator's pan for no reason —
  // the whole point of the slot layout is that nothing on screen moves.
  let prevRingKey = ''
  $effect(() => {
    const ringKey = `${geometry.rx}x${geometry.ry}`
    if (ringKey !== prevRingKey) {
      prevRingKey = ringKey
      fitView()
    }
  })
  function handlePointerDown(e: PointerEvent) {
    if ((e.target as HTMLElement).closest('button')) return
    isDragging = true
    dragStart = { x: e.clientX - panX, y: e.clientY - panY }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }

  function handlePointerMove(e: PointerEvent) {
    if (!isDragging) return
    panX = e.clientX - dragStart.x
    panY = e.clientY - dragStart.y
  }

  function handlePointerUp(e: PointerEvent) {
    if (isDragging) {
      isDragging = false
      try {
        ;(e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId)
      } catch {}
    }
  }

  function handleWheel(e: WheelEvent) {
    e.preventDefault()
    const zoomFactor = e.deltaY < 0 ? 1.08 : 0.92
    zoom = Math.min(2.2, Math.max(0.25, zoom * zoomFactor))
  }

  onMount(() => {
    animLevel = getAnimationLevel()
    if (!containerEl) return
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        containerWidth = entry.contentRect.width || 800
        containerHeight = entry.contentRect.height || 480
        fitView()
      }
    })
    observer.observe(containerEl)
    fitView()
    return () => observer.disconnect()
  })
</script>

<div
  bind:this={containerEl}
  role="region"
  aria-label="Provider topology map"
  class="h-[320px] w-full min-w-0 rounded-lg border border-border bg-bg-subtle/30 sm:h-[480px] relative overflow-hidden select-none cursor-grab active:cursor-grabbing"
  onpointerdown={handlePointerDown}
  onpointermove={handlePointerMove}
  onpointerup={handlePointerUp}
  onpointercancel={handlePointerUp}
  onwheel={handleWheel}
>
  <!-- Interactive Viewport Layer (centered at 0, 0) -->
  <div
    class="absolute inset-0 pointer-events-none"
    style="transform: translate({containerWidth / 2 + panX}px, {containerHeight / 2 + panY}px) scale({zoom}); transform-origin: 0 0;"
  >
    <!-- SVG Layer for Bezier Edges -->
    <svg class="absolute inset-0 overflow-visible pointer-events-none" style="transform: translate(0, 0);">

      <!-- Edges connecting router to provider nodes -->
      {#each geometry.nodes as node (node.id)}
        {#if node.isActive}
          <!-- Expanding Shockwave Ripples from Provider Target -->
          {#if animLevel === 'default'}
            <g class="topology-shockwave-group">
              <circle
                cx={node.x}
                cy={node.y}
                r="35"
                fill="none"
                stroke={node.color || '#22d3ee'}
                opacity="0"
              >
                <animate attributeName="r" values="32;75" dur="2.4s" repeatCount="indefinite" />
                <animate attributeName="opacity" values="0.7;0" dur="2.4s" repeatCount="indefinite" />
                <animate attributeName="stroke-width" values="2.5;0.5" dur="2.4s" repeatCount="indefinite" />
              </circle>
            </g>
          {/if}

          <!-- Bidirectional Neural Stream (Router <-> Provider) -->
          <g class="topology-edge-electric">
            <!-- 1. Forward plasma stream -->
            <path
              d={node.path}
              fill="none"
              stroke="#06b6d4"
              stroke-width="3.5"
              stroke-opacity="0.8"
              stroke-linecap="round"
              class="topology-edge-plasma"
            />
            <!-- 2. Core laser beam -->
            <path
              d={node.path}
              fill="none"
              stroke="#f8fafc"
              stroke-width="1.5"
              class="topology-edge-kame"
            />

            <!-- 3. Return stream (Provider -> Router) -->
            <path
              d={node.returnPath}
              fill="none"
              stroke="#10b981"
              stroke-width="2.5"
              stroke-opacity="0.85"
              class="topology-edge-return"
            />

            {#if animLevel !== 'off'}
              <!-- Forward and return tokens with lightweight smooth motion -->
              <circle r="4" fill="#38bdf8" opacity="0.95">
                <animateMotion dur="1.6s" repeatCount="indefinite" path={node.path} />
              </circle>
              <circle r="3.5" fill="#34d399" opacity="0.95">
                <animateMotion dur="1.8s" repeatCount="indefinite" path={node.returnPath} />
              </circle>
            {/if}
          </g>
        {:else if node.isLast}
          <!-- Last Provider Active Edge (Amber) -->
          <path
            d={node.path}
            fill="none"
            stroke="#f59e0b"
            stroke-width="2"
            opacity="0.7"
          />
        {:else if node.isError}
          <!-- Error Edge (Red) -->
          <path
            d={node.path}
            fill="none"
            stroke="#ef4444"
            stroke-width="2.5"
            opacity="0.9"
          />
        {:else}
          <!-- Inactive Subtle Edge -->
          <path
            d={node.path}
            fill="none"
            stroke="var(--color-border)"
            stroke-width="1"
            opacity="0.3"
          />
        {/if}
      {/each}
      <!-- Center Router Absorption Rings when activeCount > 0 -->
      {#if activeCount > 0 && animLevel !== 'off'}
        <g class="topology-router-absorption">
          <circle
            cx="0"
            cy="0"
            r="30"
            fill="none"
            stroke="#22d3ee"
            stroke-dasharray="5 5"
            opacity="0"
          >
            <animate attributeName="r" values="50;22" dur="2.2s" repeatCount="indefinite" />
            <animate attributeName="opacity" values="0;0.6;0" dur="2.2s" repeatCount="indefinite" />
            <animate attributeName="stroke-width" values="1;2;0.5" dur="2.2s" repeatCount="indefinite" />
          </circle>
          {#if animLevel === 'default'}
            <circle
              cx="0"
              cy="0"
              r="30"
              fill="none"
              stroke="#facc15"
              stroke-dasharray="4 6"
              opacity="0"
            >
              <animate attributeName="r" values="58;26" dur="2.6s" begin="0.8s" repeatCount="indefinite" />
              <animate attributeName="opacity" values="0;0.5;0" dur="2.6s" begin="0.8s" repeatCount="indefinite" />
              <animate attributeName="stroke-width" values="0.8;1.5;0.5" dur="2.6s" begin="0.8s" repeatCount="indefinite" />
            </circle>
          {/if}
        </g>
      {/if}
    </svg>

    <!-- HTML Router Node (Center 0, 0) -->
    <div
      class="absolute z-10 flex items-center justify-center px-5 py-3 rounded-xl border-2 min-w-[130px] {activeCount > 0 ? 'topology-router-core border-yellow-300 bg-gradient-to-br from-primary/30 via-yellow-400/20 to-cyan-400/25' : 'border-primary bg-primary/5 shadow-md'} pointer-events-auto"
      style="left: 0px; top: 0px; transform: translate(-50%, -50%);"
    >
      <img
        src="/favicon.svg"
        alt="AxonRouter-Go"
        class="w-6 h-6 mr-2 object-contain {activeCount > 0 ? 'topology-router-icon' : ''}"
        loading="lazy"
        decoding="async"
      />
      <span class="text-sm font-bold {activeCount > 0 ? 'topology-router-label text-yellow-300' : 'text-primary'}">
        AxonRouter-Go
      </span>
      {#if activeCount > 0}
        <span class="ml-2 px-1.5 py-0.5 rounded-full bg-yellow-400 text-black text-xs font-bold topology-router-badge">
          {activeCount}
        </span>
      {/if}
    </div>
    <!-- HTML Provider Nodes -->
    {#each geometry.nodes as node (node.id)}
      <div
        class="absolute flex items-center gap-2.5 px-4 py-2.5 rounded-lg border-2 transition-all duration-300 bg-bg shadow-sm pointer-events-auto {node.isActive ? 'topology-node-active-bounce' : ''}"
        style="left: {node.x}px; top: {node.y}px; transform: translate(-50%, -50%); border-color: {node.isActive ? node.color : node.isLast ? '#f59e0b' : 'var(--color-border)'}; box-shadow: {node.isActive ? `0 0 0 2px ${node.color}33` : node.isLast ? '0 0 8px rgba(245, 158, 11, 0.25)' : 'none'}; min-width: 150px;"
      >
        <div
          class="w-8 h-8 rounded-md flex items-center justify-center shrink-0"
          style="background-color: {node.color}15;"
        >
          {#if !imageErrors[node.id]}
            <ProviderArtwork id={node.id} class="w-6 h-6 rounded-sm object-contain" />
          {:else}
            <span class="text-sm font-bold" style="color: {node.color};">
              {node.textIcon}
            </span>
          {/if}
        </div>
        <span
          class="text-base font-medium truncate max-w-[200px]"
          style="color: {node.isActive ? node.color : 'var(--color-text)'}"
          title={node.name}
        >
          {node.name}
        </span>

        <!-- Active indicator -->
        {#if node.isActive}
          <div class="flex items-center gap-1.5 ml-auto shrink-0">
            <span class="text-[10px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded bg-emerald-500/15 text-emerald-400 border border-emerald-500/30">
              Live
            </span>
            <span class="relative flex h-2 w-2">
              <span
                class="animate-ping absolute inline-flex h-full w-full rounded-full opacity-75"
                style="background-color: {node.color};"
              ></span>
              <span
                class="relative inline-flex rounded-full h-2 w-2"
                style="background-color: {node.color};"
              ></span>
            </span>
          </div>
        {/if}
      </div>
    {/each}
  </div>
  <!-- Upstream empty state: nothing has been used in the period, so there is
       nothing honest to draw. The router core and the zoom controls stay put so
       the card keeps its shape instead of collapsing. -->


  <!-- Bottom-left React Flow style controls -->
  <div class="absolute bottom-4 left-4 z-20 flex flex-col rounded-md border border-border bg-surface/90 shadow-md overflow-hidden backdrop-blur">
    <button
      type="button"
      onclick={() => (zoom = Math.min(2.5, zoom * 1.2))}
      class="p-1.5 text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors border-b border-border cursor-pointer flex items-center justify-center"
      title="Zoom In"
      aria-label="Zoom In"
    >
      <span class="material-symbols-outlined text-[16px]">add</span>
    </button>
    <button
      type="button"
      onclick={() => (zoom = Math.max(0.2, zoom / 1.2))}
      class="p-1.5 text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors border-b border-border cursor-pointer flex items-center justify-center"
      title="Zoom Out"
      aria-label="Zoom Out"
    >
      <span class="material-symbols-outlined text-[16px]">remove</span>
    </button>
    <button
      type="button"
      onclick={fitView}
      class="p-1.5 text-text-muted hover:text-text-main hover:bg-surface-2 transition-colors cursor-pointer flex items-center justify-center"
      title="Fit View"
      aria-label="Fit View"
    >
      <span class="material-symbols-outlined text-[16px]">crop_free</span>
    </button>
  </div>
</div>
