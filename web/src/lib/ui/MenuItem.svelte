<!--
  One entry of Menu.

  Lives beside Menu rather than inside the analytics folder because issue #224
  collapsed the control rows of six screens — quota tracker, proxy pools,
  providers, API keys, the analytics sections and the top bar — into this one
  menu, and an entry of a shared menu cannot belong to one of them.
-->
<script lang="ts">
  interface Props {
    label: string
    /** Material Symbols glyph shown left of the label. */
    icon?: string
    /** Renders this entry as `menuitemcheckbox`, for a toggle. */
    checkbox?: boolean
    /** Text shown on the right instead of a check, for example "15s". */
    note?: string
    pressed?: boolean
    disabled?: boolean
    /** Paints the entry in the destructive tone, for a delete verb. */
    danger?: boolean
    onSelect?: () => void
  }

  let {
    label,
    icon = '',
    checkbox = false,
    note = '',
    pressed = false,
    disabled = false,
    danger = false,
    onSelect = () => {},
  }: Props = $props()
</script>

<button
  type="button"
  // `aria-checked` only means anything on menuitemcheckbox, so a toggle entry
  // asks for that role rather than announcing a bare menuitem that carries a
  // state no assistive technology would read.
  role={checkbox ? 'menuitemcheckbox' : 'menuitem'}
  aria-checked={checkbox ? pressed : undefined}
  disabled={disabled}
  onclick={onSelect}
  class="flex h-8 w-full items-center gap-2.5 rounded-sm px-2.5 text-left text-[13px] transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/15 disabled:cursor-not-allowed disabled:opacity-50 {danger
    ? 'text-danger hover:bg-danger/10'
    : 'text-text-main hover:bg-surface-2'}"
>
  {#if icon}
    <span
      class="material-symbols-outlined text-[18px] {pressed ? 'text-primary' : danger
        ? 'text-danger'
        : 'text-text-muted'}"
      aria-hidden="true"
    >
      {icon}
    </span>
  {/if}
  <span class="flex-1 truncate">{label}</span>
  {#if note}
    <span class="shrink-0 text-[11px] text-text-muted">{note}</span>
  {:else if pressed}
    <span class="material-symbols-outlined text-[16px] text-primary" aria-hidden="true">check</span>
  {/if}
</button>