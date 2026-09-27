// Pools, placement groups and capacity per device class, read from the
// ceph-mgr exporter. Pure, like ceph-diagnostic.ts, which it complements.
//
// The Rook CRs only know the pools Rook created as CephBlockPool: on the
// cluster this was written against that is one pool out of ten — the RGW
// pools of a CephObjectStore and `.mgr` do not appear. `ceph_pool_metadata`
// lists them all, so the mgr is the source of the pool list; the CRs are not.

import type { PromSample, PromSnapshot } from './ceph-diagnostic'

/**
 * PG states whose presence in a pool means data is at risk or unavailable,
 * by severity. A PG carries several states at once (`active+undersized+degraded`),
 * so these counts overlap and are never summed into a total.
 */
export const PG_CRITICAL_STATES = ['down', 'incomplete', 'inconsistent', 'stale', 'unknown', 'backfill_toofull', 'recovery_toofull', 'recovery_unfound', 'backfill_unfound'] as const
export const PG_WARNING_STATES = ['degraded', 'undersized', 'peering', 'remapped', 'backfilling', 'backfill_wait', 'recovering', 'recovery_wait'] as const

/** The order the PG line reads in: health first, then the work in progress. */
export const PG_DISPLAY_STATES = [
  'active', 'clean', 'degraded', 'undersized', 'remapped', 'backfilling', 'backfill_wait',
  'recovering', 'recovery_wait', 'peering', 'down', 'incomplete', 'inconsistent', 'stale', 'unknown',
  'scrubbing', 'deep',
] as const

export interface PgTally {
  total: number
  byState: Record<string, number>
}

export interface PoolRow {
  id: string
  name: string
  application: string
  type: string
  /** Replica count read from the mgr's pool description (`replica:4`); null for EC or unreadable. */
  size: number | null
  /** `k+m` of an erasure-coded pool. */
  erasure: string | null
  /** Not exported by the mgr; null until its REST API is read. */
  minSize: number | null
  /** Not exported by the mgr; null until its REST API is read. */
  crushRule: string | null
  pgs: PgTally
  storedBytes: number | null
  maxAvailBytes: number | null
  percentUsed: number | null
  objects: number | null
  /** Worst PG trouble in the pool, for the table and the banner. */
  trouble: 'critical' | 'warning' | null
  troubleDetail: string
}

function pgStateName(metric: string): string | null {
  const m = /^ceph_pg_(.+)$/.exec(metric)
  return m ? m[1] : null
}

/** PG counts per pool id and cluster-wide, from `{__name__=~"ceph_pg_.+"}`. */
export function tallyPgs(samples: PromSample[] | undefined): { cluster: PgTally; byPool: Map<string, PgTally> } {
  const byPool = new Map<string, PgTally>()
  const cluster: PgTally = { total: 0, byState: {} }
  for (const s of samples ?? []) {
    const state = pgStateName(s.labels.__name__ ?? '')
    const pool = s.labels.pool_id
    if (!state || pool === undefined || !Number.isFinite(s.value)) continue
    const t = byPool.get(pool) ?? { total: 0, byState: {} }
    if (state === 'total') {
      t.total += s.value
      cluster.total += s.value
    } else {
      t.byState[state] = (t.byState[state] ?? 0) + s.value
      cluster.byState[state] = (cluster.byState[state] ?? 0) + s.value
    }
    byPool.set(pool, t)
  }
  return { cluster, byPool }
}

/** The worst PG trouble in a tally, and the states behind it in words. */
export function pgTrouble(t: PgTally): { trouble: 'critical' | 'warning' | null; detail: string } {
  const hit = (states: readonly string[]) =>
    states.filter((s) => (t.byState[s] ?? 0) > 0).map((s) => `${t.byState[s]} ${s}`)
  const crit = hit(PG_CRITICAL_STATES)
  const warn = hit(PG_WARNING_STATES)
  // A PG that is not active serves no I/O at all, whatever else it is.
  const inactive = t.total - (t.byState.active ?? t.total)
  if (inactive > 0) crit.unshift(`${inactive} not active`)
  if (crit.length) return { trouble: 'critical', detail: [...crit, ...warn].join(', ') }
  if (warn.length) return { trouble: 'warning', detail: warn.join(', ') }
  return { trouble: null, detail: '' }
}

function parseDescription(desc: string | undefined): { size: number | null; erasure: string | null } {
  const rep = /replica:(\d+)/.exec(desc ?? '')
  if (rep) return { size: Number(rep[1]), erasure: null }
  const ec = /ec:(\d+\+\d+)/.exec(desc ?? '')
  return { size: null, erasure: ec ? ec[1] : null }
}

function byPoolId(samples: PromSample[] | undefined): Map<string, number> {
  const m = new Map<string, number>()
  for (const s of samples ?? []) {
    if (s.labels.pool_id !== undefined && Number.isFinite(s.value)) m.set(s.labels.pool_id, s.value)
  }
  return m
}

/**
 * `placement` (pool name → min_size, CRUSH rule) comes from the ceph-mgr
 * dashboard through the FrameStorage status; a pool it does not cover keeps
 * null for both.
 */
export function buildPoolRows(
  prom: PromSnapshot,
  placement: Map<string, { minSize: number | null; crushRule: string | null }> = new Map(),
): PoolRow[] {
  const { byPool } = tallyPgs(prom.pgStates)
  const stored = byPoolId(prom.poolStored)
  const maxAvail = byPoolId(prom.poolMaxAvail)
  const pct = byPoolId(prom.poolPercent)
  const objects = byPoolId(prom.poolObjects)
  return (prom.poolMeta ?? [])
    .map((m): PoolRow => {
      const id = m.labels.pool_id ?? ''
      const pgs = byPool.get(id) ?? { total: 0, byState: {} }
      const { trouble, detail } = pgTrouble(pgs)
      const p = pct.get(id)
      return {
        id,
        name: m.labels.name ?? `pool ${id}`,
        application: m.labels.application ?? '',
        type: m.labels.type ?? '',
        ...parseDescription(m.labels.description),
        minSize: placement.get(m.labels.name ?? '')?.minSize ?? null,
        crushRule: placement.get(m.labels.name ?? '')?.crushRule ?? null,
        pgs,
        storedBytes: stored.get(id) ?? null,
        maxAvailBytes: maxAvail.get(id) ?? null,
        // ceph_pool_percent_used is a ratio (0–1) despite its name.
        percentUsed: p === undefined ? null : p * 100,
        objects: objects.get(id) ?? null,
        trouble,
        troubleDetail: detail,
      }
    })
    .sort((a, b) => Number(a.id) - Number(b.id))
}

export interface ClassCapacity {
  deviceClass: string
  totalBytes: number
  usedBytes: number
  osds: number[]
  hosts: string[]
}

export function capacityByClass(
  prom: PromSnapshot,
  osds: Array<{ id: number; deviceClass: string; host: string }>,
): ClassCapacity[] {
  const used = new Map((prom.classUsed ?? []).map((s) => [s.labels.device_class ?? '', s.value]))
  return (prom.classTotal ?? [])
    .map((s) => {
      const cls = s.labels.device_class ?? ''
      const members = osds.filter((o) => o.deviceClass === cls)
      return {
        deviceClass: cls,
        totalBytes: s.value,
        usedBytes: used.get(cls) ?? 0,
        osds: members.map((o) => o.id),
        hosts: [...new Set(members.map((o) => o.host).filter(Boolean))].sort(),
      }
    })
    .sort((a, b) => a.deviceClass.localeCompare(b.deviceClass))
}

/** Names of the OSD map flags currently set (`ceph_osd_flag_noout` = 1 → "noout"). */
export function setFlags(samples: PromSample[] | undefined): string[] {
  return (samples ?? [])
    .filter((s) => s.value === 1)
    .map((s) => (s.labels.__name__ ?? '').replace(/^ceph_osd_flag_/, ''))
    .filter(Boolean)
    .sort()
}

export interface ObjectHealth {
  degraded: number | null
  misplaced: number | null
  /** Object copies the cluster should hold: Σ objects × size over replicated pools. */
  copies: number | null
  degradedPct: number | null
  recoveryBytesPerSec: number | null
}

export function objectHealth(prom: PromSnapshot, pools: PoolRow[]): ObjectHealth {
  const one = (s: PromSample[] | undefined) =>
    s && s.length > 0 && Number.isFinite(s[0].value) ? s[0].value : null
  const degraded = one(prom.objectsDegraded)
  let copies: number | null = 0
  for (const p of pools) {
    if (p.objects === null || p.size === null) {
      copies = null
      break
    }
    copies += p.objects * p.size
  }
  return {
    degraded,
    misplaced: one(prom.objectsMisplaced),
    copies,
    degradedPct: degraded !== null && copies ? (100 * degraded) / copies : null,
    recoveryBytesPerSec: one(prom.recoveryBytes),
  }
}

export interface DaemonQuorum {
  monsInQuorum: string[]
  monsOut: string[]
  mgrActive: string[]
  mgrStandby: string[]
}

export function daemonQuorum(prom: PromSnapshot): DaemonQuorum {
  const name = (s: PromSample) => (s.labels.ceph_daemon ?? '').replace(/^(mon|mgr)\./, '')
  const mons = prom.monQuorum ?? []
  const mgrs = prom.mgrStatus ?? []
  return {
    monsInQuorum: mons.filter((s) => s.value === 1).map(name).sort(),
    monsOut: mons.filter((s) => s.value !== 1).map(name).sort(),
    mgrActive: mgrs.filter((s) => s.value === 1).map(name).sort(),
    mgrStandby: mgrs.filter((s) => s.value !== 1).map(name).sort(),
  }
}
