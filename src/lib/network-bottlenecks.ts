// The Network → Bottlenecks screen: every link of every machine, busiest first,
// and the pods moving the most. Pure.

import type { LinkTraffic, Machine } from './machines'

export interface BottleneckRow {
  node: string
  name: string
  /** A bond, or the port of one, which reads differently from a lone port. */
  kind: 'bond' | 'member' | 'port'
  traffic: LinkTraffic
}

function score(t: LinkTraffic): number {
  return t.peakUtilizationPercent ?? -1
}

/**
 * Links with a figure to compare come first, by their peak over 15 minutes; links
 * that give no speed follow, by bytes, since their percentage is unknown and not
 * zero. Links with no rates yet are left out: nothing to rank.
 */
export function rankLinks(machines: Machine[]): BottleneckRow[] {
  const rows: BottleneckRow[] = []
  for (const m of machines) {
    const node = m.nodeRef || m.name
    for (const b of m.network?.bonds ?? []) if (b.traffic) rows.push({ node, name: b.name, kind: 'bond', traffic: b.traffic })
    for (const i of m.network?.interfaces ?? []) {
      if (i.traffic) rows.push({ node, name: i.name, kind: i.master ? 'member' : 'port', traffic: i.traffic })
    }
  }
  const peakBytes = (t: LinkTraffic) => Math.max(t.peakRxBytesPerSecond, t.peakTxBytesPerSecond)
  return rows.sort((a, b) => score(b.traffic) - score(a.traffic) || peakBytes(b.traffic) - peakBytes(a.traffic))
}

export function rateLabel(bytesPerSecond: number): string {
  const bits = bytesPerSecond * 8
  if (bits >= 1e9) return `${(bits / 1e9).toFixed(1)} Gb/s`
  if (bits >= 1e6) return `${(bits / 1e6).toFixed(1)} Mb/s`
  return `${Math.round(bits / 1e3)} kb/s`
}

export function percentLabel(p: number | undefined): string {
  return p === undefined ? '—' : `${p} %`
}

/**
 * Host-network pods are left out: cAdvisor gives them the whole host's interfaces,
 * so each would report the node's traffic as its own (read on the cluster: a
 * node-exporter, a tetragon and a CSI plugin all "sending" the same 4.5 MB/s).
 */
const podRate = (metric: string) =>
  `topk(10, sum by (namespace, pod) (rate(${metric}[5m])) unless on (namespace, pod) kube_pod_info{host_network="true"})`

export const POD_TRAFFIC_QUERIES = {
  tx: podRate('container_network_transmit_bytes_total'),
  rx: podRate('container_network_receive_bytes_total'),
} as const

export interface PodTraffic {
  namespace: string
  pod: string
  bytesPerSecond: number
}

export function toPodTraffic(samples: { labels: Record<string, string>; value: number }[] | undefined): PodTraffic[] {
  return (samples ?? [])
    .filter((s) => Number.isFinite(s.value))
    .map((s) => ({ namespace: s.labels.namespace ?? '', pod: s.labels.pod ?? '', bytesPerSecond: s.value }))
    .sort((a, b) => b.bytesPerSecond - a.bytesPerSecond)
}
