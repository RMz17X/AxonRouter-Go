import type { ModelCaps } from '../../api/client'
import { parseCustomModelsResponse, parseDisabledModelsMap, type CustomModelEntry } from '../../lib/customModels'

export interface PickerExtrasSource {
  getModelAliases: () => Promise<{ aliases?: Record<string, string> } | null>
  getCustomModels: () => Promise<unknown>
  getDisabledModels: () => Promise<unknown>
  getModelDeprecations: () => Promise<{ deprecations?: Record<string, unknown> } | null>
  /** Optional so test doubles pre-dating the credit pill keep working. */
  getModelCaps?: (provider: string) => Promise<{ provider: string; caps: Record<string, ModelCaps> } | null>
}

export interface ResolvedPickerExtras {
  modelAliases: Record<string, string>
  customModels: CustomModelEntry[]
  disabledModels: Record<string, string[]>
  /** Models the gateway saw refuse with a 410, keyed "<provider>/<model>". */
  deprecations: Record<string, unknown>
  /**
   * Server-resolved caps for the credit-priced providers, keyed
   * "<provider>/<model>". Credit lives ONLY in the backend's one table
   * (internal/providers/codebuddy_catalog.go) and reaches the picker
   * through GET /api/models/caps — never copied into the static bundle.
   */
  modelCaps: Record<string, ModelCaps>
}

export const EMPTY_PICKER_EXTRAS: ResolvedPickerExtras = {
  modelAliases: {},
  customModels: [],
  disabledModels: {},
  deprecations: {},
  modelCaps: {},
}

/**
 * The only providers whose caps carry a per-request credit price — the
 * CodeBuddy family's scope in the backend credit table. Fetching caps for
 * every catalog provider would be ~100 requests per picker open for data
 * only these three can have.
 */
export const CREDIT_CAPS_PROVIDERS = ['codebuddy-cn', 'codebuddy-intl', 'workbuddy']

/**
 * Fetch the three model-metadata endpoints in one batch and hand back one
 * object, so the picker can publish it in a single state write.
 *
 * The modal used to chain three independent `.then()` callbacks, each writing
 * its own `$state`. Every settle re-ran `resolveModelPickerGroups` and
 * re-rendered the whole pill list — three full passes per open even when the
 * payload had not changed, which is most of the cost behind issue #61.
 *
 * Each endpoint catches on its own: one failing request must not discard the
 * two that succeeded.
 */
export async function loadPickerExtras(source: PickerExtrasSource): Promise<ResolvedPickerExtras> {
  const [aliasRes, customRes, disabledRes, deprecationRes, ...capsResList] = await Promise.all([
    source.getModelAliases().catch(() => null),
    source.getCustomModels().catch(() => null),
    source.getDisabledModels().catch(() => null),
    source.getModelDeprecations().catch(() => null),
    ...CREDIT_CAPS_PROVIDERS.map((p) =>
      source.getModelCaps ? source.getModelCaps(p).catch(() => null) : Promise.resolve(null)
    ),
  ])

  const modelCaps: Record<string, ModelCaps> = {}
  capsResList.forEach((res, i) => {
    if (!res?.caps) return
    const provider = CREDIT_CAPS_PROVIDERS[i]
    for (const [modelId, caps] of Object.entries(res.caps)) {
      modelCaps[`${provider}/${modelId}`] = caps
    }
  })

  return {
    modelAliases: aliasRes?.aliases || {},
    customModels: parseCustomModelsResponse(customRes),
    disabledModels: parseDisabledModelsMap(disabledRes),
    deprecations: deprecationRes?.deprecations || {},
    modelCaps,
  }
}