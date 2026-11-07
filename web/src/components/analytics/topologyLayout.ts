/**
 * Stable ring layout for the provider topology map.
 *
 * The map used to place nodes by list position: the angle came from the
 * node's index in the activity-ordered providers list and the ring radii
 * from the list length, so a provider arriving, leaving, or merely being
 * reordered moved every node on screen. Here a node sits in a numbered
 * SLOT that its identity owns for the whole session:
 *
 * - A present provider keeps its slot; nobody else's arrival, departure,
 *   or reordering can move it.
 * - A provider that leaves frees its slot for newcomers, but the slot is
 *   remembered: if it returns while the slot is still free, it reclaims
 *   the exact seat it had.
 * - A newcomer takes the lowest free slot.
 * - The ring's capacity comes from a coarse tier ladder and only ever
 *   grows during the session, so the radii (and every angle, which is
 *   slot / capacity) stay put unless a genuinely larger concurrent set
 *   forces a tier step.
 *
 * Slot state is deliberately session-scoped and lives with the card that
 * owns the allocator: an unmount forgets every assignment. Identity keys
 * are the caller's job — the card passes the same lowercased, trimmed
 * provider id it already matches activity against.
 */

export interface TopologyRingRadii {
  rx: number
  ry: number
}

export interface TopologySlotAllocator {
  /** Reconcile the allocator with the providers currently on the map. */
  sync(ids: readonly string[]): void
  /** The slot a currently-present provider owns, if any. */
  slotOf(id: string): number | undefined
  /** Ring capacity in slots; grows by tiers, never shrinks mid-session. */
  readonly capacity: number
}

/**
 * Capacity tiers. Coarse on purpose: most maps hold a handful of
 * providers, and a step is the only moment existing angles may change,
 * so steps must be rare.
 */
const CAPACITY_TIERS = [6, 12, 18, 24, 36, 48, 64]

function capacityTierFor(needed: number): number {
  for (const tier of CAPACITY_TIERS) {
    if (needed <= tier) return tier
  }
  return needed
}

/**
 * Ring radii for a capacity, from the card's long-standing formulas
 * (upstream: rx = max(320, 204 * s / 2π), ry = max(200, 0.55 * rx)) —
 * with the slot capacity in place of the live provider count, so the
 * ring no longer breathes as providers come and go.
 */
export function topologyRingRadii(capacity: number): TopologyRingRadii {
  const rx = Math.max(320, (204 * capacity) / (2 * Math.PI))
  const ry = Math.max(200, 0.55 * rx)
  return { rx, ry }
}

/** Angle of a slot on a ring of the given capacity; slot 0 sits at the top. */
export function topologySlotAngle(slot: number, capacity: number): number {
  return -Math.PI / 2 + (2 * Math.PI * slot) / capacity
}

export function createTopologySlotAllocator(): TopologySlotAllocator {
  // Every slot a provider has owned this session, kept after it leaves
  // so a return can reclaim the same seat.
  const remembered = new Map<string, number>()
  // Slots owned by the providers on the map right now.
  let present = new Map<string, number>()
  let capacity = CAPACITY_TIERS[0]

  function sync(ids: readonly string[]): void {
    const next = new Map<string, number>()
    const taken = new Set<number>()

    // Still present: the slot is theirs, unconditionally.
    for (const id of ids) {
      const current = present.get(id)
      if (current !== undefined && !next.has(id)) {
        next.set(id, current)
        taken.add(current)
      }
    }
    // Returning: reclaim the remembered slot while it is still free. A
    // slot another present provider owns is never taken back — the
    // returner falls through to a fresh slot instead of moving anyone.
    for (const id of ids) {
      if (next.has(id)) continue
      const old = remembered.get(id)
      if (old !== undefined && !taken.has(old)) {
        next.set(id, old)
        taken.add(old)
      }
    }
    // New: the lowest free slot.
    for (const id of ids) {
      if (next.has(id)) continue
      let slot = 0
      while (taken.has(slot)) slot++
      next.set(id, slot)
      taken.add(slot)
    }

    present = next
    for (const [id, slot] of next) remembered.set(id, slot)

    let needed = 0
    for (const slot of next.values()) {
      if (slot + 1 > needed) needed = slot + 1
    }
    const tier = capacityTierFor(needed)
    if (tier > capacity) capacity = tier
  }

  return {
    sync,
    slotOf: (id) => present.get(id),
    get capacity() {
      return capacity
    }
  }
}
