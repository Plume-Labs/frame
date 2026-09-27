// Pure decisions behind the diagnostic half of the Storage screen: one row
// per OSD, one per pool, the PG state tally and the "À traiter" banner. No
// network, no DOM — the SDK hands this file what Prometheus (ceph-mgr,
// ceph-exporter, node-exporter), the Rook CRs and the OSD pods said, and
// every judgement an operator reads is made here, where vitest reaches it.
//
// Two rules hold throughout:
//
//  1. A value nobody collected is `null`, never 0. Ceph's CRUSH weight is not
//     in Prometheus (`ceph_osd_weight` is the reweight, always 1 here), and a
//     column of zeros would read as "no weight" — the very failure (four HDD
//     OSDs at weight 0, holding no PG) this screen exists to show.
//  2. An OSD is tied to a disk by serial number, never by /dev/sdX. The sdX
//     order changes across boots, and the mgr and Rook already disagreed on
//     it for one OSD on the cluster this was written against (Rook's
//     ROOK_BLOCK_PATH is frozen at preparation). The serial comes from the
//     mgr's own `device_ids` label, `<name>=<VENDOR>_<MODEL>_<SERIAL>`.

import type { Machine } from './machines'

/** One sample of an instant PromQL vector. */
export interface PromSample {
  labels: Record<string, string>
  value: number
}

/**
 * The instant queries the diagnostic reads, by key. One place, so the SDK
 * that sends them and the builders that read them cannot drift apart.
 */
export const CEPH_DIAG_QUERIES = {
  osdMeta: 'ceph_osd_metadata',
  osdUp: 'ceph_osd_up',
  osdIn: 'ceph_osd_in',
  osdPgs: 'ceph_osd_numpg',
  osdBytes: 'ceph_osd_stat_bytes',
  osdUsed: 'ceph_osd_stat_bytes_used',
  osdApply: 'ceph_osd_apply_latency_ms',
  osdCommit: 'ceph_osd_commit_latency_ms',
  osdDisk: 'ceph_disk_occupation',
  slowOps: 'ceph_daemon_health_metrics{type="SLOW_OPS"}',
  // BlueStore's own slow-operation counters, cumulative since the OSD
  // started. They are what attributes a BLUESTORE_SLOW_OP_ALERT, whose
  // health message only gives a count ("1 OSD(s) experiencing…").
  bluestoreSlow:
    'sum by (ceph_daemon) (ceph_bluestore_slow_aio_wait_count + ceph_bluestore_slow_committed_kv_count + ceph_bluestore_slow_read_onode_meta_count + ceph_bluestore_slow_read_wait_aio_count)',
  flags: '{__name__=~"ceph_osd_flag_.+"}',
  pgStates: '{__name__=~"ceph_pg_.+"}',
  objectsDegraded: 'ceph_num_objects_degraded',
  objectsMisplaced: 'ceph_num_objects_misplaced',
  recoveryBytes: 'sum(ceph_pool_recovering_bytes_per_sec)',
  poolMeta: 'ceph_pool_metadata',
  poolStored: 'ceph_pool_stored',
  poolMaxAvail: 'ceph_pool_max_avail',
  poolPercent: 'ceph_pool_percent_used',
  poolObjects: 'ceph_pool_objects',
  monQuorum: 'ceph_mon_quorum_status',
  mgrStatus: 'ceph_mgr_status',
  classTotal: 'ceph_cluster_by_class_total_bytes',
  classUsed: 'ceph_cluster_by_class_total_used_bytes',
  diskUtil:
    '100 * rate(node_disk_io_time_seconds_total[5m]) * on(instance) group_left(nodename) node_uname_info',
  diskAwait:
    '1000 * (rate(node_disk_read_time_seconds_total[5m]) + rate(node_disk_write_time_seconds_total[5m])) / clamp_min(rate(node_disk_reads_completed_total[5m]) + rate(node_disk_writes_completed_total[5m]), 1e-9) * on(instance) group_left(nodename) node_uname_info',
  mdDisks: 'node_md_disks * on(instance) group_left(nodename) node_uname_info',
  mdRequired: 'node_md_disks_required * on(instance) group_left(nodename) node_uname_info',
  mdState: 'node_md_state * on(instance) group_left(nodename) node_uname_info',
} as const

export type CephDiagQueryKey = keyof typeof CEPH_DIAG_QUERIES
export type PromSnapshot = Partial<Record<CephDiagQueryKey, PromSample[]>>

/**
 * A snapshot in which not a single query answered is no snapshot: the SDK
 * turns it into null, "Prometheus unreachable". One answered query (even an
 * empty vector) is enough to say Prometheus is up; the missing keys then read
 * as unknown in the builders.
 */
export function promAnswered<T extends object>(snapshot: T): T | null {
  return Object.keys(snapshot).length === 0 ? null : snapshot
}

/** What the SDK reads off one rook-ceph-osd pod — enough to say "unstable". */
export interface OsdPod {
  osdId: number
  name: string
  node: string
  phase: string
  ready: boolean
  restarts: number
  initDone: number
  initTotal: number
  /** Why the pod is not running yet: the init container it waits on, or a waiting reason. */
  waiting: string
  startedAt: string | null
  lastTerminationAt: string | null
  createdAt: string
}

/** The pod shape `toOsdPod` reads. Every field optional: a pod mid-creation has almost none. */
export interface OsdPodCR {
  metadata: { name: string; labels?: Record<string, string>; creationTimestamp?: string }
  spec?: { nodeName?: string }
  status?: {
    phase?: string
    initContainerStatuses?: ContainerStatusCR[]
    containerStatuses?: ContainerStatusCR[]
  }
}

interface ContainerStatusCR {
  name: string
  ready?: boolean
  restartCount?: number
  state?: {
    running?: { startedAt?: string }
    waiting?: { reason?: string }
    terminated?: { reason?: string; finishedAt?: string }
  }
  lastState?: { terminated?: { finishedAt?: string } }
}

export function toOsdPod(p: OsdPodCR): OsdPod | null {
  const id = Number(p.metadata.labels?.['ceph-osd-id'])
  if (!Number.isInteger(id)) return null
  const inits = p.status?.initContainerStatuses ?? []
  const mains = p.status?.containerStatuses ?? []
  const initDone = inits.filter((c) => c.state?.terminated?.reason === 'Completed').length
  const pendingInit = inits.find((c) => !c.state?.terminated)
  const waitingMain = mains.find((c) => c.state?.waiting?.reason)
  // The last restart is the last time a main container ended
  // (containerStatuses[].lastState.terminated.finishedAt); init containers
  // run once per pod start and say nothing a restart count does not.
  const lastTerminations = mains
    .map((c) => c.lastState?.terminated?.finishedAt)
    .filter((t): t is string => !!t)
    .sort()
  return {
    osdId: id,
    name: p.metadata.name,
    node: p.spec?.nodeName ?? '',
    phase: p.status?.phase ?? 'Unknown',
    ready: mains.length > 0 && mains.every((c) => c.ready === true),
    restarts: mains.reduce((n, c) => n + (c.restartCount ?? 0), 0),
    initDone,
    initTotal: inits.length,
    waiting: pendingInit
      ? `init ${pendingInit.name}`
      : (waitingMain?.state?.waiting?.reason ?? ''),
    startedAt: mains.find((c) => c.state?.running?.startedAt)?.state?.running?.startedAt ?? null,
    lastTerminationAt: lastTerminations[lastTerminations.length - 1] ?? null,
    createdAt: p.metadata.creationTimestamp ?? '',
  }
}

/**
 * The pod that stands for an OSD now. Rook leaves Completed pods of older
 * ReplicaSets behind (seen: three per OSD); counting or reading those would
 * report a healthy OSD as stopped. A non-terminal pod wins, newest first.
 */
export function currentOsdPods(pods: OsdPod[]): Map<number, OsdPod> {
  const out = new Map<number, OsdPod>()
  const live = (p: OsdPod) => p.phase !== 'Succeeded' && p.phase !== 'Failed'
  for (const p of pods) {
    const prev = out.get(p.osdId)
    if (!prev) {
      out.set(p.osdId, p)
      continue
    }
    if (live(p) !== live(prev)) {
      if (live(p)) out.set(p.osdId, p)
      continue
    }
    if (p.createdAt > prev.createdAt) out.set(p.osdId, p)
  }
  return out
}

/** How an OSD is tied to storage underneath it. */
export type OsdDiskLink =
  | {
      kind: 'physical'
      serial: string
      /** Machine whose BMC lists this serial; null when no FrameMachine does. */
      machine: string | null
      bay: string | null
      model: string | null
      /** Where the node agent sees that serial now (same boot as the mgr's sdX or not). */
      osPath: string | null
      /** The machine found by serial is not the OSD's node: never silently accepted. */
      hostConflict: boolean
      /** Every machine listing this serial, when more than one does (else empty). */
      duplicateOn: string[]
    }
  | { kind: 'virtual'; serial: string; drive: string }
  | { kind: 'unknown'; reason: string }

/**
 * `settling`: up, in, pod running and ready, but restarted less than the
 * stability window ago — watched, not alarming. Past the window the restart
 * is forgotten and the OSD is `up-in` again.
 */
export type OsdState = 'up-in' | 'down' | 'out' | 'unstable' | 'settling' | 'unknown'

export interface OsdRow {
  id: number
  name: string
  deviceClass: string
  host: string
  up: boolean | null
  in: boolean | null
  pgs: number | null
  bytesTotal: number | null
  bytesUsed: number | null
  usePct: number | null
  applyMs: number | null
  commitMs: number | null
  /** Ops blocked right now (ceph_daemon_health_metrics SLOW_OPS). */
  slowOps: number | null
  /** BlueStore slow-operation counters since the OSD started. */
  bluestoreSlow: number | null
  /** Not in Prometheus (see file header): null until the mgr API is read. */
  crushWeight: number | null
  /** Kernel names from the mgr, for display and I/O lookup only — never the join key. */
  devices: string[]
  deviceIds: string[]
  pod: OsdPod | null
  state: OsdState
  stateDetail: string
  disk: OsdDiskLink
}

function byDaemon(samples: PromSample[] | undefined): Map<string, PromSample> {
  const m = new Map<string, PromSample>()
  for (const s of samples ?? []) {
    const d = s.labels.ceph_daemon
    if (d) m.set(d, s)
  }
  return m
}

function num(m: Map<string, PromSample>, key: string): number | null {
  const v = m.get(key)?.value
  return v === undefined || !Number.isFinite(v) ? null : v
}

/**
 * The ids in a mgr `device_ids` label, `sdf=VENDOR_MODEL_SERIAL[,sdg=…]`.
 * A multi-device OSD (separate DB or WAL) lists several.
 */
export function parseDeviceIds(label: string | undefined): string[] {
  return (label ?? '')
    .split(',')
    .map((part) => part.slice(part.indexOf('=') + 1).trim())
    .filter((id) => id.length > 0)
}

/**
 * The serial inside a Ceph device id. Against a list of known serials, the
 * one the id ends with (models and serials may contain `_`, so splitting
 * alone can cut in the wrong place). Otherwise the last `_` segment.
 */
export function serialFromDeviceId(id: string, known: Iterable<string> = []): string {
  let best = ''
  for (const s of known) {
    if (!s) continue
    if ((id === s || id.endsWith(`_${s}`)) && s.length > best.length) best = s
  }
  if (best) return best
  const cut = id.lastIndexOf('_')
  return cut >= 0 ? id.slice(cut + 1) : id
}

/** QEMU names its virtual disks `QEMU_HARDDISK_<drive id>`; that disk has no bay and no BMC. */
function virtualDrive(id: string): string | null {
  const m = /^QEMU_(?:QEMU_)?HARDDISK_(.+)$/.exec(id)
  return m ? m[1] : null
}

/**
 * Tie one OSD to its disk, by serial. `host` is the OSD's node per the mgr.
 * The agent's `observed[].path` is reported next to it, never used to find it.
 */
export function linkOsdDisk(deviceIds: string[], host: string, machines: Machine[]): OsdDiskLink {
  const id = deviceIds[0]
  if (!id) return { kind: 'unknown', reason: 'the mgr reports no device id for this OSD' }
  const drive = virtualDrive(id)
  if (drive) return { kind: 'virtual', serial: drive, drive }

  const known: string[] = []
  for (const m of machines) {
    for (const d of m.inventory?.drives ?? []) known.push(d.serialNumber)
    for (const d of m.storage?.observed ?? []) known.push(d.serialNumber)
  }
  const serial = serialFromDeviceId(id, known)

  const holders = machines.filter(
    (m) =>
      (m.inventory?.drives ?? []).some((d) => d.serialNumber === serial) ||
      (m.storage?.observed ?? []).some((d) => d.serialNumber === serial),
  )
  // Two machines claiming one serial is a data problem (a cloned inventory,
  // a BMC reporting a placeholder): the OSD's own node wins if it is one of
  // them, and the duplicate is named rather than silently resolved.
  const m = holders.find((h) => h.nodeRef === host) ?? holders[0]
  if (!m) {
    return { kind: 'physical', serial, machine: null, bay: null, model: null, osPath: null, hostConflict: false, duplicateOn: [] }
  }
  const bmc = (m.inventory?.drives ?? []).find((d) => d.serialNumber === serial)
  const os = (m.storage?.observed ?? []).find((d) => d.serialNumber === serial)
  return {
    kind: 'physical',
    serial,
    machine: m.name,
    bay: bmc?.location || null,
    model: bmc?.model || null,
    osPath: os?.path ?? null,
    hostConflict: !!m.nodeRef && m.nodeRef !== host,
    duplicateOn: holders.length > 1 ? holders.map((h) => h.name) : [],
  }
}

/**
 * Default stability window: a restart older than this, with no restart
 * since, is forgotten. Configurable (frame-config `storage.osdStableAfterMinutes`).
 */
export const FLAP_WINDOW_MS = 60 * 60 * 1000

/** "47 min", "3 h 12 min" — how long something has lasted. */
export function durationText(ms: number): string {
  const min = Math.max(0, Math.floor(ms / 60000))
  if (min < 60) return `${min} min`
  const h = Math.floor(min / 60)
  return min % 60 ? `${h} h ${min % 60} min` : `${h} h`
}

/**
 * How a running, ready pod has fared since its last restart. null when it
 * never restarted or the restart time is unknown.
 */
export function restartHistory(pod: OsdPod, now: number, windowMs: number): { settling: boolean; text: string } | null {
  const last = pod.lastTerminationAt ? Date.parse(pod.lastTerminationAt) : NaN
  if (pod.restarts === 0 || !Number.isFinite(last)) return null
  const started = pod.startedAt ? Date.parse(pod.startedAt) : NaN
  const since = Number.isFinite(started) && started >= last ? started : last
  const settling = now - last < windowMs
  const text = `stable for ${durationText(now - since)}, ${pod.restarts} restart(s) in total`
  return { settling, text: settling ? `${text} — watched until ${durationText(windowMs)} without a restart` : text }
}

function judgeOsd(
  up: boolean | null,
  inn: boolean | null,
  pod: OsdPod | null,
  now: number,
  windowMs: number,
): { state: OsdState; detail: string } {
  if (up === false) return { state: 'down', detail: 'down for Ceph' }
  const podTrouble = pod ? podInstability(pod, now, windowMs) : ''
  if (podTrouble) return { state: 'unstable', detail: podTrouble }
  if (inn === false) return { state: 'out', detail: 'out of the data placement' }
  if (up === null || inn === null) return { state: 'unknown', detail: 'no ceph-mgr reading' }
  const history = pod ? restartHistory(pod, now, windowMs) : null
  if (history?.settling) return { state: 'settling', detail: history.text }
  return { state: 'up-in', detail: history?.text ?? '' }
}

/**
 * Empty when the pod is running and ready; otherwise what is wrong with it,
 * in words. A recent restart of a pod that is now running and ready is not
 * instability — see restartHistory and the `settling` state.
 */
export function podInstability(pod: OsdPod, now: number, windowMs = FLAP_WINDOW_MS): string {
  const parts: string[] = []
  if (pod.phase !== 'Running') {
    parts.push(
      pod.initTotal > 0 && pod.initDone < pod.initTotal
        ? `pod in init ${pod.initDone}/${pod.initTotal}${pod.waiting ? ` (${pod.waiting})` : ''}`
        : `pod ${pod.phase}${pod.waiting ? ` (${pod.waiting})` : ''}`,
    )
  } else if (!pod.ready) {
    parts.push(`pod not ready${pod.waiting ? ` (${pod.waiting})` : ''}`)
  }
  if (parts.length === 0) return ''
  const last = pod.lastTerminationAt ? Date.parse(pod.lastTerminationAt) : NaN
  if (pod.restarts > 0 && Number.isFinite(last) && now - last < windowMs) {
    parts.push(`${pod.restarts} restart(s), last ${durationText(now - last)} ago`)
  }
  return parts.join(', ')
}

export interface OsdInputs {
  prom: PromSnapshot
  pods: OsdPod[]
  machines: Machine[]
  now: number
  /** Stability window after a restart (default FLAP_WINDOW_MS). */
  stableAfterMs?: number
  /** CRUSH weights from the ceph-mgr dashboard, by OSD id; an absent id stays null. */
  crushWeights?: Map<number, number>
}

export function buildOsdRows({ prom, pods, machines, now, stableAfterMs = FLAP_WINDOW_MS, crushWeights }: OsdInputs): OsdRow[] {
  const meta = byDaemon(prom.osdMeta)
  const up = byDaemon(prom.osdUp)
  const inn = byDaemon(prom.osdIn)
  const pgs = byDaemon(prom.osdPgs)
  const bytes = byDaemon(prom.osdBytes)
  const used = byDaemon(prom.osdUsed)
  const apply = byDaemon(prom.osdApply)
  const commit = byDaemon(prom.osdCommit)
  const disk = byDaemon(prom.osdDisk)
  const slow = byDaemon(prom.slowOps)
  const blue = byDaemon(prom.bluestoreSlow)
  const podById = currentOsdPods(pods)

  const ids = new Set<number>()
  for (const m of [meta, up, inn]) for (const k of m.keys()) ids.add(Number(k.replace(/^osd\./, '')))
  for (const k of podById.keys()) ids.add(k)

  return [...ids]
    .filter((id) => Number.isInteger(id))
    .sort((a, b) => a - b)
    .map((id): OsdRow => {
      const name = `osd.${id}`
      const pod = podById.get(id) ?? null
      const host = meta.get(name)?.labels.hostname || pod?.node || ''
      const total = num(bytes, name)
      const usedB = num(used, name)
      const upV = num(up, name)
      const inV = num(inn, name)
      const deviceIds = parseDeviceIds(disk.get(name)?.labels.device_ids)
      const judged = judgeOsd(upV === null ? null : upV === 1, inV === null ? null : inV === 1, pod, now, stableAfterMs)
      return {
        id,
        name,
        deviceClass: meta.get(name)?.labels.device_class ?? '',
        host,
        up: upV === null ? null : upV === 1,
        in: inV === null ? null : inV === 1,
        pgs: num(pgs, name),
        bytesTotal: total,
        bytesUsed: usedB,
        usePct: total && usedB !== null ? (100 * usedB) / total : null,
        applyMs: num(apply, name),
        commitMs: num(commit, name),
        slowOps: num(slow, name),
        bluestoreSlow: num(blue, name),
        crushWeight: crushWeights?.get(id) ?? null,
        devices: (disk.get(name)?.labels.devices ?? '').split(',').filter(Boolean),
        deviceIds,
        pod,
        state: judged.state,
        stateDetail: judged.detail,
        disk: linkOsdDisk(deviceIds, host, machines),
      }
    })
}

/** "osd.3-6" for a contiguous run, "osd.1, osd.4" otherwise — how the banner names a group. */
export function osdRange(ids: number[]): string {
  const s = [...ids].sort((a, b) => a - b)
  if (s.length === 0) return ''
  const contiguous = s.every((v, i) => i === 0 || v === s[i - 1] + 1)
  if (contiguous && s.length > 2) return `osd.${s[0]}-${s[s.length - 1]}`
  return s.map((i) => `osd.${i}`).join(', ')
}
