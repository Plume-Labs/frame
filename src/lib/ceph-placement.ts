// What the ceph-mgr dashboard adds to the Storage screen, through the
// FrameStorage status the controller writes (status.cephPlacement): each
// pool's min_size and CRUSH rule, each OSD's CRUSH weight. Pure.
//
// Absent data stays absent: an OSD the read did not cover keeps a null
// weight (shown "—"), never 0 — a CRUSH weight of 0 is a real and alarming
// value (an OSD that takes no data), not a placeholder.

import type { StorageCapacity } from './storage'

export interface CephPlacement {
  state: 'Collected' | 'NotConfigured' | 'Error' | 'AuthRefused'
  message?: string
  collectedAt?: string
  poolsReadAt?: string
  osdsReadAt?: string
  rulesReadAt?: string
  /** Expiry of the certificate pinned in the dashboard account Secret. */
  certificateNotAfter?: string
  pools?: Array<{ name: string; size?: number; minSize?: number; crushRule?: string }>
  osds?: Array<{ id: number; crushWeight: string; deviceClass?: string }>
}

interface EntryLike {
  type: string
  capacity: StorageCapacity
  cephPlacement?: CephPlacement | null
}

/**
 * The ceph-* entry whose figures the Storage screen shows: the first one
 * with a usable capacity, else the first ceph-* entry at all. With none,
 * the screen falls back to raw and says usable is unknown.
 */
export function cephCapacityEntry<T extends EntryLike>(entries: T[]): T | undefined {
  return entries.find((e) => e.type.startsWith('ceph-') && e.capacity.usable) ?? entries.find((e) => e.type.startsWith('ceph-'))
}

/** The placement read carried by the ceph-* entries (they all carry the same cluster-wide read). */
export function placementOf(entries: EntryLike[]): CephPlacement | null {
  const withRead = entries.filter((e) => e.type.startsWith('ceph-') && e.cephPlacement)
  return withRead.find((e) => e.cephPlacement!.state === 'Collected')?.cephPlacement ?? withRead[0]?.cephPlacement ?? null
}

/** OSD id → CRUSH weight, only for OSDs the read covered. */
export function crushWeights(p: CephPlacement | null): Map<number, number> {
  const out = new Map<number, number>()
  for (const o of p?.osds ?? []) {
    const w = Number(o.crushWeight)
    if (Number.isFinite(w)) out.set(o.id, w)
  }
  return out
}

/** Pool name → min_size and CRUSH rule. */
export function poolPlacement(p: CephPlacement | null): Map<string, { minSize: number | null; crushRule: string | null }> {
  const out = new Map<string, { minSize: number | null; crushRule: string | null }>()
  for (const pool of p?.pools ?? []) out.set(pool.name, { minSize: pool.minSize ?? null, crushRule: pool.crushRule || null })
  return out
}

/** One line saying where the placement figures come from, or why they are missing. */
export function placementSourceLine(p: CephPlacement | null): string {
  if (!p) return 'min_size, CRUSH rule and CRUSH weight: not collected (no ceph FrameStorage entry)'
  switch (p.state) {
    case 'Collected':
      return `min_size, CRUSH rule and CRUSH weight: ceph-mgr dashboard, read ${p.collectedAt ?? '?'}`
    case 'NotConfigured':
      return 'min_size, CRUSH rule and CRUSH weight: not collected (no read-only dashboard account configured)'
    case 'AuthRefused':
      return `min_size, CRUSH rule and CRUSH weight: the dashboard refused the account — no retry until the account in the Secret changes${p.collectedAt ? `; showing the read of ${p.collectedAt}` : ''}`
    default: {
      const dates = [
        p.poolsReadAt ? `pools ${p.poolsReadAt}` : '',
        p.osdsReadAt ? `OSDs ${p.osdsReadAt}` : '',
        p.rulesReadAt ? `rules ${p.rulesReadAt}` : '',
      ].filter(Boolean)
      return `min_size, CRUSH rule and CRUSH weight: last read failed — ${p.message || 'no reason'}${dates.length ? `; lists as read: ${dates.join(', ')}` : p.collectedAt ? `; showing the read of ${p.collectedAt}` : ''}`
    }
  }
}
