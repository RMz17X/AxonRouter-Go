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

// Binding-target helpers, shared by every surface that reads or writes a
// proxy selector (the badge, the provider lock, the connection modal).
// A target is one selector or a comma-separated list of them; each part
// is a pool ID, a "group:<name>" or "type:<key>" selector, or a bare
// name. These mirror the backend's SplitProxyTarget /
// NormalizeProxyTarget (internal/db/proxyPools.go) so the dashboard's
// derivations cannot drift from the resolver's.
export function splitProxyTarget(target: string): string[] {
  return target
    .split(',')
    .map((part) => part.trim())
    .filter((part) => part !== '' && part !== '__none__')
}

export interface ProxyTargetPart {
  kind: 'group' | 'type' | 'bare'
  name: string
}

// parseProxyTargetParts classifies each part of a target. The kind comes
// from the part's first prefix; repeated prefixes ("group:group:X" —
// the double-prefix a derivation bug once stored) are stripped so the
// name is the real group or type name either way.
export function parseProxyTargetParts(target: string): ProxyTargetPart[] {
  return splitProxyTarget(target).map((raw) => {
    let name = raw
    let kind: ProxyTargetPart['kind'] = 'bare'
    for (;;) {
      if (name.startsWith('group:')) {
        if (kind === 'bare') kind = 'group'
        name = name.slice(6)
      } else if (name.startsWith('type:')) {
        if (kind === 'bare') kind = 'type'
        name = name.slice(5)
      } else {
        break
      }
    }
    return { kind, name }
  })
}

// The bare values the backend treats as proxy types rather than group
// names (isKnownProxyType in internal/db and internal/handlers/chat).
const KNOWN_PROXY_TYPES = ['http', 'cloudflare', 'vercel', 'deno', 'netlify']

// normalizeProxyTarget converts a stored proxyGroup value into selector
// form: each part keeps an explicit prefix, a bare known type becomes a
// type selector, any other bare value becomes a group selector. An
// already-prefixed value passes through unchanged — prefixing it again
// was the all-red-badges bug ("group:group:ISP-BD" matches no pool).
export function normalizeProxyTarget(value: string): string {
  return splitProxyTarget(value)
    .map((part) => {
      if (part.startsWith('group:') || part.startsWith('type:')) return part
      return KNOWN_PROXY_TYPES.includes(part.toLowerCase()) ? `type:${part}` : `group:${part}`
    })
    .join(',')
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
