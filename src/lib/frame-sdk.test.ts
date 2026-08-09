import { describe, it, expect } from 'vitest'
import { __testing, projectToFull, type MetricSeries } from './frame-sdk'

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

// crToQuota, crToNode and crToPolicy are module-private, exposed only via the
// __testing barrel at the bottom of frame-sdk.ts for these tests.
describe('crToQuota', () => {
  it('reads the real usage the controller aggregates', () => {
    const q = __testing.crToQuota({
      metadata: { name: 'quota-high', namespace: 'default' },
      spec: { serviceClass: 'HIGH', maxGPUs: 4, maxCPU: '8', maxMemory: '16Gi' },
      status: {
        used: { 'limits.cpu': '3', 'limits.memory': '6Gi', 'requests.nvidia.com/gpu': '2' },
        namespaces: 2,
      },
    })
    expect(q.usedCPU).toBe('3')
    expect(q.usedMemory).toBe('6Gi')
    expect(q.usedGPUs).toBe(2)
    expect(q.namespaces).toBe(2)
  })

  it('falls back when no namespace has reported usage', () => {
    const q = __testing.crToQuota({
      metadata: { name: 'quota-low', namespace: 'default' },
      spec: { serviceClass: 'LOW' },
    })
    expect(q.usedCPU).toBe('0')
    expect(q.usedGPUs).toBe(0)
    expect(q.namespaces).toBe(0)
  })
})

describe('crToNode', () => {
  it('takes the GPU count from status.allocatable, not a spec field that does not exist', () => {
    const n = __testing.crToNode({
      metadata: { name: 'w2' },
      spec: { ip: '10.0.0.2', serviceClass: 'HIGH' },
      status: { allocatable: { cpu: '8', memory: '32Gi', 'nvidia.com/gpu': '1' } },
    })
    expect(n.gpuCount).toBe(1)
  })

  it('reports zero GPUs when allocatable carries no GPU key', () => {
    const n = __testing.crToNode({
      metadata: { name: 'w3' },
      spec: { ip: '10.0.0.3', serviceClass: 'LOW' },
      status: { allocatable: { cpu: '4', memory: '16Gi' } },
    })
    expect(n.gpuCount).toBe(0)
  })
})

describe('crToPolicy', () => {
  it('no longer reports resource ceilings SchedulingPolicySpec never had', () => {
    const p = __testing.crToPolicy({
      metadata: { name: 'neura-default' },
      spec: { scheduler: 'volcano', queueName: 'neura-high', queueWeight: 100 },
    }) as unknown as Record<string, unknown>
    expect(p).not.toHaveProperty('maxGPUs')
    expect(p).not.toHaveProperty('maxCPUs')
  })
})
