import type { ProviderConnection } from '../../api/client'

export type MediaKind = 'embedding' | 'image' | 'tts' | 'stt' | 'video' | 'systemone' | 'webSearch' | 'webFetch'
export interface MediaKindInfo {
  kind: MediaKind
  title: string
  singular: string
  description: string
  emptyMessage: string
}

export const MEDIA_KIND_INFO: Record<MediaKind, MediaKindInfo> = {
  embedding: {
    kind: 'embedding',
    title: 'Embedding Models & Providers',
    singular: 'Embedding',
    description: 'High-dimensional vector embedding models for semantic search and retrieval.',
    emptyMessage: 'No embedding providers found.',
  },
  image: {
    kind: 'image',
    title: 'Text to Image Models & Providers',
    singular: 'Text to Image',
    description: 'Diffusion and generative image creation models and image generation endpoints.',
    emptyMessage: 'No text-to-image providers found.',
  },
  tts: {
    kind: 'tts',
    title: 'Text To Speech Models & Providers',
    singular: 'Text To Speech',
    description: 'Neural voice synthesis, audio generation, and speech rendering endpoints.',
    emptyMessage: 'No text-to-speech providers found.',
  },
  stt: {
    kind: 'stt',
    title: 'Speech To Text Models & Providers',
    singular: 'Speech To Text',
    description: 'Speech recognition, audio transcription, and multilingual captioning endpoints.',
    emptyMessage: 'No speech-to-text providers found.',
  },
  video: {
    kind: 'video',
    title: 'Video Generation Models & Providers',
    singular: 'Video',
    description: 'Text-to-video generation, video extension, and motion synthesis models.',
    emptyMessage: 'No video generation providers found.',
  },
  systemone: {
    kind: 'systemone',
    title: 'System One Models & Providers',
    singular: 'System One',
    description: 'Structured state evaluation models with yes/no question answering.',
    emptyMessage: 'No System One providers found.',
  },
  webSearch: {
    kind: 'webSearch',
    title: 'Web Search Providers',
    singular: 'Web Search',
    description: 'Web search APIs and live search endpoints.',
    emptyMessage: 'No web search providers found.',
  },
  webFetch: {
    kind: 'webFetch',
    title: 'Web Fetch Providers',
    singular: 'Web Fetch',
    description: 'URL content extraction, scraper, and web reader endpoints.',
    emptyMessage: 'No web fetch providers found.',
  },
}

export interface MediaProviderStats {
  total: number
  active: number
  errorCount: number
  status: 'connected' | 'ready' | 'error' | 'none' | 'disabled'
  connections: ProviderConnection[]
}

/**
 * The per-provider aggregate shape served by GET /api/providers/stats:
 * provider id -> auth type -> counters.
 */
export type MediaSummaryStats = Record<
  string,
  Record<
    string,
    {
      total?: number
      connected?: number
      error?: number
      exhausted?: number
      allDisabled?: boolean
      states?: Record<string, number>
    }
  >
>

/**
 * Counts a provider's connections for a media tile.
 *
 * `summaryStats` is the aggregate the dashboard already loads from
 * /api/providers/stats. It is the preferred source: the full connection
 * list is no longer shipped to the browser on boot, so a tile that filters
 * the (now empty) global list would render "No connections" for a provider
 * with hundreds of live accounts.
 *
 * The connection list is still accepted, and is used as a fallback for
 * callers that hold a provider's roster directly (the detail page loads one
 * provider's rows on demand).
 */
export function getMediaProviderStats(
  connections: ProviderConnection[],
  providerId: string,
  noAuth?: boolean,
  summaryStats?: MediaSummaryStats
): MediaProviderStats {
  const list = connections.filter((c) => c.provider === providerId)

  let total: number
  let active: number
  let errorCount: number
  let allDisabled: boolean

  const provStats = summaryStats?.[providerId]
  if (provStats && Object.keys(provStats).length > 0) {
    total = 0
    active = 0
    errorCount = 0
    allDisabled = true
    for (const stat of Object.values(provStats)) {
      total += stat.total || 0
      active += stat.connected || 0
      errorCount += stat.error || 0
      if (!stat.allDisabled && (stat.connected || 0) > 0) allDisabled = false
    }
    // A provider with rows but no live connection is "disabled", not
    // "none" — the aggregate reports total > 0 in that case.
    if (total === 0) allDisabled = false
  } else {
    total = list.length
    const activeList = list.filter((c) => c.isActive === 1)
    active = activeList.length
    errorCount = list.filter(
      (c) => c.testStatus === 'error' || (c.testStatus !== 'active' && c.testStatus !== 'passed' && Boolean(c.lastError))
    ).length
    allDisabled = total > 0 && activeList.length === 0
  }

  let status: 'connected' | 'ready' | 'error' | 'none' | 'disabled' = 'none'
  if (errorCount > 0) {
    status = 'error'
  } else if (allDisabled) {
    status = 'disabled'
  } else if (active > 0) {
    status = 'connected'
  } else if (noAuth) {
    status = 'ready'
  } else if (total > 0) {
    // Rows exist but none are connected and none are in error: the pool is
    // present but parked (cooldown, validation, model lock). Reporting
    // "none" here would read as "no accounts configured".
    status = 'disabled'
  }

  return {
    total,
    active,
    errorCount,
    status,
    connections: list,
  }
}
