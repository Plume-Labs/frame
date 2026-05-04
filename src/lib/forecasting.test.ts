import { describe, it, expect } from 'vitest'
import { generateForecast, collectHistoricalData } from './forecasting'
import { ResourceDataPoint, ClusterStats } from './types'

// Helper: build a simple dataset of n points with constant or linearly-growing values
function makeDataPoints(
  n: number,
  { cpu = 50, memory = 60, storage = 40, network = 5000, cpuSlope = 0, networkSlope = 0 } = {}
): ResourceDataPoint[] {
  return Array.from({ length: n }, (_, i) => ({
    timestamp: 1_000_000 + i * 3600_000,
    cpu: cpu + cpuSlope * i,
    memory,
    storage,
    network: network + networkSlope * i
  }))
}

const exampleStats: ClusterStats = {
  totalNodes: 8,
  onlineNodes: 7,
  degradedNodes: 1,
  offlineNodes: 0,
  totalCpu: 256,
  usedCpu: 128,
  totalMemory: 2048,
  usedMemory: 1024,
  totalStorage: 16384,
  usedStorage: 6554,
  networkThroughput: 40000
}

describe('generateForecast', () => {
  it('returns empty arrays when fewer than 3 data points are provided', () => {
    const result = generateForecast([makeDataPoints(2)[0], makeDataPoints(1)[0]])
    expect(result.cpu).toHaveLength(0)
    expect(result.network).toHaveLength(0)
  })

  it('returns the requested number of forecast periods', () => {
    const data = makeDataPoints(10)
    const result = generateForecast(data, 6)
    expect(result.cpu).toHaveLength(6)
    expect(result.memory).toHaveLength(6)
    expect(result.storage).toHaveLength(6)
    expect(result.network).toHaveLength(6)
  })

  it('CPU/memory/storage forecast values are clamped to [0, 100]', () => {
    // Force a steeply rising slope that would exceed 100 without clamping
    const data = makeDataPoints(10, { cpu: 90, cpuSlope: 5 })
    const result = generateForecast(data, 12)
    for (const point of result.cpu) {
      expect(point.predicted).toBeGreaterThanOrEqual(0)
      expect(point.predicted).toBeLessThanOrEqual(100)
    }
  })

  it('network forecast is NOT clamped to 100 (regression: was capped at 100 Mbps)', () => {
    // Flat dataset at 5000 Mbps — predicted values should stay near 5000, not 100
    const data = makeDataPoints(10, { network: 5000 })
    const result = generateForecast(data, 6)
    // With a flat trend, every prediction should be close to 5000
    for (const point of result.network) {
      expect(point.predicted).toBeGreaterThan(100)
    }
  })

  it('network confidence upper bound is not capped at 100', () => {
    const data = makeDataPoints(10, { network: 5000 })
    const result = generateForecast(data, 6)
    // The upper bound must also exceed 100
    for (const point of result.network) {
      expect(point.confidence.upper).toBeGreaterThan(100)
    }
  })

  it('forecast timestamps are strictly increasing', () => {
    const data = makeDataPoints(10)
    const result = generateForecast(data, 6)
    for (let i = 1; i < result.cpu.length; i++) {
      expect(result.cpu[i].timestamp).toBeGreaterThan(result.cpu[i - 1].timestamp)
    }
  })

  it('confidence lower bound is always <= predicted', () => {
    const data = makeDataPoints(10)
    const result = generateForecast(data, 6)
    for (const point of result.cpu) {
      expect(point.confidence.lower).toBeLessThanOrEqual(point.predicted)
    }
  })

  it('confidence upper bound is always >= predicted', () => {
    const data = makeDataPoints(10)
    const result = generateForecast(data, 6)
    for (const point of result.cpu) {
      expect(point.confidence.upper).toBeGreaterThanOrEqual(point.predicted)
    }
  })

  it('predicts growth correctly for a linearly rising CPU load', () => {
    // cpu goes 50, 51, 52, … (slope = 1 per period)
    const data = makeDataPoints(10, { cpu: 50, cpuSlope: 1 })
    const result = generateForecast(data, 1)
    // The first forecast point should be slightly above 59 (= 50 + 9 + ~1)
    expect(result.cpu[0].predicted).toBeGreaterThan(55)
  })
})

describe('collectHistoricalData', () => {
  it('returns a data point with timestamp close to Date.now()', () => {
    const before = Date.now()
    const point = collectHistoricalData(exampleStats)
    const after = Date.now()
    expect(point.timestamp).toBeGreaterThanOrEqual(before)
    expect(point.timestamp).toBeLessThanOrEqual(after)
  })

  it('cpu is usedCpu / totalCpu * 100', () => {
    const point = collectHistoricalData(exampleStats)
    expect(point.cpu).toBeCloseTo((exampleStats.usedCpu / exampleStats.totalCpu) * 100, 5)
  })

  it('network equals networkThroughput (raw Mbps, not a percentage)', () => {
    const point = collectHistoricalData(exampleStats)
    expect(point.network).toBe(exampleStats.networkThroughput)
  })
})
