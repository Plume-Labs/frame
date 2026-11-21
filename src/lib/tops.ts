/**
 * Compute capacity in TOPS (tera-operations per second) for the GPUs DCGM reports.
 *
 * Source: a spec table keyed on the model name DCGM exposes (`modelName`),
 * not a measurement. No exporter reports TOPS, and a benchmark would load the
 * cards it is meant to describe. The figure is the vendor's peak INT8 tensor
 * throughput, dense (without structured sparsity), the unit "TOPS" is quoted
 * in for inference cards. It is a ceiling, not what a workload achieves.
 *
 * A model missing from the table has no figure (`null`), shown as "—". We do
 * not guess: a wrong number on a capacity screen is worse than a blank one.
 * To add a card, add a row from its datasheet (INT8, dense).
 */
const PEAK_INT8_TOPS: ReadonlyArray<readonly [RegExp, number]> = [
  [/\bh100\b.*\bpcie\b/i, 1513],
  [/\bh100\b/i, 1979],
  [/\ba100\b/i, 624],
  [/\bl40s\b/i, 733],
  [/\bl4\b/i, 242],
  [/\ba10\b/i, 250],
  [/\ba6000\b/i, 154.8],
  [/\bt4\b/i, 130],
  [/\bp40\b/i, 47],
  [/\bp4\b/i, 22],
]

/** Peak INT8 TOPS (dense) of one GPU of this model, or null when the model is not in the table. */
export function peakTops(model: string): number | null {
  for (const [re, tops] of PEAK_INT8_TOPS) if (re.test(model)) return tops
  return null
}

export interface TopsSummary {
  /** Sum of the peak of every GPU whose model is known. */
  peak: number
  /** Peak scaled by each GPU's utilisation: an upper-bound estimate of what is in use. */
  used: number
  /** GPUs counted in the two sums. */
  known: number
  /** GPUs whose model is not in the table, left out of the sums. */
  unknown: number
}

export function summarizeTops(gpus: ReadonlyArray<{ model: string; utilPct: number }>): TopsSummary {
  const s: TopsSummary = { peak: 0, used: 0, known: 0, unknown: 0 }
  for (const g of gpus) {
    const peak = peakTops(g.model)
    if (peak === null) {
      s.unknown++
      continue
    }
    s.known++
    s.peak += peak
    s.used += (peak * Math.min(Math.max(g.utilPct, 0), 100)) / 100
  }
  return s
}

/** "130 TOPS", "1.2 POPS" is not a thing people read: stay in TOPS and keep one decimal under 100. */
export function formatTops(tops: number | null): string {
  if (tops === null) return '—'
  return `${tops >= 100 ? Math.round(tops) : Number(tops.toFixed(1))} TOPS`
}
