// Shared group/type derivation for the proxy assignment pickers.
//
// AddConnectionModal, ProviderDetailView and NoAuthProxyCard each carried a
// copy-pasted derivation of the group and type options their proxy pickers
// offer. They live here so the three surfaces cannot drift apart again:
// a group option exists for every distinct non-empty trimmed `group` value,
// a type option for every distinct non-empty `type` value, and both carry
// the total pool count and the active pool count (the surfaces label their
// options with the active count). Entries with zero active pools are kept —
// a picker must still be able to show a group that currently has no live
// member. Sorted by name.
import type { ProxyPool } from '../api/client'

export interface ProxyGroupSummary {
  name: string
  total: number
  active: number
}

export interface ProxyTypeSummary {
  type: string
  total: number
  active: number
}

export function deriveProxyGroups(pools: ProxyPool[]): ProxyGroupSummary[] {
  const byName = new Map<string, ProxyGroupSummary>()
  for (const p of pools) {
    const name = (p.group || '').trim()
    if (!name) continue
    let entry = byName.get(name)
    if (!entry) {
      entry = { name, total: 0, active: 0 }
      byName.set(name, entry)
    }
    entry.total += 1
    if (p.isActive) entry.active += 1
  }
  return Array.from(byName.values()).sort((a, b) => a.name.localeCompare(b.name))
}

export function deriveProxyTypes(pools: ProxyPool[]): ProxyTypeSummary[] {
  const byType = new Map<string, ProxyTypeSummary>()
  for (const p of pools) {
    const type = (p.type || '').trim()
    if (!type) continue
    let entry = byType.get(type)
    if (!entry) {
      entry = { type, total: 0, active: 0 }
      byType.set(type, entry)
    }
    entry.total += 1
    if (p.isActive) entry.active += 1
  }
  return Array.from(byType.values()).sort((a, b) => a.type.localeCompare(b.type))
}
