// Pure decisions behind Storage → Volumes: one row per PersistentVolumeClaim,
// saying who uses it, how full it is, and which physical path every write to
// it takes — Ceph pool → CRUSH rule → OSDs → disks → nodes, or local-path →
// the node's own disk. Read-only: nothing here proposes an action.
//
// Same two rules as ceph-diagnostic.ts:
//
//  1. A value nobody collected is `null`, never 0. kubelet_volume_stats is not
//     scraped everywhere, and a PVC shown "0 % used" because its kubelet is not
//     scraped would hide the very volume that is about to fill up.
//  2. The pool → OSD set is read from what Rook declares (CephBlockPool
//     spec.deviceClass) and the OSDs Prometheus reports, never guessed from a
//     rule name. When the pool is not declared by a CephBlockPool, every OSD
//     is listed and the row says so.

import type { OsdRow, PromSample } from './ceph-diagnostic'
import type { OwnerRef } from './workloads'
import type { Tone } from './thresholds'
import { quantityBytes } from './storage'

/**
 * The instant queries this screen adds to the Ceph diagnostic, by key.
 *
 * `osdCommitWorst24h`: the worst 5-minute average of each OSD's commit latency
 * over the last 24 h. A subquery rather than a plain max_over_time so a single
 * scrape spike does not read as "the worst five minutes of the day".
 */
export const VOLUME_LOAD_QUERIES = {
  volUsed: 'kubelet_volume_stats_used_bytes',
  volCapacity: 'kubelet_volume_stats_capacity_bytes',
  osdCommitWorst24h: 'max_over_time(avg_over_time(ceph_osd_commit_latency_ms[5m])[24h:5m])',
} as const

export type VolumeLoadQueryKey = keyof typeof VOLUME_LOAD_QUERIES
export type VolumeLoadSnapshot = Partial<Record<VolumeLoadQueryKey, PromSample[]>>

/** Fill thresholds (percent of the volume's capacity). */
export const FILL_WARN_PCT = 85
export const FILL_CRITICAL_PCT = 95

/** Commit latency bands (ms) for an OSD on a write path. */
export const WRITE_OK_MS = 20
export const WRITE_SLOW_MS = 50

export const CEPH_RBD_DRIVERS = ['rook-ceph.rbd.csi.ceph.com', 'rbd.csi.ceph.com']
export const CEPH_FS_DRIVERS = ['rook-ceph.cephfs.csi.ceph.com', 'cephfs.csi.ceph.com']
export const LOCAL_PATH_PROVISIONERS = ['rancher.io/local-path', 'kubernetes.io/no-provisioner']

// ── inputs (what the SDK hands over, already flattened) ─────────────────────

export interface PvcInfo {
  namespace: string
  name: string
  phase: string
  storageClass: string | null
  volumeName: string | null
  /** status.capacity.storage in bytes, else spec.resources.requests.storage. */
  sizeBytes: number | null
}

export interface PvInfo {
  name: string
  storageClass: string | null
  reclaimPolicy: string | null
  csiDriver: string | null
  /** spec.csi.volumeAttributes.pool — the pool the volume was actually created in. */
  csiPool: string | null
  /** spec.local.path or spec.hostPath.path. */
  localPath: string | null
  /** The single node a node-affinity pins the volume to (kubernetes.io/hostname). */
  pinnedNode: string | null
}

export interface StorageClassInfo {
  name: string
  provisioner: string
  /** parameters.pool (ceph-csi RBD and CephFS). */
  pool: string | null
}

export interface BlockPoolInfo {
  /** Ceph's pool name: spec.name when set, else the CR's own name. */
  poolName: string
  deviceClass: string | null
  failureDomain: string
  size: number | null
}

export interface PodClaimInfo {
  namespace: string
  name: string
  node: string
  phase: string
  owner?: OwnerRef
  claims: string[]
}

export interface VolumeAttachmentInfo {
  pvName: string
  node: string
  attached: boolean
}

/** What `ClusterClient.volumeLoad()` reads; the OSDs and CRUSH rules come from the Ceph diagnostic. */
export interface VolumeLoadRaw {
  pvcs: PvcInfo[]
  pvs: PvInfo[]
  storageClasses: StorageClassInfo[]
  blockPools: BlockPoolInfo[]
  pods: PodClaimInfo[]
  replicaSetOwners: Map<string, OwnerRef>
  attachments: VolumeAttachmentInfo[]
  prom: VolumeLoadSnapshot | null
}

export interface VolumeLoadInputs {
  pvcs: PvcInfo[]
  pvs: PvInfo[]
  storageClasses: StorageClassInfo[]
  blockPools: BlockPoolInfo[]
  pods: PodClaimInfo[]
  replicaSetOwners: ReadonlyMap<string, OwnerRef>
  attachments: VolumeAttachmentInfo[]
  osds: OsdRow[]
  /** Pool name → CRUSH rule, from the ceph-mgr read (status.cephPlacement). */
  crushRules: ReadonlyMap<string, string | null>
  /** Null when Prometheus could not be read at all. */
  prom: VolumeLoadSnapshot | null
}

// ── outputs ─────────────────────────────────────────────────────────────────

export interface PathOsd {
  id: number
  host: string
  deviceClass: string
  commitMs: number | null
  worst24hMs: number | null
  /** "MODEL SERIAL" when the OSD was tied to a physical disk, else the mgr's device names. */
  disk: string
  /** On every write of this pool (the only OSD of a host the pool must place a copy on). */
  onEveryWrite: boolean
}

export type VolumePath =
  | {
      kind: 'ceph'
      flavour: 'rbd' | 'cephfs'
      pool: string
      crushRule: string | null
      /** Null: the pool is not declared by a CephBlockPool, so every OSD is listed. */
      deviceClass: string | null
      declared: boolean
      size: number | null
      osds: PathOsd[]
      hosts: string[]
    }
  | { kind: 'local'; node: string | null; path: string | null }
  | { kind: 'other'; driver: string }
  | { kind: 'unbound' }

export interface VolumeConsumer {
  /** "StatefulSet/neura-db", "Deployment/api", "Pod/foo" (bare pod). */
  workload: string
  pods: Array<{ name: string; node: string; phase: string }>
}

export interface VolumeRow {
  key: string
  namespace: string
  name: string
  phase: string
  storageClass: string | null
  reclaimPolicy: string | null
  consumers: VolumeConsumer[]
  /** Nodes the volume is used from: pods' nodes, else the CSI attachment's node. */
  nodes: string[]
  sizeBytes: number | null
  usedBytes: number | null
  usePct: number | null
  fill: 'ok' | 'warning' | 'critical' | null
  path: VolumePath
  /** The slowest OSD on the path right now, or null when none was measured. */
  likelyWrite: { osd: number; ms: number } | null
  /** The worst 5 minutes of any OSD on the path over 24 h (may be another OSD than likelyWrite). */
  worst24h: { osd: number; ms: number } | null
}

export interface SlowSharedOsd {
  osd: number
  host: string
  commitMs: number
  pools: string[]
  volumes: number
}

export interface VolumeFillFinding {
  key: string
  namespace: string
  name: string
  usePct: number
  severity: 'warning' | 'critical'
}

// ── helpers ─────────────────────────────────────────────────────────────────

function finite(v: number | undefined): number | null {
  return v === undefined || !Number.isFinite(v) ? null : v
}

/** namespace/pvc → value, from a kubelet_volume_stats vector. */
export function byClaim(samples: PromSample[] | undefined): Map<string, number> {
  const out = new Map<string, number>()
  for (const s of samples ?? []) {
    const ns = s.labels.namespace
    const pvc = s.labels.persistentvolumeclaim
    const v = finite(s.value)
    if (ns && pvc && v !== null) out.set(`${ns}/${pvc}`, v)
  }
  return out
}

/** OSD id → value, from a vector labelled ceph_daemon="osd.N". */
function byOsd(samples: PromSample[] | undefined): Map<number, number> {
  const out = new Map<number, number>()
  for (const s of samples ?? []) {
    const m = /^osd\.(\d+)$/.exec(s.labels.ceph_daemon ?? '')
    const v = finite(s.value)
    if (m && v !== null) out.set(Number(m[1]), v)
  }
  return out
}

export function fillLevel(pct: number | null): VolumeRow['fill'] {
  if (pct === null) return null
  if (pct >= FILL_CRITICAL_PCT) return 'critical'
  if (pct >= FILL_WARN_PCT) return 'warning'
  return 'ok'
}

export function writeTone(ms: number | null): Tone {
  if (ms === null) return 'muted'
  if (ms <= WRITE_OK_MS) return 'accent'
  if (ms <= WRITE_SLOW_MS) return 'warning'
  return 'destructive'
}

function diskLabel(o: OsdRow): string {
  if (o.disk.kind === 'physical') return [o.disk.model, o.disk.serial].filter(Boolean).join(' ')
  return o.devices.join(', ') || '—'
}

/**
 * The OSDs a pool places data on, and which of them every write waits for.
 *
 * A replicated pool with failure domain `host` puts each copy on a different
 * host. When it has exactly as many eligible hosts as copies, every placement
 * group has one copy on each of them — so an OSD that is the only eligible one
 * on its host takes part in every single write of the pool. That is the case
 * that makes one slow disk the floor of the whole cluster's write latency.
 * Only `up` and `in` OSDs are eligible: an `out` OSD holds no new copies.
 */
export function poolOsds(
  osds: OsdRow[],
  pool: { deviceClass: string | null; failureDomain: string; size: number | null },
  worst24h: ReadonlyMap<number, number>,
): { osds: PathOsd[]; hosts: string[] } {
  const eligible = osds.filter(
    (o) => (pool.deviceClass === null || o.deviceClass === pool.deviceClass) && o.up !== false && o.in !== false,
  )
  const perHost = new Map<string, number>()
  for (const o of eligible) perHost.set(o.host, (perHost.get(o.host) ?? 0) + 1)
  const hosts = [...perHost.keys()].sort()
  const everyHostHoldsACopy = pool.failureDomain === 'host' && pool.size !== null && pool.size === hosts.length
  return {
    hosts,
    osds: eligible
      .map((o) => ({
        id: o.id,
        host: o.host,
        deviceClass: o.deviceClass,
        commitMs: o.commitMs,
        worst24hMs: worst24h.get(o.id) ?? null,
        disk: diskLabel(o),
        onEveryWrite: everyHostHoldsACopy && perHost.get(o.host) === 1,
      }))
      .sort((a, b) => a.id - b.id),
  }
}

function slowest(osds: PathOsd[], pick: (o: PathOsd) => number | null): { osd: number; ms: number } | null {
  let best: { osd: number; ms: number } | null = null
  for (const o of osds) {
    const v = pick(o)
    if (v !== null && (best === null || v > best.ms)) best = { osd: o.id, ms: v }
  }
  return best
}

function workloadOf(pod: PodClaimInfo, rsOwners: ReadonlyMap<string, OwnerRef>): string {
  const o = pod.owner
  if (!o) return `Pod/${pod.name}`
  if (o.kind === 'ReplicaSet') {
    const parent = rsOwners.get(`${pod.namespace}/${o.name}`)
    return parent ? `${parent.kind}/${parent.name}` : `ReplicaSet/${o.name}`
  }
  return `${o.kind}/${o.name}`
}

function pathOf(
  pvc: PvcInfo,
  pv: PvInfo | undefined,
  sc: StorageClassInfo | undefined,
  inputs: VolumeLoadInputs,
  worst24h: ReadonlyMap<number, number>,
): VolumePath {
  const driver = pv?.csiDriver ?? sc?.provisioner ?? null
  if (!pv && !sc) return { kind: 'unbound' }
  if (driver && (CEPH_RBD_DRIVERS.includes(driver) || CEPH_FS_DRIVERS.includes(driver))) {
    const pool = pv?.csiPool ?? sc?.pool ?? ''
    const declared = inputs.blockPools.find((p) => p.poolName === pool)
    const spec = declared ?? { deviceClass: null, failureDomain: 'host', size: null }
    const { osds, hosts } = poolOsds(inputs.osds, spec, worst24h)
    return {
      kind: 'ceph',
      flavour: CEPH_RBD_DRIVERS.includes(driver) ? 'rbd' : 'cephfs',
      pool,
      crushRule: inputs.crushRules.get(pool) ?? null,
      deviceClass: declared?.deviceClass ?? null,
      declared: !!declared,
      size: declared?.size ?? null,
      osds,
      hosts,
    }
  }
  if ((driver && LOCAL_PATH_PROVISIONERS.includes(driver)) || pv?.localPath) {
    return { kind: 'local', node: pv?.pinnedNode ?? null, path: pv?.localPath ?? null }
  }
  if (!pv && pvc.phase !== 'Bound') return { kind: 'unbound' }
  return { kind: 'other', driver: driver ?? 'unknown' }
}

// ── builders ────────────────────────────────────────────────────────────────

export function buildVolumeRows(inputs: VolumeLoadInputs): VolumeRow[] {
  const used = byClaim(inputs.prom?.volUsed)
  const capacity = byClaim(inputs.prom?.volCapacity)
  const worst24h = byOsd(inputs.prom?.osdCommitWorst24h)
  const pvs = new Map(inputs.pvs.map((p) => [p.name, p]))
  const scs = new Map(inputs.storageClasses.map((s) => [s.name, s]))

  const consumers = new Map<string, Map<string, VolumeConsumer>>()
  for (const pod of inputs.pods) {
    for (const claim of pod.claims) {
      const key = `${pod.namespace}/${claim}`
      const byWorkload = consumers.get(key) ?? new Map<string, VolumeConsumer>()
      const w = workloadOf(pod, inputs.replicaSetOwners)
      const c = byWorkload.get(w) ?? { workload: w, pods: [] }
      c.pods.push({ name: pod.name, node: pod.node, phase: pod.phase })
      byWorkload.set(w, c)
      consumers.set(key, byWorkload)
    }
  }

  return inputs.pvcs
    .map((pvc): VolumeRow => {
      const key = `${pvc.namespace}/${pvc.name}`
      const pv = pvc.volumeName ? pvs.get(pvc.volumeName) : undefined
      const scName = pvc.storageClass ?? pv?.storageClass ?? null
      const sc = scName ? scs.get(scName) : undefined
      const path = pathOf(pvc, pv, sc, inputs, worst24h)
      const users = [...(consumers.get(key)?.values() ?? [])]
      const podNodes = users.flatMap((u) => u.pods.map((p) => p.node)).filter(Boolean)
      const attached = inputs.attachments.filter((a) => a.pvName === pvc.volumeName && a.attached).map((a) => a.node)
      const localNode = path.kind === 'local' && path.node ? [path.node] : []
      const nodes = [...new Set(podNodes.length ? podNodes : attached.length ? attached : localNode)].sort()

      const usedBytes = used.get(key) ?? null
      const capBytes = capacity.get(key) ?? null
      const usePct = usedBytes !== null && capBytes ? (usedBytes / capBytes) * 100 : null
      const osds = path.kind === 'ceph' ? path.osds : []
      const likely = slowest(osds, (o) => o.commitMs)
      return {
        key,
        namespace: pvc.namespace,
        name: pvc.name,
        phase: pvc.phase,
        storageClass: scName,
        reclaimPolicy: pv?.reclaimPolicy ?? null,
        consumers: users,
        nodes,
        sizeBytes: capBytes ?? pvc.sizeBytes,
        usedBytes,
        usePct,
        fill: fillLevel(usePct),
        path,
        likelyWrite: likely,
        worst24h: slowest(osds, (o) => o.worst24hMs),
      }
    })
    .sort((a, b) => (b.usePct ?? -1) - (a.usePct ?? -1) || a.key.localeCompare(b.key))
}

/**
 * OSDs every write of a pool waits for, that are slow right now. Sorted
 * slowest first. This is the banner: one disk setting the floor of every
 * write of every volume in those pools.
 */
export function slowSharedOsds(rows: VolumeRow[], slowMs: number = WRITE_SLOW_MS): SlowSharedOsd[] {
  const out = new Map<number, SlowSharedOsd>()
  for (const r of rows) {
    if (r.path.kind !== 'ceph') continue
    for (const o of r.path.osds) {
      if (!o.onEveryWrite || o.commitMs === null || o.commitMs < slowMs) continue
      const e = out.get(o.id) ?? { osd: o.id, host: o.host, commitMs: o.commitMs, pools: [], volumes: 0 }
      if (!e.pools.includes(r.path.pool)) e.pools.push(r.path.pool)
      e.volumes += 1
      out.set(o.id, e)
    }
  }
  return [...out.values()].map((e) => ({ ...e, pools: e.pools.sort() })).sort((a, b) => b.commitMs - a.commitMs)
}

/**
 * Volumes at or past the fill thresholds, worst first. Built from the kubelet
 * figures alone so the Overview can raise them without reading the PV graph.
 */
export function volumeFillFindings(prom: Pick<VolumeLoadSnapshot, 'volUsed' | 'volCapacity'> | null): VolumeFillFinding[] {
  const used = byClaim(prom?.volUsed)
  const capacity = byClaim(prom?.volCapacity)
  const out: VolumeFillFinding[] = []
  for (const [key, u] of used) {
    const cap = capacity.get(key)
    if (!cap) continue
    const pct = (u / cap) * 100
    const level = fillLevel(pct)
    if (level !== 'warning' && level !== 'critical') continue
    const [namespace, name] = key.split('/') as [string, string]
    out.push({ key, namespace, name, usePct: pct, severity: level })
  }
  return out.sort((a, b) => b.usePct - a.usePct)
}

/** One line for a volume's physical path, for the table and for tests. */
export function pathLine(p: VolumePath): string {
  switch (p.kind) {
    case 'ceph': {
      const cls = p.declared ? (p.deviceClass ?? 'any class') : 'pool not declared by a CephBlockPool: every OSD listed'
      return `ceph ${p.flavour} · pool ${p.pool || '?'} · rule ${p.crushRule ?? 'not collected'} · ${cls} · ${p.osds.length} OSD on ${p.hosts.length} host${p.hosts.length > 1 ? 's' : ''}`
    }
    case 'local':
      return `local disk of ${p.node ?? 'unknown node'}${p.path ? ` · ${p.path}` : ''}`
    case 'other':
      return `provisioner ${p.driver}`
    default:
      return 'not bound to a volume'
  }
}

// ── CR → input converters (the SDK's only job is to fetch and call these) ──

interface Meta {
  name: string
  namespace?: string
  ownerReferences?: Array<{ kind: string; name: string; controller?: boolean }>
}

export interface PvcCR {
  metadata: Meta
  spec?: { storageClassName?: string; volumeName?: string; resources?: { requests?: { storage?: string } } }
  status?: { phase?: string; capacity?: { storage?: string } }
}

export interface PvCR {
  metadata: Meta
  spec?: {
    storageClassName?: string
    persistentVolumeReclaimPolicy?: string
    csi?: { driver?: string; volumeAttributes?: Record<string, string> }
    local?: { path?: string }
    hostPath?: { path?: string }
    nodeAffinity?: {
      required?: { nodeSelectorTerms?: Array<{ matchExpressions?: Array<{ key: string; operator?: string; values?: string[] }> }> }
    }
  }
}

export interface StorageClassCR {
  metadata: Meta
  provisioner?: string
  parameters?: Record<string, string>
}

export interface BlockPoolCR {
  metadata: Meta
  spec?: { name?: string; deviceClass?: string; failureDomain?: string; replicated?: { size?: number } }
}

export interface PodVolumesCR {
  metadata: Meta
  spec?: { nodeName?: string; volumes?: Array<{ persistentVolumeClaim?: { claimName?: string } }> }
  status?: { phase?: string }
}

export interface VolumeAttachmentCR {
  spec?: { nodeName?: string; source?: { persistentVolumeName?: string } }
  status?: { attached?: boolean }
}

function controllerRef(meta: Meta): OwnerRef | undefined {
  const refs = meta.ownerReferences ?? []
  const o = refs.find((r) => r.controller) ?? refs[0]
  return o ? { kind: o.kind, name: o.name } : undefined
}

export function toPvcInfo(cr: PvcCR): PvcInfo {
  return {
    namespace: cr.metadata.namespace ?? '',
    name: cr.metadata.name,
    phase: cr.status?.phase ?? 'Unknown',
    storageClass: cr.spec?.storageClassName || null,
    volumeName: cr.spec?.volumeName || null,
    sizeBytes: quantityBytes(cr.status?.capacity?.storage) ?? quantityBytes(cr.spec?.resources?.requests?.storage),
  }
}

/** The one node a PV's required node affinity pins it to, else null (zero or several). */
function pinnedNode(cr: PvCR): string | null {
  const nodes = new Set<string>()
  for (const t of cr.spec?.nodeAffinity?.required?.nodeSelectorTerms ?? []) {
    for (const e of t.matchExpressions ?? []) {
      if (e.key === 'kubernetes.io/hostname' && (e.operator ?? 'In') === 'In') for (const v of e.values ?? []) nodes.add(v)
    }
  }
  return nodes.size === 1 ? [...nodes][0]! : null
}

export function toPvInfo(cr: PvCR): PvInfo {
  return {
    name: cr.metadata.name,
    storageClass: cr.spec?.storageClassName || null,
    reclaimPolicy: cr.spec?.persistentVolumeReclaimPolicy || null,
    csiDriver: cr.spec?.csi?.driver || null,
    csiPool: cr.spec?.csi?.volumeAttributes?.pool || null,
    localPath: cr.spec?.local?.path || cr.spec?.hostPath?.path || null,
    pinnedNode: pinnedNode(cr),
  }
}

export function toStorageClassInfo(cr: StorageClassCR): StorageClassInfo {
  return { name: cr.metadata.name, provisioner: cr.provisioner ?? '', pool: cr.parameters?.pool || null }
}

export function toBlockPoolInfo(cr: BlockPoolCR): BlockPoolInfo {
  return {
    poolName: cr.spec?.name || cr.metadata.name,
    deviceClass: cr.spec?.deviceClass || null,
    // Rook's own default when the field is left out.
    failureDomain: cr.spec?.failureDomain || 'host',
    size: cr.spec?.replicated?.size ?? null,
  }
}

export function toPodClaimInfo(cr: PodVolumesCR): PodClaimInfo | null {
  const claims = (cr.spec?.volumes ?? []).map((v) => v.persistentVolumeClaim?.claimName).filter((c): c is string => !!c)
  if (claims.length === 0) return null
  return {
    namespace: cr.metadata.namespace ?? '',
    name: cr.metadata.name,
    node: cr.spec?.nodeName ?? '',
    phase: cr.status?.phase ?? 'Unknown',
    owner: controllerRef(cr.metadata),
    claims,
  }
}

export function toAttachmentInfo(cr: VolumeAttachmentCR): VolumeAttachmentInfo | null {
  const pv = cr.spec?.source?.persistentVolumeName
  if (!pv || !cr.spec?.nodeName) return null
  return { pvName: pv, node: cr.spec.nodeName, attached: cr.status?.attached === true }
}
