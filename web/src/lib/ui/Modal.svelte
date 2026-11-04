<script lang="ts">
  // Port of decolua/9router src/shared/components/Modal.js
  import type { Snippet } from 'svelte'
  import { pushEscapeHandler } from './escapeStack'

  let {
    isOpen = false,
    onClose,
    title = '',
    icon = '',
    size = 'md',
    closeOnOverlay = true,
    showTrafficLights = true,
    class: klass = '',
    children,
    footer
  }: {
    isOpen?: boolean
    onClose?: () => void
    title?: string
    icon?: string
    size?: 'sm' | 'md' | 'lg' | 'xl' | '2xl' | '3xl' | 'full'
    closeOnOverlay?: boolean
    showTrafficLights?: boolean
    class?: string
    children?: Snippet
    footer?: Snippet
  } = $props()

  const sizes = {
    sm: 'max-w-sm',
    md: 'max-w-md',
    lg: 'max-w-lg',
    xl: 'max-w-xl',
    '2xl': 'max-w-2xl',
    '3xl': 'max-w-3xl',
    full: 'max-w-4xl'
  }

  // Lock background scroll while the modal is open.
  $effect(() => {
    if (!isOpen) return
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = previous
    }
  })

  // Escape closes only the topmost dialog, so a dialog opened over this one
  // (a picker, a confirm) is dismissed without taking this one with it.
  $effect(() => {
    if (!isOpen) return
    return pushEscapeHandler(() => onClose?.())
  })
</script>

{#if isOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="absolute inset-0 bg-black/60 fade-in"
      onclick={closeOnOverlay ? () => onClose?.() : undefined}
      onkeydown={(e) => e.key === 'Escape' && onClose?.()}
      role="presentation"
    ></div>

    <div
      class="relative w-full bg-surface border border-border rounded-brand-lg shadow-[var(--shadow-elevated)] fade-in {sizes[size]} {klass}"
    >
      {#if title || showTrafficLights}
        <div class="flex items-center justify-between p-2 border-b border-border">
          <div class="flex items-center">
            {#if title}
              <h2 class="flex items-center gap-2 text-sm font-semibold text-text-main">
                {#if icon}
                  <span class="material-symbols-outlined text-[20px] text-primary">{icon}</span>
                {/if}
                {title}
              </h2>
            {/if}
          </div>
          <button
            type="button"
            onclick={() => onClose?.()}
            aria-label="Close"
            class="p-1.5 rounded-sm text-text-muted hover:bg-surface-2 hover:text-text-main transition-colors"
          >
            <span class="material-symbols-outlined text-[20px]">close</span>
          </button>
        </div>
      {/if}

      <div class="p-6 max-h-[calc(85vh-100px)] overflow-y-auto custom-scrollbar">
        {@render children?.()}
      </div>

      {#if footer}
        <div class="flex items-center justify-end gap-3 p-6 border-t border-border-subtle">
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}
