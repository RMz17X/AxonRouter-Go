/**
 * The panel universe of the Usage page: every card, table and dialog each Usage
 * section can draw, grouped by the section that draws it. Pure data — no state,
 * no storage — so the arrangement logic in lib/usagePanelPlan.ts and the state in
 * lib/usagePanel.svelte.ts can both build on it, and bun can unit-test the plan
 * without the Svelte compiler.
 *
 * The order of the items below is the order the sections draw them in when
 * nothing has been arranged (see USAGE_PANEL_DEFAULT_ORDER).
 */

export type UsagePanelGroupId = 'details' | 'cache' | 'compression'

export type UsagePanelId =
  | 'details.lifetime'
  | 'details.heatmap'
  | 'details.nodeHealth'
  | 'details.requests'
  | 'details.payload'
  | 'cache.trend'
  | 'cache.provider'
  | 'cache.model'
  | 'cache.invalidation'
  | 'cache.entries'
  | 'compression.trend'
  | 'compression.mode'
  | 'compression.provider'
  | 'compression.model'
  | 'compression.topSavers'
  | 'compression.receipts'

export interface UsagePanelItem {
  id: UsagePanelId
  label: string
}

export interface UsagePanelGroup {
  id: UsagePanelGroupId
  label: string
  items: UsagePanelItem[]
}

/**
 * Every panel the Usage page can draw, in the order the page draws it. The
 * Overview section is not here: it mirrors AxonRouter's Overview tab with a
 * fixed layout, so it has no arrangeable panels.
 */
export const USAGE_PANEL_GROUPS: UsagePanelGroup[] = [
  {
    id: 'details',
    label: 'Usage › Details',
    items: [
      { id: 'details.lifetime', label: 'Lifetime KPI cards' },
      { id: 'details.heatmap', label: 'Activity Heatmap' },
      { id: 'details.nodeHealth', label: 'Node Health' },
      { id: 'details.requests', label: 'Recent Request Details table' },
      { id: 'details.payload', label: 'Request payload modal (View)' },
    ],
  },
  {
    id: 'cache',
    label: 'Usage › Cache Analytics',
    items: [
      { id: 'cache.trend', label: 'Hourly cache trend' },
      { id: 'cache.provider', label: 'Breakdown by provider' },
      { id: 'cache.model', label: 'Breakdown by model' },
      { id: 'cache.invalidation', label: 'Cache invalidation & cleanup' },
      { id: 'cache.entries', label: 'Cached entries' },
    ],
  },
  {
    id: 'compression',
    label: 'Usage › Compression Analytics',
    items: [
      { id: 'compression.trend', label: 'Hourly token savings trend' },
      { id: 'compression.mode', label: 'Breakdown by mode' },
      { id: 'compression.provider', label: 'Breakdown by provider' },
      { id: 'compression.model', label: 'Breakdown by model' },
      { id: 'compression.topSavers', label: 'Top 10 biggest token savers' },
      { id: 'compression.receipts', label: 'Upstream usage receipts' },
    ],
  },
]

export const USAGE_PANEL_IDS: UsagePanelId[] = USAGE_PANEL_GROUPS.flatMap((g) => g.items.map((i) => i.id))

export type UsagePanelPreset = 'default' | 'advanced' | 'simple' | 'super-lite'

export interface UsagePanelPresetDef {
  id: UsagePanelPreset
  label: string
  hint: string
  /** The panels this preset leaves on screen. */
  visible: UsagePanelId[]
}

/**
 * Panels whose position is fixed, with the reason the Settings card prints. The
 * arrangement may still hide them, but moving one would move a dialog or the
 * paged list out of the flow that owns it, so their arrows are shown disabled
 * rather than offering a control that would put the list above the totals it
 * explains.
 */
export const USAGE_PANEL_FIXED_POSITION: Partial<Record<UsagePanelId, string>> = {
  'details.requests': 'Stays below the Details panels (paged list)',
  'details.payload': 'Opened from a row, not a card in the stack',
}

/**
 * Groups whose panels can be arranged. The two analytics sections interleave
 * their panels with stat grids that are not panels, so their stacking order is
 * the layout's and the Settings card says so instead of offering arrows that
 * could not be honoured.
 */
export const USAGE_PANEL_ORDERABLE_GROUPS: UsagePanelGroupId[] = ['details']

/**
 * The panels a dashboard that has never been arranged starts without: the
 * three Details cards that only repeat what the Overview already says.
 * defaultLayout() hides these, and the Default preset restores exactly this
 * set.
 */
export const USAGE_PANEL_DEFAULT_HIDDEN: UsagePanelId[] = [
  'details.lifetime',
  'details.heatmap',
  'details.nodeHealth',
]

/**
 * Four ready-made shapes. Default is what an unarranged dashboard shows: the
 * Details section down to its paged request table. Advanced is everything;
 * Simple drops the operational panels; Super Lite keeps only lifetime totals
 * and the request table. A preset fills the toggles only: an arrangement,
 * once made, is kept.
 */
export const USAGE_PANEL_PRESETS: UsagePanelPresetDef[] = [
  {
    id: 'default',
    label: 'Default',
    hint: 'No Details-only cards',
    visible: USAGE_PANEL_IDS.filter((id) => !USAGE_PANEL_DEFAULT_HIDDEN.includes(id)),
  },
  {
    id: 'advanced',
    label: 'Advanced',
    hint: 'Every panel',
    visible: [...USAGE_PANEL_IDS],
  },
  {
    id: 'simple',
    label: 'Simple',
    hint: 'No cleanup/raw-entry panels, no payload modal',
    visible: USAGE_PANEL_IDS.filter(
      (id) =>
        !['details.payload', 'cache.invalidation', 'cache.entries', 'compression.receipts'].includes(id)
    ),
  },
  {
    id: 'super-lite',
    label: 'Super Lite',
    hint: 'Lifetime totals and the request table only',
    visible: ['details.lifetime', 'details.requests'],
  },
]
