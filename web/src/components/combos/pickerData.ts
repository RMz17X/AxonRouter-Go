import type { Combo, ModelCaps, ProviderConnection, ProviderNode } from '../../api/client'
import { getModelCaps, getModelKind, getModelsByProviderId, PROVIDER_ID_TO_ALIAS } from '../../lib/models'
import { PROVIDER_CATALOG, isChatProvider } from '../../lib/providers'
import { parseSearchQuery, matchesSearch } from '../../lib/modelSearchFilter'

const MEDIA_KINDS: Record<string, true> = {
  image: true,
  tts: true,
  stt: true,
  embedding: true,
  video: true,
}

function isChatModel(m: unknown, target?: string): boolean {
  const kind = getModelKind(m)
  if (target === 'router' && (kind === 'systemone' || kind === 'decision')) {
    return true
  }
  if (!kind || kind === 'llm' || kind === 'chat') {
    const obj = typeof m === 'object' && m !== null ? (m as { kind?: string; type?: string }) : null
    return !(obj?.kind && MEDIA_KINDS[obj.kind]) && !(obj?.type && MEDIA_KINDS[obj.type])
  }
  return false
}

export interface PickerModel {
  id: string
  name: string
  value: string
  /** True when this entry is a model alias: `name` is the alias itself
   * (which the gateway resolves on the wire), `value` is the full model
   * behind it. Most consumers want `value`; a consumer that promises
   * "the name you picked is the name you get" wants `name`. */
  isAlias?: boolean
  caps: { vision: boolean; audioInput: boolean; reasoning: boolean; decision?: boolean }
  /** True when the gateway confirmed this model is gone upstream. */
  deprecated?: boolean
  /** Per-request credit price from the server caps (CodeBuddy family);
   *  flattened like the caps booleans so pill props compare by value. */
  credit?: number
  /** Server-resolved free flag from the same caps entry. */
  free?: boolean
}

export interface PickerGroup {
  id: string
  name: string
  color?: string
  accountCount?: number
  isConnected?: boolean
  models: PickerModel[]
}

export interface PickerExtras {
  modelAliases?: Record<string, string>
  customModels?: Array<{ providerAlias?: string; id: string; name?: string; type?: string }>
  disabledModels?: Record<string, string[]>
  /** Deprecations keyed "<provider>/<model>", from /api/models/deprecations. */
  deprecations?: Record<string, unknown>
  /** Server caps for credit-priced providers, keyed "<provider>/<model>". */
  modelCaps?: Record<string, ModelCaps>
  /** Provider IDs with active accounts / connections from providerStats or connections */
  connectedProviders?: string[]
  providerStats?: Record<string, Record<string, { total?: number; connected?: number }>>
}

/** Credit price / free flag for one picker row, from the server caps map. */
function creditFields(
  extras: PickerExtras,
  providerId: string,
  modelId: string
): { credit?: number; free?: boolean } {
  const caps = extras.modelCaps?.[`${providerId}/${modelId}`]
  if (!caps) return {}
  return {
    credit: typeof caps.credit === 'number' ? caps.credit : undefined,
    free: caps.free === true ? true : undefined,
  }
}

export function resolveModelPickerGroups(
  connections: ProviderConnection[] = [],
  providerNodes: ProviderNode[] = [],
  extras: PickerExtras = {},
  target?: string
): PickerGroup[] {
  // Upstream parity (page.js fetchData + ModelSelectModal.js groupedModels):
  // `activeProviders` holds ALL connections (no isActive filter) — every
  // connected provider shows, plus no-auth providers. Also include any
  // provider identified as having active connections in providerStats.
  const connectedProviderIds = new Set<string>()
  for (const c of connections) {
    if (c.provider) {
      connectedProviderIds.add(c.provider)
    }
  }
  if (extras.connectedProviders) {
    for (const p of extras.connectedProviders) {
      connectedProviderIds.add(p)
    }
  }
  if (extras.providerStats) {
    for (const [p, authMap] of Object.entries(extras.providerStats)) {
      if (!authMap || typeof authMap !== 'object') continue
      for (const item of Object.values(authMap)) {
        if (item && ((item.total ?? 0) > 0 || (item.connected ?? 0) > 0)) {
          connectedProviderIds.add(p)
          break
        }
      }
    }
  }

  function getAccountCount(provId: string, alias?: string): number {
    let count = 0
    const stats = extras.providerStats
    if (stats) {
      const pStats = stats[provId] || (alias ? stats[alias] : undefined)
      if (pStats) {
        for (const item of Object.values(pStats)) {
          count += item?.total || item?.connected || 0
        }
      }
    }
    if (count === 0 && connections.length > 0) {
      count = connections.filter((c) => c.provider === provId || (alias && c.provider === alias)).length
    }
    return count
  }

  const groups: PickerGroup[] = []
  const seenGroupIds = new Set<string>()

  // 1. Catalog providers (active connections or noAuth)
  for (const catItem of PROVIDER_CATALOG) {
    if (!isChatProvider(catItem) && !(target === 'router' && catItem.serviceKinds?.includes('systemone'))) {
      continue
    }
    const isConnected =
      connectedProviderIds.has(catItem.id) || (catItem.alias && connectedProviderIds.has(catItem.alias))
    // Only the explicit noAuth flag (upstream registry semantics): category
    // "free" providers like kiro/gemini-cli still require a connection.
    const isNoAuth = catItem.noAuth === true
    if (!isConnected && !isNoAuth) {
      continue
    }
    const alias = catItem.alias || PROVIDER_ID_TO_ALIAS[catItem.id] || catItem.id
    const rawModels = getModelsByProviderId(catItem.id)
    const hardcodedIds = new Set((rawModels || []).map((m) => m.id))
    const hasHardcoded = (rawModels || []).length > 0

    // Upstream parity (ModelSelectModal.js): custom models registered via
    // /api/models/custom for this provider (only aliasName === modelId shown
    // when hardcoded models exist — the "Add Model" button pattern).
    const customs = (extras.customModels || []).filter(
      (m) => m.providerAlias === alias && m.id && !hardcodedIds.has(m.id)
    )
    const validCustoms = customs.filter((m) => {
      if (!m.type || m.type === 'llm') return true
      return !MEDIA_KINDS[m.type]
    })
    const visibleCustoms = hasHardcoded
      ? validCustoms.filter((m) => (m.name || m.id) === m.id)
      : validCustoms

    // Upstream parity: aliases stored as {aliasName: "alias/modelId"}.
    const aliasEntries = Object.entries(extras.modelAliases || {}).filter(
      ([aliasName, fullModel]) =>
        typeof fullModel === 'string' &&
        fullModel.startsWith(`${alias}/`) &&
        (hasHardcoded ? aliasName === fullModel.replace(`${alias}/`, '') : true) &&
        !hardcodedIds.has(fullModel.replace(`${alias}/`, ''))
    )

    if ((!rawModels || rawModels.length === 0) && customs.length === 0 && aliasEntries.length === 0) {
      continue
    }

    const seenModelIds = new Set<string>()
    const models: PickerModel[] = []
    // A model the gateway saw refused with a 410 is marked, not hidden: the
    // operator may still want it in a combo while they replace it, and a
    // picker that silently dropped it would leave them editing JSON to find
    // out why it disappeared.
    const isDeprecated = (modelId: string) =>
      Boolean(extras.deprecations?.[`${catItem.id.toLowerCase()}/${modelId.toLowerCase()}`])


    for (const m of rawModels || []) {
      if (!m.id || seenModelIds.has(m.id) || !isChatModel(m, target)) continue
      seenModelIds.add(m.id)
      models.push({
        id: m.id,
        name: m.name || m.id,
        value: `${alias}/${m.id}`,
        caps: getModelCaps(m.id, m),
        deprecated: isDeprecated(m.id),
        ...creditFields(extras, catItem.id, m.id),
      })
    }

    for (const m of visibleCustoms) {
      if (seenModelIds.has(m.id)) continue
      seenModelIds.add(m.id)
      models.push({
        id: m.id,
        name: m.name || m.id,
        value: `${alias}/${m.id}`,
        caps: getModelCaps(m.id),
        deprecated: isDeprecated(m.id),
        ...creditFields(extras, catItem.id, m.id),
      })
    }

    for (const [aliasName, fullModel] of aliasEntries) {
      const modelId = (fullModel as string).replace(`${alias}/`, '')
      if (!modelId || seenModelIds.has(modelId)) continue
      seenModelIds.add(modelId)
      models.push({
        id: modelId,
        name: aliasName,
        value: fullModel as string,
        isAlias: true,
        caps: getModelCaps(modelId),
        deprecated: isDeprecated(modelId),
        ...creditFields(extras, catItem.id, modelId),
      })
    }

    // Upstream parity: filter out disabled models per provider
    // (disabled keyed by storage alias OR providerId).
    const disabled = new Set([
      ...((extras.disabledModels || {})[alias] || []),
      ...((extras.disabledModels || {})[catItem.id] || []),
    ])
    const visible = disabled.size > 0 ? models.filter((m) => !disabled.has(m.id)) : models

    if (visible.length > 0) {
      seenGroupIds.add(catItem.id)
      const accCount = getAccountCount(catItem.id, catItem.alias)
      groups.push({
        id: catItem.id,
        name: catItem.name || catItem.id,
        color: catItem.color,
        accountCount: accCount,
        isConnected: isConnected || accCount > 0,
        models: visible,
      })
    }
  }

  // 2. Custom (openai/anthropic-compatible) nodes — LLM-only. Upstream parity
  // (ModelSelectModal.js isCustomProvider): aliases filtered by raw providerId,
  // values use the display prefix, plus custom models registered for the node id;
  // placeholder when connected but empty.
  for (const node of providerNodes) {
    if (!node.id || seenGroupIds.has(node.id)) continue
    const conn = connections.find((c) => c.provider === node.id)
    const isConnectedNode = conn !== undefined || connectedProviderIds.has(node.id)
    const psd = (conn?.providerSpecificData || {}) as Record<string, unknown>
    const nodePrefix = (psd.prefix as string) || node.prefix || node.id
    const displayName = node.name || conn?.name || node.id
    const isCompatibleNode = node.type === 'openai-compatible' || node.type === 'anthropic-compatible'

    const registeredCustom = (extras.customModels || [])
      .filter((m) => m.providerAlias === node.id && m.id)
      .map((m) => ({
        id: m.id,
        name: m.name || m.id,
        value: `${nodePrefix}/${m.id}`,
        caps: getModelCaps(m.id),
        deprecated: Boolean(
          extras.deprecations?.[`${node.id.toLowerCase()}/${m.id.toLowerCase()}`]
        ),
      }))

    if (!isConnectedNode && (!isCompatibleNode || registeredCustom.length === 0)) continue

    const nodeModels = Object.entries(extras.modelAliases || {})
      .filter(([, fullModel]) => typeof fullModel === 'string' && fullModel.startsWith(`${node.id}/`))
      .map(([aliasName, fullModel]) => {
        const modelId = (fullModel as string).replace(`${node.id}/`, '')
        return {
          id: modelId,
          name: aliasName,
          value: `${nodePrefix}/${modelId}`,
          isAlias: true,
          caps: getModelCaps(modelId),
          deprecated: Boolean(
            extras.deprecations?.[`${node.id.toLowerCase()}/${modelId.toLowerCase()}`]
          ),
        }
      })
    const seen = new Set(nodeModels.map((m) => m.value))
    const mergedModels = [...nodeModels, ...registeredCustom.filter((m) => !seen.has(m.value))]

    const modelsToShow =
      mergedModels.length > 0
        ? mergedModels
        : [
            {
              id: `__placeholder__${node.id}`,
              name: `${nodePrefix}/model-id`,
              value: `${nodePrefix}/model-id`,
              caps: { vision: false, audioInput: false, reasoning: false },
            },
          ]

    seenGroupIds.add(node.id)
    const accCount = getAccountCount(node.id)
    groups.push({
      id: node.id,
      name: displayName,
      accountCount: accCount,
      isConnected: isConnectedNode || accCount > 0,
      models: modelsToShow,
    })
  }

  // Sort groups: providers with active connections / accounts appear first,
  // then other providers. Within each bucket, sort alphabetically by name.
  groups.sort((a, b) => {
    const aActive = (a.isConnected || (a.accountCount ?? 0) > 0) ? 1 : 0
    const bActive = (b.isConnected || (b.accountCount ?? 0) > 0) ? 1 : 0
    if (aActive !== bActive) {
      return bActive - aActive
    }
    return a.name.localeCompare(b.name)
  })

  return groups
}

export function resolveFilteredCombos(
  combos: Combo[] = [],
  currentComboName: string | undefined,
  searchQuery: string,
  target: string
): Combo[] {
  if (target === 'vision' || target === 'audio') {
    return []
  }

  const parsed = parseSearchQuery(searchQuery)
  if (parsed.isEmpty) {
    return combos.filter((c) => {
      // Upstream parity (page.js fetchData): webSearch/webFetch combos live
      // under media-providers/web. search-combo is excluded even when its kind
      // field is missing (older rows predate the kind column).
      if (c.kind && c.kind !== 'llm') return false
      if (c.name === 'search-combo' || c.name.startsWith('search-combo-')) return false
      if (currentComboName && c.name === currentComboName) return false
      return true
    })
  }
  return combos.filter((c) => {
    // Upstream parity (page.js fetchData): webSearch/webFetch combos live
    // under media-providers/web. search-combo is excluded even when its kind
    // field is missing (older rows predate the kind column).
    if (c.kind && c.kind !== 'llm') return false
    if (c.name === 'search-combo' || c.name.startsWith('search-combo-')) return false
    if (currentComboName && c.name === currentComboName) return false
    return matchesSearch(c.name, parsed)
  })
}

// resolveFilteredGroups applies the search text and the modality target. The
// already-added models are deliberately not an input: nothing here reads them, but
// passing them from the caller made the signature part of the reactive graph, so
// clicking one pill re-filtered and re-sorted every group and reconciled the
// whole pill list again (#61).
export function resolveFilteredGroups(
  groups: PickerGroup[] = [],
  searchQuery: string,
  target: string
): PickerGroup[] {
  const parsed = parseSearchQuery(searchQuery)

  return groups
    .map((group) => {
      let models = group.models

      // Filter by input-modality capability (vision / audio) matching upstream ModelSelectModal capFilter
      if (target === 'vision') {
        models = models.filter((m) => m.caps.vision)
      } else if (target === 'audio') {
        models = models.filter((m) => m.caps.audioInput)
      }

      if (models.length === 0) return null

      if (!parsed.isEmpty) {
        const groupMatches = matchesSearch(group.name, parsed)
        models = models.filter((m) => {
          if (matchesSearch(m, parsed)) return true
          if (groupMatches) {
            if (parsed.negativeTerms.length > 0) {
              return matchesSearch(m, { ...parsed, positiveTerms: [] })
            }
            return true
          }
          return false
        })
      }

      if (models.length === 0) return null

      // Sort models alphabetically; stable display avoids sudden layout shift and freezing on click
      const sorted = [...models].sort((a, b) => a.name.localeCompare(b.name))

      return {
        ...group,
        models: sorted,
      }
    })
    .filter((g): g is PickerGroup => g !== null)
}
