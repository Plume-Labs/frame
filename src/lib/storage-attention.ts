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
import type { DiskRow, MdArray } from './disk-inventory'
import { duplicateSerials } from './disk-inventory'
import type { Machine } from './machines'
import { describeDivergence } from './storage'
import { localStackItems, nodesWithPublishedMd } from './local-storage'
import type { Message } from '@/i18n'

export type AttentionSeverity = 'critical' | 'warning' | 'info'

export interface AttentionItem {
  severity: AttentionSeverity
  /** Stable identifier of the rule that fired — what tests and keys use. */
  code: string
  /** A Message for the UI to render, or raw text read off the cluster (a Ceph health message, a pool's own trouble line). */
  text: string | Message
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
  /** status.cephPlacement.certificateNotAfter; required so the banner can't silently lose it. */
  certificateNotAfter: string | null
  now: number
  extra?: AttentionItem[]
}

const DAY_MS = 86_400_000
/** How long before the pinned dashboard certificate expires the banner says so. */
export const CERT_WARN_DAYS = 30

/**
 * "À traiter" line for the dashboard certificate pinned in the account
 * Secret: from 30 days before it expires, and once it has (every read then
 * fails on TLS). Renewal: docs/storage-mgr-account.md.
 */
export function certificateItems(notAfter: string | null | undefined, now: number): AttentionItem[] {
  const at = notAfter ? Date.parse(notAfter) : NaN
  if (!Number.isFinite(at)) return []
  const days = Math.floor((at - now) / DAY_MS)
  if (at <= now)
    return [{ severity: 'critical', code: 'dashboard-cert-expired', text: { key: 'sa.certExpired', params: { at: notAfter ?? '' } } }]
  if (days <= CERT_WARN_DAYS)
    return [{ severity: 'warning', code: 'dashboard-cert-expiring', text: { key: 'sa.certExpiring', params: { count: days, at: notAfter ?? '' } } }]
  return []
}

const RANK: Record<AttentionSeverity, number> = { critical: 0, warning: 1, info: 2 }

/** Flags that stop Ceph from healing itself; set on purpose during maintenance, forgotten after. */
const HEALING_FLAGS = new Set(['noout', 'norebalance', 'nobackfill', 'norecover', 'nodown', 'noup', 'noin'])

export function attentionItems(i: AttentionInputs): AttentionItem[] {
  const out: AttentionItem[] = [...certificateItems(i.certificateNotAfter, i.now)]

  if (!i.promAvailable) {
    out.push({
      severity: 'warning',
      code: 'prometheus-unavailable',
      text: { key: 'sa.promDown' },
    })
  }

  for (const o of i.osds) {
    const who = { name: o.name, where: o.host ? ` (${o.host})` : '' }
    if (o.state === 'down') out.push({ severity: 'critical', code: 'osd-down', text: { key: 'sa.osdDown', params: who } })
    else if (o.state === 'unstable')
      out.push({ severity: 'warning', code: 'osd-unstable', text: { key: 'sa.osdUnstable', params: { ...who, detail: o.stateDetail } } })
    else if (o.state === 'settling')
      out.push({ severity: 'info', code: 'osd-settling', text: { key: 'sa.osdSettling', params: { ...who, detail: o.stateDetail } } })
    else if (o.state === 'out') out.push({ severity: 'warning', code: 'osd-out', text: { key: 'sa.osdOut', params: who } })
    if (o.disk.kind === 'physical' && o.disk.hostConflict) {
      out.push({
        severity: 'warning',
        code: 'osd-disk-host-conflict',
        text: { key: 'sa.diskHostConflict', params: { name: o.name, serial: o.disk.serial, machine: o.disk.machine ?? '', host: o.host ?? '' } },
      })
    }
  }

  // An OSD the mgr says nothing about (pod seen, no up/in reading) is not a
  // healthy OSD: with Prometheus up but these series missing, an empty
  // banner would hide the absence of data.
  const unknown = i.osds.filter((o) => o.state === 'unknown')
  if (i.promAvailable && unknown.length > 0) {
    out.push({
      severity: 'warning',
      code: 'osd-unknown',
      text: { key: 'sa.osdUnknown', params: { count: unknown.length, range: osdRange(unknown.map((o) => o.id)) } },
    })
  }

  // An OSD that is up and in but holds no PG stores nothing: typically CRUSH
  // weight 0, or a rule that does not select its class/host yet.
  const empty = i.osds.filter((o) => o.up === true && o.in === true && o.pgs === 0)
  if (empty.length > 0) {
    const classes = [...new Set(empty.map((o) => o.deviceClass).filter(Boolean))].join(', ')
    const hosts = [...new Set(empty.map((o) => o.host).filter(Boolean))].join(', ')
    const ctx = [classes, hosts].filter(Boolean).join(', ')
    // With the CRUSH weights read (ceph-mgr dashboard), say which of the
    // two causes it is; without them, say both are possible.
    const weights = empty.map((o) => o.crushWeight)
    const why: Message = weights.every((w) => w === null)
      ? { key: 'sa.noPgWhyUnknown' }
      : weights.every((w) => w === 0)
        ? { key: 'sa.noPgWhyZero' }
        : weights.every((w) => w !== null && w > 0)
          ? { key: 'sa.noPgWhyRule', params: { weights: [...new Set(weights)].join(', ') } }
          : { key: 'sa.noPgWhyMixed', params: { weights: weights.map((w) => w ?? '?').join(', ') } }
    out.push({
      severity: 'warning',
      code: 'osd-no-pg',
      text: { key: 'sa.osdNoPg', params: { count: empty.length, range: osdRange(empty.map((o) => o.id)), ctx: ctx ? ` (${ctx})` : '', why } },
    })
  }

  if (i.flags.length > 0) {
    const healing = i.flags.some((f) => HEALING_FLAGS.has(f))
    out.push({
      severity: healing ? 'warning' : 'info',
      code: 'osd-flags',
      text: { key: 'sa.flags', params: { list: i.flags.join(', ') } },
    })
  }

  const slowCodes = Object.entries(i.healthDetails).filter(([code]) => /SLOW_OP/.test(code))
  if (slowCodes.length > 0) {
    const named = i.osds.filter((o) => (o.slowOps ?? 0) > 0 || (o.bluestoreSlow ?? 0) > 0)
    const who: Message = named.length
      ? { key: 'sa.slowNamed', params: { list: named.map((o) => o.name).join(', ') } }
      : { key: 'sa.slowUnattributed' }
    for (const [code, d] of slowCodes) {
      out.push({ severity: 'warning', code: 'slow-ops', text: { key: 'sa.slowOps', params: { message: d.message || code, who } } })
    }
  }

  for (const p of i.pools) {
    if (!p.trouble) continue
    out.push({ severity: p.trouble, code: 'pool-pg-trouble', text: { key: 'sa.poolTrouble', params: { name: p.name, detail: p.troubleDetail ?? '' } } })
  }

  if ((i.objects.degraded ?? 0) > 0) {
    const pct = i.objects.degradedPct !== null ? ` (${i.objects.degradedPct.toFixed(1)} %)` : ''
    out.push({ severity: 'warning', code: 'objects-degraded', text: { key: 'sa.objectsDegraded', params: { count: i.objects.degraded ?? 0, pct } } })
  }

  if (i.quorum.monsOut.length > 0) {
    out.push({ severity: 'warning', code: 'mon-out-of-quorum', text: { key: 'sa.monOut', params: { list: i.quorum.monsOut.join(', ') } } })
  }

  // A node whose agent publishes its md arrays is judged from that (below,
  // with member serials); node-exporter's view of it would say it twice.
  const publishedMd = nodesWithPublishedMd(i.machines)
  for (const a of i.mdArrays) {
    if (!a.degraded || publishedMd.has(a.node)) continue
    out.push({
      severity: 'critical',
      code: 'md-degraded',
      text: { key: 'sa.mdDegraded', params: { node: a.node, device: a.device, active: a.active, required: a.required !== null ? `/${a.required}` : '', failed: a.failed, state: a.state } },
    })
  }

  for (const m of i.machines) out.push(...localStackItems(m))

  for (const m of i.machines) {
    for (const d of m.storage?.divergences ?? []) {
      out.push({ severity: 'info', code: 'disk-divergence', text: { key: 'sa.divergence', params: { machine: m.name, what: describeDivergence(d) } } })
    }
  }

  for (const m of i.machines) {
    for (const d of m.inventory?.drives ?? []) {
      if (d.serialNumber) continue
      out.push({
        severity: 'info',
        code: 'disk-no-serial',
        text: { key: 'sa.noSerial', params: { machine: m.name, bay: d.location || '?' } },
      })
    }
  }
  for (const [serial, names] of duplicateSerials(i.machines)) {
    out.push({
      severity: 'warning',
      code: 'disk-serial-duplicate',
      text: { key: 'sa.serialDuplicate', params: { serial, list: names.join(', ') } },
    })
  }

  if (i.frameStorageCount === 0) {
    out.push({
      severity: 'info',
      code: 'no-framestorage',
      text: { key: 'sa.noFrameStorage' },
    })
  }

  out.push(...(i.extra ?? []))
  return out.sort((a, b) => RANK[a.severity] - RANK[b.severity])
}

/**
 * OSD disks whose WWN by-id path the CephCluster spec does not list. Only
 * rows with a verdict (`declaredInCeph === false`) fire; unknown stays quiet.
 */
export function undeclaredOsdDiskItems(machine: string, rows: DiskRow[]): AttentionItem[] {
  return rows
    .filter((r) => r.osd && r.declaredInCeph === false)
    .map((r) => ({
      severity: 'warning' as const,
      code: 'osd-disk-undeclared',
      text: { key: 'sa.osdUndeclared', params: { name: r.osd!.name, machine, bay: r.bay ?? '', serial: r.serial, path: r.byIdPath ?? '' } },
    }))
}

/** The banner when the diagnostic read itself failed: never an empty list. */
export function unreadableDiagnosticItems(message: string): AttentionItem[] {
  return [{ severity: 'warning', code: 'diagnostic-unreadable', text: { key: 'sa.diagUnreadable', params: { message } } }]
}
