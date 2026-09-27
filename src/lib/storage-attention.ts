// The "À traiter" banner at the top of the Storage screen: every finding an
// operator should act on, worst first, each naming the OSD, pool, disk or
// array it is about. Pure.
//
// The banner never goes quiet by omission: when a source is missing
// (Prometheus unreachable), it says so as a finding of its own rather than
// render an empty — and therefore reassuring — list.

import type { OsdRow } from './ceph-diagnostic'
import { osdRange } from './ceph-diagnostic'
import type { PoolRow, DaemonQuorum, ObjectHealth } from './ceph-pools'
import type { MdArray } from './disk-inventory'
import type { Machine } from './machines'
import { describeDivergence } from './storage'

export type AttentionSeverity = 'critical' | 'warning' | 'info'

export interface AttentionItem {
  severity: AttentionSeverity
  /** Stable identifier of the rule that fired — what tests and keys use. */
  code: string
  text: string
}

export interface AttentionInputs {
  /** False when Prometheus could not be read: OSD/pool detail is then unknown, not fine. */
  promAvailable: boolean
  osds: OsdRow[]
  pools: PoolRow[]
  flags: string[]
  /** `status.ceph.details` codes off the CephCluster CR, with their messages. */
  healthDetails: Record<string, { message?: string; severity?: string }>
  quorum: DaemonQuorum
  objects: ObjectHealth
  mdArrays: MdArray[]
  machines: Machine[]
  frameStorageCount: number | null
  extra?: AttentionItem[]
}

const RANK: Record<AttentionSeverity, number> = { critical: 0, warning: 1, info: 2 }

/** Flags that stop Ceph from healing itself; set on purpose during maintenance, forgotten after. */
const HEALING_FLAGS = new Set(['noout', 'norebalance', 'nobackfill', 'norecover', 'nodown', 'noup', 'noin'])

export function attentionItems(i: AttentionInputs): AttentionItem[] {
  const out: AttentionItem[] = []

  if (!i.promAvailable) {
    out.push({
      severity: 'warning',
      code: 'prometheus-unavailable',
      text: 'Prometheus unreachable: OSD, PG and pool detail unknown (not healthy — unknown)',
    })
  }

  for (const o of i.osds) {
    const where = o.host ? ` (${o.host})` : ''
    if (o.state === 'down') out.push({ severity: 'critical', code: 'osd-down', text: `${o.name}${where} down` })
    else if (o.state === 'unstable')
      out.push({ severity: 'warning', code: 'osd-unstable', text: `${o.name}${where} unstable — ${o.stateDetail}` })
    else if (o.state === 'out') out.push({ severity: 'warning', code: 'osd-out', text: `${o.name}${where} out` })
    if (o.disk.kind === 'physical' && o.disk.hostConflict) {
      out.push({
        severity: 'warning',
        code: 'osd-disk-host-conflict',
        text: `${o.name}: serial ${o.disk.serial} is listed by ${o.disk.machine}, which is not node ${o.host}`,
      })
    }
  }

  // An OSD that is up and in but holds no PG stores nothing: typically CRUSH
  // weight 0, or a rule that does not select its class/host yet.
  const empty = i.osds.filter((o) => o.up === true && o.in === true && o.pgs === 0)
  if (empty.length > 0) {
    const classes = [...new Set(empty.map((o) => o.deviceClass).filter(Boolean))].join(', ')
    const hosts = [...new Set(empty.map((o) => o.host).filter(Boolean))].join(', ')
    const ctx = [classes, hosts].filter(Boolean).join(', ')
    out.push({
      severity: 'warning',
      code: 'osd-no-pg',
      text: `${empty.length} OSD without any PG: ${osdRange(empty.map((o) => o.id))}${ctx ? ` (${ctx})` : ''} — CRUSH weight 0 or not selected by any rule? (weight not collected)`,
    })
  }

  if (i.flags.length > 0) {
    const healing = i.flags.some((f) => HEALING_FLAGS.has(f))
    out.push({
      severity: healing ? 'warning' : 'info',
      code: 'osd-flags',
      text: `Flags set: ${i.flags.join(', ')}`,
    })
  }

  const slowCodes = Object.entries(i.healthDetails).filter(([code]) => /SLOW_OP/.test(code))
  if (slowCodes.length > 0) {
    const named = i.osds.filter((o) => (o.slowOps ?? 0) > 0 || (o.bluestoreSlow ?? 0) > 0)
    const who = named.length
      ? `→ ${named.map((o) => o.name).join(', ')} (slow-op counters > 0)`
      : '→ OSD not attributed (counters at 0; mgr API not read)'
    for (const [code, d] of slowCodes) {
      out.push({ severity: 'warning', code: 'slow-ops', text: `${d.message || code} ${who}` })
    }
  }

  for (const p of i.pools) {
    if (!p.trouble) continue
    out.push({ severity: p.trouble, code: 'pool-pg-trouble', text: `Pool ${p.name}: ${p.troubleDetail}` })
  }

  if ((i.objects.degraded ?? 0) > 0) {
    const pct = i.objects.degradedPct !== null ? ` (${i.objects.degradedPct.toFixed(1)} %)` : ''
    out.push({ severity: 'warning', code: 'objects-degraded', text: `${i.objects.degraded} degraded object copies${pct}` })
  }

  if (i.quorum.monsOut.length > 0) {
    out.push({ severity: 'warning', code: 'mon-out-of-quorum', text: `Monitor(s) out of quorum: ${i.quorum.monsOut.join(', ')}` })
  }

  for (const a of i.mdArrays) {
    if (!a.degraded) continue
    out.push({
      severity: 'critical',
      code: 'md-degraded',
      text: `${a.node} ${a.device}: ${a.active} active${a.required !== null ? `/${a.required}` : ''}, ${a.failed} failed, state ${a.state}`,
    })
  }

  for (const m of i.machines) {
    for (const d of m.storage?.divergences ?? []) {
      out.push({ severity: 'info', code: 'disk-divergence', text: `${m.name}: ${describeDivergence(d)}` })
    }
  }

  if (i.frameStorageCount === 0) {
    out.push({
      severity: 'info',
      code: 'no-framestorage',
      text: 'Usable capacity unknown: no FrameStorage entry declared',
    })
  }

  out.push(...(i.extra ?? []))
  return out.sort((a, b) => RANK[a.severity] - RANK[b.severity])
}
