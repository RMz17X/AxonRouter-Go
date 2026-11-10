// Shared rendering helpers for a request's recorded smart-router decision
// (RequestDetailItem.smartDecision). Used by the Request Details list and
// the Realtime card so the chip reads identically in both.
import type { RequestDetailItem } from './types'

export type SmartDecision = NonNullable<RequestDetailItem['smartDecision']>

export function smartDecisionLabel(d: SmartDecision): string {
  const conf = d.confidence !== undefined ? ` ${Math.round(d.confidence * 100)}%` : ''
  if (d.verdict === 'easy') return `AI Easy${conf}`
  if (d.verdict === 'hard') return `AI Hard${conf}`
  return 'Fallback'
}

export function smartDecisionIcon(d: SmartDecision): string {
  if (d.verdict === 'easy') return 'bolt'
  if (d.verdict === 'hard') return 'psychology'
  return 'alt_route'
}

export function smartDecisionClasses(d: SmartDecision): string {
  if (d.verdict === 'easy') return 'border-emerald-500/20 bg-emerald-500/10 text-emerald-500'
  if (d.verdict === 'hard') return 'border-purple-500/20 bg-purple-500/10 text-purple-400'
  return 'border-border bg-surface-2 text-text-muted'
}

export function smartDecisionTitle(d: SmartDecision): string {
  const parts = [`Smart router decision: ${d.source}`]
  if (d.model) parts.push(`led with ${d.model}`)
  if (d.confidence !== undefined) parts.push(`confidence ${Math.round(d.confidence * 100)}%`)
  if (d.threshold) parts.push(`threshold ${Math.round(d.threshold * 100)}%`)
  return parts.join(' · ')
}
