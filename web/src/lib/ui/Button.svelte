<script lang="ts">
  // Port of decolua/9router src/shared/components/Button.js
  import type { Snippet } from 'svelte'

  type Variant = 'primary' | 'secondary' | 'outline' | 'ghost' | 'danger' | 'success'
  type Size = 'sm' | 'md' | 'lg'

  let {
    variant = 'primary',
    size = 'md',
    type = 'button',
    icon = '',
    iconRight = '',
    disabled = false,
    loading = false,
    fullWidth = false,
    title = '',
    class: klass = '',
    onclick,
    children
  }: {
    title?: string
    variant?: Variant
    size?: Size
    type?: 'button' | 'submit' | 'reset'
    icon?: string
    iconRight?: string
    disabled?: boolean
    loading?: boolean
    fullWidth?: boolean
    class?: string
    onclick?: (e: MouseEvent) => void
    children?: Snippet
  } = $props()
  const variants: Record<Variant, string> = {
    primary:
      'bg-primary hover:bg-primary-hover text-white focus-visible:ring-2 focus-visible:ring-primary/15 disabled:bg-surface-3 disabled:text-text-muted',
    secondary:
      'bg-surface-2 hover:bg-surface-3 text-text-main border border-border focus-visible:ring-2 focus-visible:ring-primary/15 focus-visible:border-primary/60 disabled:opacity-50',
    outline:
      'border border-border text-text-main hover:bg-surface-2 hover:border-primary/40 focus-visible:ring-2 focus-visible:ring-primary/15 focus-visible:border-primary/60',
    ghost:
      'text-text-muted hover:bg-surface-2 hover:text-text-main focus-visible:ring-2 focus-visible:ring-primary/15',
    danger:
      'bg-danger hover:bg-danger/80 text-white focus-visible:ring-2 focus-visible:ring-primary/15 disabled:bg-surface-3 disabled:text-text-muted',
    success:
      'bg-success hover:bg-success/80 text-white focus-visible:ring-2 focus-visible:ring-primary/15 disabled:bg-surface-3 disabled:text-text-muted',
  }

  const sizes: Record<Size, string> = {
    sm: 'h-7 px-2.5 text-xs rounded-sm',
    md: 'h-8 px-3 text-[13px] rounded-sm',
    lg: 'h-9 px-4 text-sm rounded-sm',
  }
</script>

<button
  {type}
  {title}
  class="inline-flex items-center justify-center gap-2 font-medium transition-all duration-150 ease-out cursor-pointer focus-visible:outline-none active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed disabled:active:scale-100 {variants[variant]} {sizes[size]} {fullWidth ? 'w-full' : ''} {klass}"
  disabled={disabled || loading}
  {onclick}
>
  {#if loading}
    <span class="material-symbols-outlined animate-spin text-[18px]">progress_activity</span>
  {:else if icon}
    <span class="material-symbols-outlined text-[18px]">{icon}</span>
  {/if}
  {@render children?.()}
  {#if iconRight && !loading}
    <span class="material-symbols-outlined text-[18px]">{iconRight}</span>
  {/if}
</button>
