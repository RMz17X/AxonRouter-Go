<script lang="ts">
  import Badge from '../../lib/ui/Badge.svelte'
  import { emailPrivacy } from '../../lib/privacy'
  import type { ProviderCatalogItem } from '../../lib/providers'
  import ProviderArtwork from '../providers/ProviderArtwork.svelte'

  interface Props {
    provider: ProviderCatalogItem
    kind: string
    kindTitle: string
    parentPath: string
    onBack: () => void
  }

  let { provider, kind, kindTitle, parentPath, onBack }: Props = $props()
</script>

<!-- Header (Back button, Provider Avatar, Name, Get Key, Service Badges) -->
<div>
  <a
    class="inline-flex items-center gap-1 text-sm text-text-muted hover:text-primary transition-colors mb-4 cursor-pointer"
    href={parentPath}
    onclick={(e) => { e.preventDefault(); onBack() }}
  >
    <span class="material-symbols-outlined text-lg">arrow_back</span>
    {kindTitle}
  </a>
  <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-4">
    <div
      class="size-12 rounded-lg flex items-center justify-center shrink-0 border border-border/40 overflow-hidden"
      style="background-color: {provider.color ? provider.color + '15' : '#88888815'}"
    >
      <ProviderArtwork
        id={provider.id}
        alt={provider.name}
        class="object-contain size-8 rounded text-sm leading-none font-semibold"
      />
    </div>
    <div class="min-w-0 flex-1">
      <div class="flex items-center gap-3">
        <h1 class="text-2xl sm:text-3xl font-semibold tracking-tight text-text-main">{provider.name}</h1>
        {#if provider.notice?.apiKeyUrl || provider.website}
          <a
            href={provider.notice?.apiKeyUrl || provider.website}
            target="_blank"
            rel="noopener noreferrer"
            class="text-xs text-primary hover:underline inline-flex items-center gap-1"
          >
            <span class="material-symbols-outlined text-sm">open_in_new</span>
            Get API Key
          </a>
        {/if}
        <!-- Media tabs never mount the Providers view, so without a toggle
             here the masking applied elsewhere is unreachable from the
             seven tabs that mount this component. -->
        <button
          type="button"
          onclick={() => emailPrivacy.toggle()}
          aria-pressed={$emailPrivacy}
          class="ml-auto flex h-8 shrink-0 items-center gap-1 rounded-lg border px-2 text-xs transition-colors cursor-pointer {$emailPrivacy
            ? 'border-brand-500/40 bg-brand-500/10 text-brand-500 font-medium'
            : 'border-border-subtle bg-surface text-text-muted hover:text-text-main hover:bg-surface-2'}"
          title={$emailPrivacy ? 'Show full email' : 'Mask / hide email'}
        >
          <span class="material-symbols-outlined text-[16px]">{$emailPrivacy ? 'visibility_off' : 'visibility'}</span>
          <span class="hidden sm:inline">{$emailPrivacy ? 'Email Masked' : 'Mask Email'}</span>
        </button>
      </div>
      <div class="flex items-center gap-1.5 mt-1 flex-wrap">
        {#each provider.serviceKinds || [] as sk}
          <Badge tone={sk.toLowerCase() === kind.toLowerCase() ? 'primary' : 'default'} size="sm">
            {sk.toUpperCase()}
          </Badge>
        {/each}
      </div>
    </div>
  </div>
</div>

<!-- Notice Banner (Free tier / sign up) -->
{#if provider.notice?.text}
  <div class="flex flex-col gap-2 rounded-lg border border-blue-500/30 bg-blue-500/10 px-3 py-2 sm:flex-row sm:items-center">
    <span class="material-symbols-outlined text-[16px] text-blue-500 shrink-0">info</span>
    <p class="min-w-0 flex-1 text-xs leading-relaxed text-blue-600 dark:text-blue-400">{provider.notice.text}</p>
    {#if provider.notice.apiKeyUrl}
      <a
        href={provider.notice.apiKeyUrl}
        target="_blank"
        rel="noopener noreferrer"
        class="inline-flex justify-center rounded bg-blue-500 px-2 py-1 text-xs font-medium text-white transition-colors hover:bg-blue-600 sm:py-0.5"
      >
        Get API Key →
      </a>
    {/if}
  </div>
{/if}
