/**
 * The arrangement half of the Usage panel preferences: which order the panels of
 * a section are drawn in, and how that order folds into renderable blocks.
 *
 * Pure on purpose — the reactive state (and its localStorage draft) lives in
 * lib/usagePanel.svelte.ts, so this module can be unit-tested without the Svelte
 * compiler, the way lib/period.ts and lib/comboMatch.ts are.
 */
import { USAGE_PANEL_GROUPS, type UsagePanelGroupId, type UsagePanelId } from './usagePanel'

/** The order a section draws its panels in when nothing has been arranged. */
export const USAGE_PANEL_DEFAULT_ORDER: Record<UsagePanelGroupId, UsagePanelId[]> =
  Object.fromEntries(USAGE_PANEL_GROUPS.map((group) => [group.id, group.items.map((item) => item.id)])) as Record<
    UsagePanelGroupId,
    UsagePanelId[]
  >

export interface UsagePanelBlock {
  /** Stable key for the `{#each}` over the section's blocks. */
  key: string
  ids: UsagePanelId[]
}

/**
 * blocksFor turns one section's ordered, visible panels into the blocks the
 * template draws: one block per panel. (The Overview's old topology+recent
 * row pairing went away with its fixed Axon layout; no remaining panel
 * shares a row.) Ordering is preserved exactly as given, so what the
 * settings list shows is what the page draws.
 */
export function blocksFor(orderedVisible: readonly UsagePanelId[]): UsagePanelBlock[] {
  const blocks: UsagePanelBlock[] = []
  const consumed = new Set<UsagePanelId>()
  for (const id of orderedVisible) {
    if (consumed.has(id)) continue
    blocks.push({ key: id, ids: [id] })
    consumed.add(id)
  }
  return blocks
}
