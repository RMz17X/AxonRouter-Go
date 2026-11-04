<script lang="ts">
  // Port of decolua/9router src/shared/components/Badge.js
  import type { Snippet } from 'svelte'

  type Tone = 'default' | 'neutral' | 'primary' | 'success' | 'warning' | 'danger' | 'error' | 'info' | 'outline'

  let {
    tone,
    variant = 'default',
    size = 'md',
    dot = false,
    class: klass = '',
    children
  }: {
    tone?: Tone
    variant?: Tone
    size?: 'sm' | 'md' | 'lg'
    dot?: boolean
    class?: string
    children?: Snippet
  } = $props()

  const activeTone = $derived(tone || variant || 'default')

  const tones: Record<string, string> = {
    default: 'bg-surface-2 text-text-muted border border-border',
    neutral: 'bg-surface-2 text-text-muted border border-border',
    outline: 'bg-transparent text-text-muted border border-border',
    primary: 'bg-primary/10 text-primary border border-primary/25',
    success: 'bg-success/10 text-success border border-success/25',
    warning: 'bg-warning/10 text-warning border border-warning/25',
    danger: 'bg-danger/10 text-danger border border-danger/25',
    error: 'bg-danger/10 text-danger border border-danger/25',
    info: 'bg-info/10 text-info border border-info/25',
  }

  const dotColors: Record<string, string> = {
    default: 'bg-text-muted',
    neutral: 'bg-text-muted',
    outline: 'bg-text-muted',
    primary: 'bg-primary',
    success: 'bg-success',
    warning: 'bg-warning',
    danger: 'bg-danger',
    error: 'bg-danger',
    info: 'bg-info',
  }

  const sizeClasses: Record<string, string> = {
    sm: 'text-[11px] px-1.5 py-0.5',
    md: 'text-[11px] px-1.5 py-0.5',
    lg: 'text-xs px-2 py-0.5',
  }
</script>

<span
  class="inline-flex items-center gap-1.5 rounded-sm font-medium {sizeClasses[size] || sizeClasses.md} {tones[activeTone] || tones.default} {klass}"
>
  {#if dot}
    <span class="w-1.5 h-1.5 rounded-full shrink-0 {dotColors[activeTone] || 'bg-current'}"></span>
  {/if}
  {@render children?.()}
</span>
