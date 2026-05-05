import { describe, it, expect } from 'vitest'
import {
  generateClusterNodes,
  calculateClusterStats,
  updateNodeMetrics,
  formatBytes,
  formatBandwidth
} from './cluster'

describe('generateClusterNodes', () => {
  it('generates the requested number of nodes', () => {
    const nodes = generateClusterNodes(10)
    expect(nodes).toHaveLength(10)
  })

  it('assigns unique IDs to every node', () => {
    const nodes = generateClusterNodes(16)
    const ids = new Set(nodes.map(n => n.id))
    expect(ids.size).toBe(16)
  })

  it('assigns valid status values', () => {
    const validStatuses = new Set(['online', 'offline', 'degraded', 'provisioning'])
    const nodes = generateClusterNodes(32)
    for (const node of nodes) {
      expect(validStatuses.has(node.status)).toBe(true)
    }
  })

  it('assigns each node to one of the three zones', () => {
    const validZones = new Set(['zone-a', 'zone-b', 'zone-c'])
    const nodes = generateClusterNodes(32)
    for (const node of nodes) {
      expect(validZones.has(node.zone)).toBe(true)
    }
  })

  it('GPU migInstances is 0 when migEnabled is false, and >0 when true', () => {
    // Generate enough nodes to have a good sample of GPU metrics
    const nodes = generateClusterNodes(200)
    for (const node of nodes) {
      for (const gpu of node.gpuMetrics ?? []) {
        if (!gpu.migEnabled) {
          expect(gpu.migInstances).toBe(0)
        } else {
          expect(gpu.migInstances).toBeGreaterThan(0)
        }
      }
    }
  })

  it('generates hardware with a valid deviceType', () => {
    const validTypes = new Set(['server', 'storage', 'network', 'pdu', 'ups', 'blank'])
    // Run multiple times so we can detect if 'network' is ever generated
    const nodes = generateClusterNodes(200)
    for (const node of nodes) {
      expect(validTypes.has(node.hardware.deviceType)).toBe(true)
    }
  })

  it('generates network device types with fixed probability (regression: was unreachable)', () => {
    // With 200 nodes the probability of never hitting network (5% chance) is ~0.
    // This test will reliably catch if the condition order reverts.
    const nodes = generateClusterNodes(200)
    const hasNetwork = nodes.some(n => n.hardware.deviceType === 'network')
    expect(hasNetwork).toBe(true)
  })

  it('generates storage device types more often than network', () => {
    const nodes = generateClusterNodes(200)
    const storageCount = nodes.filter(n => n.hardware.deviceType === 'storage').length
    const networkCount = nodes.filter(n => n.hardware.deviceType === 'network').length
    // storage ~15%, network ~5% → storage should be more common
    expect(storageCount).toBeGreaterThan(networkCount)
  })
})

describe('calculateClusterStats', () => {
  it('counts statuses correctly', () => {
    const nodes = generateClusterNodes(32)
    const stats = calculateClusterStats(nodes)
    const expectedOnline = nodes.filter(n => n.status === 'online').length
    const expectedDegraded = nodes.filter(n => n.status === 'degraded').length
    const expectedOffline = nodes.filter(n => n.status === 'offline').length
    expect(stats.onlineNodes).toBe(expectedOnline)
    expect(stats.degradedNodes).toBe(expectedDegraded)
    expect(stats.offlineNodes).toBe(expectedOffline)
    expect(stats.totalNodes).toBe(32)
  })

  it('derives totalCpu from actual hardware, not a hardcoded constant', () => {
    const nodes = generateClusterNodes(8)
    const stats = calculateClusterStats(nodes)
    const expectedTotal = nodes.reduce((sum, n) => sum + n.hardware.cpuCores, 0)
    expect(stats.totalCpu).toBe(expectedTotal)
    // Make sure it's not the old hardcoded value (8 * 100 = 800)
    // unless by coincidence all nodes have exactly 100 cores, which is unlikely
    const sumIsCores = nodes.every(n => n.hardware.cpuCores === 100)
    if (!sumIsCores) {
      expect(stats.totalCpu).not.toBe(nodes.length * 100)
    }
  })

  it('derives totalMemory from actual hardware', () => {
    const nodes = generateClusterNodes(8)
    const stats = calculateClusterStats(nodes)
    const expectedTotal = nodes.reduce((sum, n) => sum + n.hardware.memoryGB, 0)
    expect(stats.totalMemory).toBe(expectedTotal)
  })

  it('derives totalStorage from actual hardware', () => {
    const nodes = generateClusterNodes(8)
    const stats = calculateClusterStats(nodes)
    const expectedTotal = nodes.reduce((sum, n) => sum + n.hardware.storageGB, 0)
    expect(stats.totalStorage).toBe(expectedTotal)
  })

  it('usedCpu is proportional to cpu metric and cpuCores (not percentage of hardcoded 100)', () => {
    const nodes = generateClusterNodes(4)
    const online = nodes.filter(n => n.status !== 'offline')
    const stats = calculateClusterStats(nodes)
    const expectedUsed = online.reduce((sum, n) => sum + (n.metrics.cpu / 100 * n.hardware.cpuCores), 0)
    expect(stats.usedCpu).toBeCloseTo(expectedUsed, 5)
  })

  it('offline nodes do not contribute to usedCpu, usedMemory, usedStorage or networkThroughput', () => {
    const nodes = generateClusterNodes(8)
    // Force all nodes offline
    const offlineNodes = nodes.map(n => ({ ...n, status: 'offline' as const }))
    const stats = calculateClusterStats(offlineNodes)
    expect(stats.usedCpu).toBe(0)
    expect(stats.usedMemory).toBe(0)
    expect(stats.usedStorage).toBe(0)
    expect(stats.networkThroughput).toBe(0)
  })

  it('handles an empty node list gracefully', () => {
    const stats = calculateClusterStats([])
    expect(stats.totalNodes).toBe(0)
    expect(stats.totalCpu).toBe(0)
    expect(stats.usedCpu).toBe(0)
    expect(stats.networkThroughput).toBe(0)
  })
})

describe('updateNodeMetrics', () => {
  it('returns a node with updated lastSeen timestamp', () => {
    // Force the node to be online so updateNodeMetrics actually updates lastSeen
    // (offline nodes are intentionally returned unchanged by design).
    const [rawNode] = generateClusterNodes(1)
    const node = { ...rawNode, status: 'online' as const }
    const before = Date.now()
    const updated = updateNodeMetrics(node)
    expect(updated.lastSeen).toBeGreaterThanOrEqual(before)
  })

  it('keeps cpu metric in [0, 100]', () => {
    const [node] = generateClusterNodes(1)
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(node)
      expect(updated.metrics.cpu).toBeGreaterThanOrEqual(0)
      expect(updated.metrics.cpu).toBeLessThanOrEqual(100)
    }
  })

  it('keeps memory metric in [0, 100]', () => {
    const [node] = generateClusterNodes(1)
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(node)
      expect(updated.metrics.memory).toBeGreaterThanOrEqual(0)
      expect(updated.metrics.memory).toBeLessThanOrEqual(100)
    }
  })

  it('does not mutate the original node', () => {
    const [node] = generateClusterNodes(1)
    const originalCpu = node.metrics.cpu
    updateNodeMetrics(node)
    expect(node.metrics.cpu).toBe(originalCpu)
  })

  it('keeps GPU utilization in [0, 100] across many updates', () => {
    // find a node with GPU metrics (most nodes have them); try up to 50
    const nodes = generateClusterNodes(50)
    const gpuNode = nodes.find(n => n.gpuMetrics && n.gpuMetrics.length > 0 && n.status !== 'offline')
    if (!gpuNode) return // no GPU node generated this run — skip
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(gpuNode)
      for (const gpu of updated.gpuMetrics ?? []) {
        expect(gpu.utilizationPercent).toBeGreaterThanOrEqual(0)
        expect(gpu.utilizationPercent).toBeLessThanOrEqual(100)
      }
    }
  })

  it('keeps GPU memoryUsedGB within [0, memoryTotalGB] across many updates', () => {
    const nodes = generateClusterNodes(50)
    const gpuNode = nodes.find(n => n.gpuMetrics && n.gpuMetrics.length > 0 && n.status !== 'offline')
    if (!gpuNode) return
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(gpuNode)
      for (const gpu of updated.gpuMetrics ?? []) {
        expect(gpu.memoryUsedGB).toBeGreaterThanOrEqual(0)
        expect(gpu.memoryUsedGB).toBeLessThanOrEqual(gpu.memoryTotalGB)
      }
    }
  })

  it('keeps GPU temperatureC within [30, 95] across many updates', () => {
    const nodes = generateClusterNodes(50)
    const gpuNode = nodes.find(n => n.gpuMetrics && n.gpuMetrics.length > 0 && n.status !== 'offline')
    if (!gpuNode) return
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(gpuNode)
      for (const gpu of updated.gpuMetrics ?? []) {
        expect(gpu.temperatureC).toBeGreaterThanOrEqual(30)
        expect(gpu.temperatureC).toBeLessThanOrEqual(95)
      }
    }
  })

  it('keeps GPU powerWatts within [50, 500] across many updates', () => {
    const nodes = generateClusterNodes(50)
    const gpuNode = nodes.find(n => n.gpuMetrics && n.gpuMetrics.length > 0 && n.status !== 'offline')
    if (!gpuNode) return
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(gpuNode)
      for (const gpu of updated.gpuMetrics ?? []) {
        expect(gpu.powerWatts).toBeGreaterThanOrEqual(50)
        expect(gpu.powerWatts).toBeLessThanOrEqual(500)
      }
    }
  })

  it('keeps GPU NVLink bandwidth non-negative across many updates', () => {
    const nodes = generateClusterNodes(50)
    const gpuNode = nodes.find(n => n.gpuMetrics && n.gpuMetrics.length > 0 && n.status !== 'offline')
    if (!gpuNode) return
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(gpuNode)
      for (const gpu of updated.gpuMetrics ?? []) {
        expect(gpu.nvlinkBandwidthGBps).toBeGreaterThanOrEqual(0)
      }
    }
  })

  it('keeps GPU smOccupancyPercent in [0, 100] across many updates', () => {
    const nodes = generateClusterNodes(50)
    const gpuNode = nodes.find(n => n.gpuMetrics && n.gpuMetrics.length > 0 && n.status !== 'offline')
    if (!gpuNode) return
    for (let i = 0; i < 20; i++) {
      const updated = updateNodeMetrics(gpuNode)
      for (const gpu of updated.gpuMetrics ?? []) {
        expect(gpu.smOccupancyPercent).toBeGreaterThanOrEqual(0)
        expect(gpu.smOccupancyPercent).toBeLessThanOrEqual(100)
      }
    }
  })
})

describe('formatBytes', () => {
  it('formats GB values with one decimal place', () => {
    expect(formatBytes(1)).toBe('1.0 GB')
    expect(formatBytes(512)).toBe('512.0 GB')
    expect(formatBytes(1023)).toBe('1023.0 GB')
  })

  it('formats TB for values >= 1024', () => {
    expect(formatBytes(1024)).toBe('1.0 TB')
    expect(formatBytes(2048)).toBe('2.0 TB')
  })
})

describe('formatBandwidth', () => {
  it('formats Mbps for values < 1000', () => {
    expect(formatBandwidth(100)).toBe('100 Mbps')
    expect(formatBandwidth(999)).toBe('999 Mbps')
  })

  it('formats Gbps for values >= 1000', () => {
    expect(formatBandwidth(1000)).toBe('1.00 Gbps')
    expect(formatBandwidth(10000)).toBe('10.00 Gbps')
  })
})
