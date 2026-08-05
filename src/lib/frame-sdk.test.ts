import { describe, it, expect } from 'vitest'
import { projectToFull, type MetricSeries } from './frame-sdk'

/** `n` samples one hour apart, starting at `from` and moving `perDay` %/day. */
function series(from: number, perDay: number, n = 12): MetricSeries {
  const hour = 3600_000
  return {
    metric: 'CPU',
    current: 0,
    history: Array.from({ length: n }, (_, i) => ({
      t: 1_700_000_000_000 + i * hour,
      v: from + (perDay / 24) * i,
    })),
  }
}

describe('projectToFull', () => {
  it('projects the days left from the slope', () => {
    // Starts at 50% and climbs 10%/day over 11 hours, so the *last* sample —
    // the one headroom is measured from — is 54.58%, leaving 45.42 points.
    const { projectedFullDays } = projectToFull(series(50, 10))
    expect(projectedFullDays).toBeCloseTo(45.4166 / 10, 2)
  })

  it('reports no projection for a flat series', () => {
    expect(projectToFull(series(50, 0)).projectedFullDays).toBeNull()
  })

  it('reports no projection when usage is falling', () => {
    expect(projectToFull(series(50, -10)).projectedFullDays).toBeNull()
  })

  it('treats imperceptible drift as flat rather than a far-off date', () => {
    // 0.05 %/day would otherwise project ~1000 days and read as a real finding.
    expect(projectToFull(series(50, 0.05)).projectedFullDays).toBeNull()
  })

  it('takes current from the last sample, not the first', () => {
    expect(projectToFull(series(50, 10)).current).toBeCloseTo(50 + (10 / 24) * 11, 5)
  })

  it('cannot fit a line through fewer than two points', () => {
    expect(projectToFull(series(50, 10, 1)).projectedFullDays).toBeNull()
    expect(projectToFull(series(50, 10, 0)).projectedFullDays).toBeNull()
  })
})
