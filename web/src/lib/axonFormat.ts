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

const enUS = new Intl.NumberFormat('en-US')

/** Full en-US grouping, no abbreviation (Axon's fmtNumber for table counts). */
export function fmtNumberUS(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '0'
  return enUS.format(Math.trunc(value))
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
