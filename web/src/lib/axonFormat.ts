// Formatters matching AxonRouter's Usage page conventions, shared by the
// ported Overview components. Axon formats token counts with Indonesian-locale
// compaction (formatTokens), table counts with en-US grouping (fmtNumber), and
// costs as plain "$" + toFixed(2) with a "~" prefix on the KPI card.

/**
 * Indonesian-locale token compaction: grouping ".", decimal ",", units K/M/B,
 * at most 2 decimals (trimmed by the locale formatter). Examples:
 * 999 -> "999", 1000 -> "1K", 1200 -> "1,2K", 1542000 -> "1,54M".
 */
export function formatTokensID(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '0'
  const v = Math.trunc(value)
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs < 1000) return sign + abs.toLocaleString('id-ID')
  const units: [number, string][] = [
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'K'],
  ]
  for (const [div, suffix] of units) {
    if (abs >= div) {
      return sign + (abs / div).toLocaleString('id-ID', { maximumFractionDigits: 2 }) + suffix
    }
  }
  return sign + String(abs)
}

/** Compact unit formatting for tables & counts: values >= 1000 auto-abbreviate to K/M/B (e.g. 168303 -> 168K). */
export function fmtNumberUS(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '0'
  const v = Math.trunc(value)
  const abs = Math.abs(v)
  const sign = v < 0 ? '-' : ''
  if (abs < 1000) return sign + String(abs)

  const units: [number, string][] = [
    [1e9, 'B'],
    [1e6, 'M'],
    [1e3, 'K'],
  ]

  for (const [div, suffix] of units) {
    if (abs >= div) {
      const n = abs / div
      // For >= 10 units (e.g. 168,303 -> 168K, 12,400 -> 12K), round to integer.
      // For < 10 units (e.g. 1,500 -> 1.5K, 2,400,000 -> 2.4M), show 1 decimal if non-zero.
      const formatted = n >= 10 ? Math.round(n).toString() : (Math.round(n * 10) / 10).toString()
      return sign + formatted + suffix
    }
  }
  return sign + String(abs)
}

/** "$" + toFixed(2), Axon's table/KPI cost shape. */
export function fmtCost(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '$0.00'
  return '$' + value.toFixed(2)
}

/** KPI-card estimated cost: "~$x.xx". */
export function fmtCostApprox(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '~$0.00'
  return '~$' + value.toFixed(2)
}
