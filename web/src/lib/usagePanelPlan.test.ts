import assert from 'node:assert/strict'
import { test } from 'node:test'
import { USAGE_PANEL_DEFAULT_ORDER, blocksFor } from './usagePanelPlan'
import { moveBeforeInList, moveInList, normalizeListAgainst } from './orderList'
import { USAGE_PANEL_IDS, USAGE_PANEL_PRESETS } from './usagePanel'
import type { UsagePanelId } from './usagePanel'

// The arrangement decides what the Usage page draws and in which order, and
// the way it can go wrong is silent: an unknown id in storage hides a panel
// from its section for good. (The Overview section is not arranged at all —
// it mirrors AxonRouter's Overview with a fixed layout — so these tests pin
// the arrangeable Details group.)

test('the order repair appends panels this build knows that storage predates', () => {
  const stored = ['details.requests', 'details.lifetime']
  assert.deepEqual(normalizeListAgainst(stored, USAGE_PANEL_DEFAULT_ORDER.details), [
    'details.requests',
    'details.lifetime',
    'details.heatmap',
    'details.nodeHealth',
    'details.payload',
  ])
})

test('the Default preset hides the three Details-only cards', () => {
  const preset = USAGE_PANEL_PRESETS.find((p) => p.id === 'default')
  assert.ok(preset, 'the default preset must exist')
  const hidden = USAGE_PANEL_IDS.filter((id) => !preset.visible.includes(id))
  assert.deepEqual(hidden, ['details.lifetime', 'details.heatmap', 'details.nodeHealth'])
})

test('the order repair drops unknown and duplicated ids', () => {
  const stored = ['details.heatmap', 'details.gone', 'details.heatmap']
  const order = normalizeListAgainst(stored, USAGE_PANEL_DEFAULT_ORDER.details)
  assert.equal(order[0], 'details.heatmap')
  assert.equal(order.length, USAGE_PANEL_DEFAULT_ORDER.details.length)
})

test('the order repair keeps a group to its own panels', () => {
  const order = normalizeListAgainst(['cache.entries', 'details.nodeHealth'], USAGE_PANEL_DEFAULT_ORDER.details)
  assert.deepEqual(order.slice(0, 4), [
    'details.nodeHealth',
    'details.lifetime',
    'details.heatmap',
    'details.requests',
  ])
})

test('moveInList swaps one place and refuses to leave the list', () => {
  const order: UsagePanelId[] = ['details.lifetime', 'details.heatmap', 'details.nodeHealth']
  assert.deepEqual(moveInList(order, 'details.heatmap', -1), [
    'details.heatmap',
    'details.lifetime',
    'details.nodeHealth',
  ])
  assert.deepEqual(moveInList(order, 'details.heatmap', 1), [
    'details.lifetime',
    'details.nodeHealth',
    'details.heatmap',
  ])
  // Past an end: unchanged, which is what a disabled arrow button relies on.
  assert.deepEqual(moveInList(order, 'details.lifetime', -1), order)
  assert.deepEqual(moveInList(order, 'details.nodeHealth', 1), order)
  assert.deepEqual(moveInList(order, 'details.missing' as UsagePanelId, 1), order)
})

test('moveBeforeInList drops a panel into the slot above or below its target', () => {
  const order: UsagePanelId[] = [
    'details.lifetime',
    'details.heatmap',
    'details.nodeHealth',
    'details.requests',
  ]
  assert.deepEqual(moveBeforeInList(order, 'details.requests', 'details.lifetime', true), [
    'details.requests',
    'details.lifetime',
    'details.heatmap',
    'details.nodeHealth',
  ])
  assert.deepEqual(moveBeforeInList(order, 'details.lifetime', 'details.nodeHealth', false), [
    'details.heatmap',
    'details.nodeHealth',
    'details.lifetime',
    'details.requests',
  ])
})

test('moveBeforeInList leaves the order alone when the drop changes nothing', () => {
  const order: UsagePanelId[] = ['details.lifetime', 'details.heatmap', 'details.nodeHealth']
  // Onto itself, and into the gap the panel already sits in.
  assert.deepEqual(moveBeforeInList(order, 'details.heatmap', 'details.heatmap', true), order)
  assert.deepEqual(moveBeforeInList(order, 'details.heatmap', 'details.lifetime', false), order)
  assert.deepEqual(moveBeforeInList(order, 'details.heatmap', 'details.nodeHealth', true), order)
  // An id this build does not know is not a drop target either.
  assert.deepEqual(moveBeforeInList(order, 'details.gone' as UsagePanelId, 'details.lifetime', true), order)
})

// blocksFor used to pair the Overview's topology+recent panels into one row
// block. That pairing went away with the Overview's fixed Axon layout: every
// panel is now its own block, in exactly the order given.
test('blocksFor gives every panel its own block, in the given order', () => {
  const blocks = blocksFor(['details.heatmap', 'details.lifetime', 'details.requests'])
  assert.deepEqual(
    blocks.map((b) => b.ids),
    [['details.heatmap'], ['details.lifetime'], ['details.requests']]
  )
})

test('blocksFor collapses a duplicated panel into a single block', () => {
  const blocks = blocksFor(['details.heatmap', 'details.heatmap', 'details.lifetime'])
  assert.deepEqual(
    blocks.map((b) => b.ids),
    [['details.heatmap'], ['details.lifetime']]
  )
})

test('blocksFor leaves each panel a full-width block of its own', () => {
  const blocks = blocksFor(['details.requests', 'details.lifetime'])
  assert.deepEqual(
    blocks.map((b) => b.ids),
    [['details.requests'], ['details.lifetime']]
  )
})

test('blocksFor keeps single-panel groups one block per panel', () => {
  const blocks = blocksFor(['details.heatmap', 'details.lifetime'])
  assert.deepEqual(
    blocks.map((b) => b.ids),
    [['details.heatmap'], ['details.lifetime']]
  )
})
