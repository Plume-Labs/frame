// One row per physical disk of a machine, joined across the three sources
// that each know part of it: the BMC (bay, model, health), the node agent
// (kernel path, occupancy) and Ceph (which OSD it carries). Pure.
//
// The join key is the serial number and nothing else. The two inventory
// lists stay visible side by side in the row — BMC columns, OS columns — so
// a disk one side cannot see still shows as a row with the other side
// marked absent: the gap is the datum (docs/superpowers/specs/2026-09-13-storage-design.md §3).
//
// SMART and per-disk temperature are not collected yet (the iLO4 reports
// 0 °C and no hours for SAS disks, and smartctl is not run). They are
// stated as such, never shown as 0.

import type { Machine, DriveInfo, ObservedDisk } from './machines'
import type { OsdRow, PromSample } from './ceph-diagnostic'

/** `os-unreported`: the BMC lists it and the node agent has never published — no verdict. */
export type DiskPresence = 'both' | 'bmc-only' | 'os-only' | 'os-unreported' | 'no-serial'

export interface DiskIo {
  utilPct: number | null
  awaitMs: number | null
}

export interface DiskRow {
  serial: string
  bay: string | null
  model: string | null
  sizeGB: number | null
  protocol: string | null
  mediaType: string | null
  bmcHealth: string | null
  bmcReasons: string[]
  osPath: string | null
  occupancy: string | null
  presence: DiskPresence
  /** The OSD whose mgr device id carries this serial. */
  osd: OsdRow | null
  role: string
  io: DiskIo
  // Lot 2 — from the agent (identity, mount, membership) and the BMC (drive
  // firmware and counters). null = not reported; a BMC 0 never gets here.
  wwn: string | null
  byIdPath: string | null
  mountPoint: string | null
  members: string[]
  firmware: string | null
  rotationSpeedRPM: number | null
  linkSpeedMbps: number | null
  temperatureCelsius: number | null
  powerOnHours: number | null
  /**
   * Whether the disk's WWN by-id path is one the CephCluster spec declares.
   * null when either side is unknown (no WWN published, or the spec lists no
   * device and lets Rook pick).
   */
  declaredInCeph: boolean | null
  /** Other machines whose inventory carries the same serial (empty normally). */
  duplicateOn: string[]
}

/** What a disk is for, in the words an operator uses. */
export function diskRole(
  osd: OsdRow | null,
  occupancy: string | null,
  presence: DiskPresence,
  detail: { members?: string[]; mountPoint?: string } = {},
): string {
  if (osd) return `${osd.name}${osd.deviceClass ? ` (${osd.deviceClass})` : ''}`
  if (presence === 'bmc-only') return 'not presented to the OS'
  if (presence === 'no-serial') return 'serial unknown — cannot be joined'
  const members = (detail.members ?? []).join(', ')
  switch (occupancy) {
    case 'ceph-osd':
      return 'ceph-osd (OSD not identified)'
    case 'md-member':
      return members ? `mdadm member (${members})` : 'mdadm member'
    case 'zfs-member':
      return members ? `ZFS member (${members})` : 'ZFS member'
    case 'mounted':
      return detail.mountPoint ? `mounted ${detail.mountPoint}` : 'mounted'
    case 'partitioned':
      return 'partitioned'
    case 'lvm-pv':
      return 'LVM PV'
    case 'free':
      return 'free'
    case null:
    case undefined:
      return '—'
    default:
      return occupancy
  }
}

function kernelName(path: string | null): string {
  return (path ?? '').replace(/^\/dev\//, '')
}

/** I/O figures keyed `node/sdX` from node-exporter (joined to node names in PromQL). */
export function ioIndex(util: PromSample[] | undefined, wait: PromSample[] | undefined): Map<string, DiskIo> {
  const out = new Map<string, DiskIo>()
  const put = (s: PromSample, key: keyof DiskIo) => {
    const k = `${s.labels.nodename ?? ''}/${s.labels.device ?? ''}`
    const cur = out.get(k) ?? { utilPct: null, awaitMs: null }
    cur[key] = Number.isFinite(s.value) ? s.value : null
    out.set(k, cur)
  }
  for (const s of util ?? []) put(s, 'utilPct')
  for (const s of wait ?? []) put(s, 'awaitMs')
  return out
}

/** The OSD whose disk link names this serial on this machine. */
function osdFor(serial: string, machine: string, osds: OsdRow[]): OsdRow | null {
  return (
    osds.find((o) => o.disk.kind === 'physical' && o.disk.serial === serial && (o.disk.machine === machine || o.disk.machine === null)) ??
    null
  )
}

export function buildDiskRows(
  machine: Machine,
  osds: OsdRow[],
  io: Map<string, DiskIo>,
  cephDevices: string[] = [],
  allMachines: Machine[] = [],
): DiskRow[] {
  const bmc: DriveInfo[] = machine.inventory?.drives ?? []
  const os: ObservedDisk[] = machine.storage?.observed ?? []
  const serials: string[] = []
  for (const d of [...bmc, ...os]) if (d.serialNumber && !serials.includes(d.serialNumber)) serials.push(d.serialNumber)

  const rows = serials.map((serial): DiskRow => {
    const b = bmc.find((d) => d.serialNumber === serial) ?? null
    const o = os.find((d) => d.serialNumber === serial) ?? null
    // An agent that never reported says nothing about presence: only a
    // machine whose agent did report can have a disk "missing" from the OS.
    const presence: DiskPresence = b && o ? 'both' : b ? (machine.storage ? 'bmc-only' : 'os-unreported') : 'os-only'
    const osd = osdFor(serial, machine.name, osds)
    const osPath = o?.path ?? null
    return {
      serial,
      bay: b?.location || null,
      model: b?.model || o?.model || null,
      sizeGB: b?.sizeGB || o?.sizeGB || null,
      protocol: b?.protocol || null,
      mediaType: b?.mediaType || null,
      bmcHealth: b ? b.health || 'unknown' : null,
      bmcReasons: (b?.statusReasons ?? []).filter((r) => r && r !== 'None'),
      osPath,
      occupancy: o?.occupancy ?? null,
      presence,
      osd,
      role: diskRole(osd, o?.occupancy ?? null, presence, { members: o?.members, mountPoint: o?.mountPoint }),
      io: (osPath && io.get(`${machine.nodeRef}/${kernelName(osPath)}`)) || { utilPct: null, awaitMs: null },
      wwn: o?.wwn || null,
      byIdPath: o?.byIdPath || null,
      mountPoint: o?.mountPoint || null,
      members: o?.members ?? [],
      firmware: b?.firmware || null,
      rotationSpeedRPM: b?.rotationSpeedRPM || null,
      linkSpeedMbps: b?.linkSpeedMbps || null,
      temperatureCelsius: b?.temperatureCelsius || null,
      powerOnHours: b?.powerOnHours || null,
      declaredInCeph: declaredInCeph(o?.byIdPath, cephDevices),
      duplicateOn: allMachines
        .filter((m) => m.name !== machine.name && machineSerials(m).includes(serial))
        .map((m) => m.name),
    }
  })
  // A BMC drive with no serial cannot be joined to anything: it still gets a
  // row, so it cannot vanish from the screen.
  bmc
    .filter((d) => !d.serialNumber)
    .forEach((d) =>
      rows.push({
        ...emptyRow(),
        bay: d.location || null,
        model: d.model || null,
        sizeGB: d.sizeGB || null,
        protocol: d.protocol || null,
        mediaType: d.mediaType || null,
        bmcHealth: d.health || 'unknown',
        bmcReasons: (d.statusReasons ?? []).filter((r) => r && r !== 'None'),
        presence: 'no-serial',
        role: diskRole(null, null, 'no-serial'),
      }),
    )
  // Bay order is the order a human walks the chassis in; disks with no bay last.
  return rows.sort((a, b) => (a.bay ?? '~').localeCompare(b.bay ?? '~') || a.serial.localeCompare(b.serial))
}

function emptyRow(): DiskRow {
  return {
    serial: '', bay: null, model: null, sizeGB: null, protocol: null, mediaType: null, bmcHealth: null,
    bmcReasons: [], osPath: null, occupancy: null, presence: 'no-serial', osd: null, role: '',
    io: { utilPct: null, awaitMs: null }, wwn: null, byIdPath: null, mountPoint: null, members: [],
    firmware: null, rotationSpeedRPM: null, linkSpeedMbps: null, temperatureCelsius: null, powerOnHours: null,
    declaredInCeph: null, duplicateOn: [],
  }
}

function machineSerials(m: Machine): string[] {
  return [...(m.inventory?.drives ?? []), ...(m.storage?.observed ?? [])].map((d) => d.serialNumber).filter(Boolean)
}

/** Serials listed by more than one machine, with the machines that list them. */
export function duplicateSerials(machines: Machine[]): Map<string, string[]> {
  const by = new Map<string, string[]>()
  for (const m of machines) {
    for (const s of new Set(machineSerials(m))) by.set(s, [...(by.get(s) ?? []), m.name])
  }
  return new Map([...by].filter(([, ms]) => ms.length > 1))
}

const COMPARABLE_BY_ID = /^\/dev\/disk\/by-id\/(wwn|nvme)-/

/**
 * Whether a disk's WWN by-id path is declared in the CephCluster device list.
 * A "no" is only said when it can be: every declared device is itself a
 * WWN/NVMe by-id path. A list naming `sdb`, `/dev/sdb` or `scsi-…` could
 * designate this very disk under another name, so it gives no verdict.
 */
export function declaredInCeph(byIdPath: string | undefined, cephDevices: string[]): boolean | null {
  if (!byIdPath || cephDevices.length === 0) return null
  if (cephDevices.includes(byIdPath)) return true
  return cephDevices.every((d) => COMPARABLE_BY_ID.test(d)) ? false : null
}

/** The chassis-level disk temperature sensor, the only one this iLO reports. */
export function chassisDiskTemp(machine: Machine): { name: string; celsius: number } | null {
  const t = (machine.sensors?.temperatures ?? []).find((s) => /HD Max/i.test(s.name))
  return t && t.celsius > 0 ? { name: t.name, celsius: t.celsius } : null
}

/** How old the agent's reading is, and whether it is too old to join on. */
export function agentFreshness(observedAt: string | null, now: number): { ageS: number | null; stale: boolean } {
  if (!observedAt) return { ageS: null, stale: true }
  const t = Date.parse(observedAt)
  if (!Number.isFinite(t)) return { ageS: null, stale: true }
  const ageS = Math.max(0, Math.round((now - t) / 1000))
  // The agent publishes every 30 s; four missed cycles is not a lag any more.
  return { ageS, stale: ageS > 120 }
}

export interface MdArray {
  node: string
  device: string
  active: number
  failed: number
  spare: number
  required: number | null
  state: string
  degraded: boolean
}

/** Software RAID arrays from node-exporter's md collector, one per node and device. */
export function mdArrays(
  disks: PromSample[] | undefined,
  required: PromSample[] | undefined,
  states: PromSample[] | undefined,
): MdArray[] {
  const key = (s: PromSample) => `${s.labels.nodename ?? ''}/${s.labels.device ?? ''}`
  const out = new Map<string, MdArray>()
  const get = (s: PromSample) => {
    const k = key(s)
    const cur = out.get(k) ?? {
      node: s.labels.nodename ?? '',
      device: s.labels.device ?? '',
      active: 0,
      failed: 0,
      spare: 0,
      required: null,
      state: 'unknown',
      degraded: false,
    }
    out.set(k, cur)
    return cur
  }
  for (const s of disks ?? []) {
    const a = get(s)
    if (s.labels.state === 'active') a.active = s.value
    if (s.labels.state === 'failed') a.failed = s.value
    if (s.labels.state === 'spare') a.spare = s.value
  }
  for (const s of required ?? []) get(s).required = s.value
  for (const s of states ?? []) if (s.value === 1) get(s).state = s.labels.state ?? 'unknown'
  for (const a of out.values()) {
    a.degraded = a.failed > 0 || (a.required !== null && a.active < a.required) || a.state === 'inactive'
  }
  return [...out.values()].sort((a, b) => `${a.node}/${a.device}`.localeCompare(`${b.node}/${b.device}`))
}

/**
 * OSDs whose node has no FrameMachine — the VM nodes. Their disks exist only
 * in Ceph's view; the screen lists them and says plainly that there is no
 * BMC and no agent publication behind them.
 */
export function osdsWithoutMachine(osds: OsdRow[], machines: Machine[]): OsdRow[] {
  const covered = new Set(machines.map((m) => m.nodeRef).filter(Boolean))
  return osds.filter((o) => !covered.has(o.host))
}

/** The array controllers of a machine, one line each; empty when the BMC reports none. */
export function controllerLines(machine: Machine): string[] {
  return (machine.inventory?.storageControllers ?? []).map((c) =>
    [
      c.location || 'controller',
      c.model || 'model not reported',
      c.firmware ? `fw ${c.firmware}` : 'fw not reported',
      `health ${c.health || 'not reported'}`,
      `cache battery ${c.backupPowerStatus || 'not reported'}`,
      c.cacheStatus ? `cache ${c.cacheStatus}` : '',
    ]
      .filter(Boolean)
      .join(' · '),
  )
}
