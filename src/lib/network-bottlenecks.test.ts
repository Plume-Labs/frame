import { describe, expect, it } from 'vitest'

import type { LinkTraffic, Machine } from './machines'
import { percentLabel, POD_TRAFFIC_QUERIES, rankLinks, rateLabel, toPodTraffic } from './network-bottlenecks'

const traffic = (peak: number | undefined, bytes = 0): LinkTraffic => ({
  historySeconds: 900,
  rxBytesPerSecond: 0,
  txBytesPerSecond: 0,
  peakRxBytesPerSecond: bytes,
  peakTxBytesPerSecond: 0,
  peakUtilizationPercent: peak,
  errorsPerMinute: 0,
  dropsPerMinute: 0,
})

const machine = (network: Machine['network']) => ({ name: 'm', nodeRef: 'node-a', network }) as Machine

describe('rankLinks', () => {
  it('puts the busiest first, and a link with no speed after those it can be compared with', () => {
    const rows = rankLinks([
      machine({
        interfaces: [
          { name: 'low', traffic: traffic(5) },
          { name: 'virtio', traffic: traffic(undefined, 900_000_000) },
          { name: 'eno1', master: 'bond0', traffic: traffic(95) },
        ],
        bonds: [{ name: 'bond0', traffic: traffic(50) }],
      }),
    ])
    expect(rows.map((r) => r.name)).toEqual(['eno1', 'bond0', 'low', 'virtio'])
    expect(rows.map((r) => r.kind)).toEqual(['member', 'bond', 'port', 'port'])
  })

  it('leaves out a link with no rates, and a machine with no report', () => {
    expect(rankLinks([machine({ interfaces: [{ name: 'eno1' }] }), machine(null)])).toEqual([])
  })
})

describe('labels', () => {
  it('reads link rates in bits, as link speeds are stated', () => {
    expect(rateLabel(125_000_000)).toBe('1.0 Gb/s')
    expect(rateLabel(1_250_000)).toBe('10.0 Mb/s')
    expect(rateLabel(1_000)).toBe('8 kb/s')
  })

  it('tells an unknown percentage from zero', () => {
    expect(percentLabel(undefined)).toBe('—')
    expect(percentLabel(0)).toBe('0 %')
  })
})

describe('pod traffic', () => {
  it('leaves host-network pods out of both directions', () => {
    for (const q of Object.values(POD_TRAFFIC_QUERIES)) expect(q).toContain('host_network="true"')
  })

  it('sorts, and drops what Prometheus could not count', () => {
    const pods = toPodTraffic([
      { labels: { namespace: 'a', pod: 'small' }, value: 1 },
      { labels: { namespace: 'a', pod: 'big' }, value: 9 },
      { labels: { namespace: 'a', pod: 'nan' }, value: NaN },
    ])
    expect(pods.map((p) => p.pod)).toEqual(['big', 'small'])
    expect(toPodTraffic(undefined)).toEqual([])
  })
})
