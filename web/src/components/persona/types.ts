import type { ChatTurn, JailbreakDoc, JailbreakKeyword } from '../../api/client'

/**
 * The preset editor's form state: a subset of JailbreakDoc. id and name are
 * editable, the timestamps are the store's. Typing it against the doc rather
 * than letting it infer keeps `order` — which the API accepts and the list
 * sorts by — from being a type error the moment it is read.
 */
export interface PresetFormState {
  id: string
  name: string
  prompt: string
  kind: string
  geminiSafety: boolean
  mode: 'single' | 'chat'
  position: 'prepend' | 'append' | 'replace'
  turns: ChatTurn[]
  source: string
  keywords: JailbreakKeyword[]
  excludes: JailbreakKeyword[]
  order: number
}

export type PersonaSegment = 'presets' | 'bindings'

export function emptyPresetForm(): PresetFormState {
  return {
    id: '',
    name: '',
    prompt: '',
    kind: '',
    geminiSafety: false,
    mode: 'single',
    position: 'prepend',
    turns: [
      { role: 'user', content: '' },
      { role: 'assistant', content: '' },
    ],
    source: '',
    keywords: [],
    excludes: [],
    order: 0,
  }
}

export function presetToForm(preset: JailbreakDoc): PresetFormState {
  return {
    id: preset.id || '',
    name: preset.name || '',
    prompt: preset.prompt || '',
    kind: preset.kind || '',
    geminiSafety: !!preset.geminiSafety,
    mode: preset.mode === 'chat' ? 'chat' : 'single',
    position:
      preset.position === 'append' || preset.position === 'replace' ? preset.position : 'prepend',
    turns:
      preset.turns && preset.turns.length > 0
        ? preset.turns.map((t) => ({ ...t }))
        : [
            { role: 'user', content: '' },
            { role: 'assistant', content: '' },
          ],
    source: preset.source || '',
    keywords: preset.keywords ? [...preset.keywords] : [],
    excludes: preset.excludes ? [...preset.excludes] : [],
    order: preset.order || 0,
  }
}

/** The 2–3 line excerpt a preset card shows: the prompt, or a chat script's
 * first turn that carries text. */
export function presetExcerpt(preset: JailbreakDoc): string {
  if (preset.mode === 'chat') {
    const first = preset.turns?.find((t) => t.content?.trim())
    return first ? first.content : ''
  }
  return preset.prompt || ''
}
