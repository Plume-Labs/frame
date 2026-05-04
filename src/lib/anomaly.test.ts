import { describe, it, expect } from 'vitest'
import { buildAnomalyPatterns, detectAnomalies } from './anomaly'
import { ResourceDataPoint } from './types'

function makePoints(values: { cpu: number; memory: number; storage: number; network: number }[]): ResourceDataPoint[] {
  return values.map((v, i) => ({ timestamp: 1_000_000 + i * 60_000, ...v }))
}

// 10 stable data points (network kept below the 80-unit sustained-high threshold)
const stablePoints = makePoints(
  Array.from({ length: 10 }, () => ({ cpu: 30, memory: 40, storage: 25, network: 50 }))
)

// 10 data points with a CPU spike at the end
const spikePoints = makePoints([
  ...Array.from({ length: 9 }, () => ({ cpu: 30, memory: 40, storage: 25, network: 50 })),
  { cpu: 98, memory: 40, storage: 25, network: 50 }
])

describe('buildAnomalyPatterns', () => {
  it('returns empty/zero patterns for empty input', () => {
    const patterns = buildAnomalyPatterns([])
    expect(patterns.cpu.baseline).toBe(0)
    expect(patterns.memory.baseline).toBe(0)
    expect(patterns.storage.baseline).toBe(0)
    expect(patterns.network.baseline).toBe(0)
  })

  it('computes a baseline close to the mean of the data', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    expect(patterns.cpu.baseline).toBeCloseTo(30, 0)
    expect(patterns.memory.baseline).toBeCloseTo(40, 0)
    expect(patterns.network.baseline).toBeCloseTo(50, 0)
  })

  it('has a non-negative baselineStdDev', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    expect(patterns.cpu.baselineStdDev).toBeGreaterThanOrEqual(0)
  })

  it('reports stable trend for constant data', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    expect(patterns.cpu.recentTrend).toBe('stable')
  })

  it('reports increasing trend for monotonically rising data', () => {
    const risingPoints = makePoints(
      Array.from({ length: 10 }, (_, i) => ({ cpu: 30 + i * 5, memory: 40, storage: 25, network: 50 }))
    )
    const patterns = buildAnomalyPatterns(risingPoints)
    expect(patterns.cpu.recentTrend).toBe('increasing')
  })
})

describe('detectAnomalies', () => {
  it('returns empty array when fewer than 5 history points', () => {
    const fewPoints = stablePoints.slice(0, 4)
    const patterns = buildAnomalyPatterns(fewPoints)
    const current = { timestamp: Date.now(), cpu: 99, memory: 99, storage: 99, network: 9999 }
    const anomalies = detectAnomalies(current, fewPoints, patterns)
    expect(anomalies).toHaveLength(0)
  })

  it('does not flag anomalies when current matches historical mean exactly', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    // Submit the same value as the constant history — there is no deviation at all
    const current = { timestamp: Date.now(), cpu: 30, memory: 40, storage: 25, network: 50 }
    const anomalies = detectAnomalies(current, stablePoints, patterns)
    expect(anomalies).toHaveLength(0)
  })

  it('detects a CPU spike anomaly when CPU jumps from ~30 to 98', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    const current = { timestamp: Date.now(), cpu: 98, memory: 40, storage: 25, network: 50 }
    const anomalies = detectAnomalies(current, spikePoints, patterns)
    const cpuAnomalies = anomalies.filter(a => a.resource === 'cpu')
    expect(cpuAnomalies.length).toBeGreaterThan(0)
  })

  it('returns anomalies sorted by severity (critical first)', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    const current = { timestamp: Date.now(), cpu: 98, memory: 98, storage: 98, network: 50 }
    const anomalies = detectAnomalies(current, spikePoints, patterns)
    const severityOrder: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3 }
    for (let i = 1; i < anomalies.length; i++) {
      expect(severityOrder[anomalies[i].severity]).toBeGreaterThanOrEqual(severityOrder[anomalies[i - 1].severity])
    }
  })

  it('each anomaly has a non-empty id, type, and recommendation', () => {
    const patterns = buildAnomalyPatterns(stablePoints)
    const current = { timestamp: Date.now(), cpu: 98, memory: 40, storage: 25, network: 50 }
    const anomalies = detectAnomalies(current, spikePoints, patterns)
    for (const a of anomalies) {
      expect(a.id).toBeTruthy()
      expect(a.type).toBeTruthy()
      expect(a.recommendation).toBeTruthy()
    }
  })
})
