<script lang="ts">
  // Port of the toast stack in upstream DashboardLayout.js.
  import { notifications, type NotificationType } from '../notifications'

  function toastStyle(type: NotificationType): { icon: string; iconColor: string } {
    if (type === 'success') {
      return {
        icon: 'check_circle',
        iconColor: 'text-success',
      }
    }
    if (type === 'error') {
      return {
        icon: 'error',
        iconColor: 'text-danger',
      }
    }
    if (type === 'warning') {
      return {
        icon: 'warning',
        iconColor: 'text-warning',
      }
    }
    return {
      icon: 'info',
      iconColor: 'text-info',
    }
  }
</script>

<div class="fixed top-16 right-4 z-[80] flex w-[min(92vw,380px)] flex-col gap-2">
  {#each $notifications as n (n.id)}
    {@const style = toastStyle(n.type)}
    <div class="rounded-sm border border-border bg-surface px-3 py-1.5 text-xs text-text-main shadow-[var(--shadow-elevated)]">
      <div class="flex items-start gap-2">
        <span class="material-symbols-outlined text-[18px] leading-5 {style.iconColor}">{style.icon}</span>
        <div class="min-w-0 flex-1">
          {#if n.title}<p class="text-xs font-semibold mb-0.5">{n.title}</p>{/if}
          <p class="text-xs whitespace-pre-wrap break-words">{n.message}</p>
        </div>
        {#if n.dismissible}
          <button
            type="button"
            onclick={() => notifications.removeNotification(n.id)}
            class="rounded-sm text-text-muted hover:text-text-main cursor-pointer"
            aria-label="Dismiss notification"
          >
            <span class="material-symbols-outlined text-[16px]">close</span>
          </button>
        {/if}
      </div>
    </div>
  {/each}
</div>
