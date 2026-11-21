import { describe, expect, it } from 'vitest'
import { formatTops, peakTops, summarizeTops } from './tops'

describe('peakTops', () => {
  it('reads the DCGM model names', () => {
    expect(peakTops('Tesla T4')).toBe(130)
    expect(peakTops('Tesla P4')).toBe(22)
    expect(peakTops('Tesla P40')).toBe(47)
    expect(peakTops('NVIDIA A100-SXM4-40GB')).toBe(624)
  })
  it('does not mistake a card for a shorter name it contains', () => {
    expect(peakTops('NVIDIA L40S')).toBe(733)
    expect(peakTops('NVIDIA A10')).not.toBe(peakTops('NVIDIA A100-SXM4-40GB'))
    expect(peakTops('NVIDIA H100 PCIe')).toBeLessThan(peakTops('NVIDIA H100 80GB HBM3')!)
  })
  it('returns null rather than inventing a figure', () => {
    expect(peakTops('Quadro K620')).toBeNull()
    expect(peakTops('')).toBeNull()
  })
})

describe('summarizeTops', () => {
  it('scales the peak by utilisation and counts unknown cards apart', () => {
    const s = summarizeTops([
      { model: 'Tesla T4', utilPct: 50 },
      { model: 'Tesla T4', utilPct: 0 },
      { model: 'Mystery', utilPct: 100 },
    ])
    expect(s).toEqual({ peak: 260, used: 65, known: 2, unknown: 1 })
  })
  it('keeps used within peak even on an out-of-range reading', () => {
    const s = summarizeTops([{ model: 'Tesla T4', utilPct: 400 }])
    expect(s.used).toBeLessThanOrEqual(s.peak)
  })
})

describe('formatTops', () => {
  it('formats', () => {
    expect(formatTops(null)).toBe('—')
    expect(formatTops(130)).toBe('130 TOPS')
    expect(formatTops(22.04)).toBe('22 TOPS')
    expect(formatTops(154.8)).toBe('155 TOPS')
  })
})
