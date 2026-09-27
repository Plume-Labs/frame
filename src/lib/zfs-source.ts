// The hypervisor's ZFS pools and disks — the storage under the VM nodes'
// OSDs, which neither Ceph nor a FrameMachine can see. Frame runs no agent
// on the Proxmox host, so this is read from the Proxmox API (read-only token,
// PVEAuditor) through a small in-cluster reader, behind the `ZfsSource`
// interface: until that reader exists the screen says "source not
// configured", never an empty pool list that would read as "no pools".
//
// Everything here is pure: `parseProxmoxHost` turns the API's JSON into the
// model, `zfsFindings` judges it. The SDK does the fetching.

import type { AttentionItem } from './storage-attention'

export interface ZfsVdev {
  name: string
  state: string
  read: number
  write: number
  cksum: number
  msg: string
  children: ZfsVdev[]
}

export interface ScrubStatus {
  kind: 'done' | 'in-progress' | 'none' | 'other'
  errors: number | null
  /** As Proxmox prints it (`Sun Sep 14 01:47:46 2026`), parsed where possible. */
  when: string | null
  whenMs: number | null
  text: string
}

export interface ZfsPool {
  name: string
  health: string
  sizeBytes: number
  allocBytes: number
  freeBytes: number
  fragPct: number | null
  scrub: ScrubStatus
  errors: string
  vdevs: ZfsVdev[]
}

export interface HostDisk {
  devpath: string
  serial: string
  model: string
  wwn: string
  sizeBytes: number
  type: string
  used: string
  /** SMART overall verdict as Proxmox reports it (PASSED, OK, FAILED, UNKNOWN). */
  smartHealth: string
  /** Proxmox's `wearout` as given; null when it says N/A. */
  wearout: number | null
}

/** One VM disk, tied to the zvol behind it. */
export interface GuestDisk {
  vmid: number
  vmName: string
  /** SMBIOS UUID the guest reports as its system UUID — the key to a Kubernetes node. */
  vmUuid: string | null
  /** Bus slot, `scsi2` — QEMU exposes it to the guest as the disk serial `drive-scsi2`. */
  slot: string
  volume: string
  storage: string
}

export interface ZfsDataset {
  storage: string
  volume: string
  sizeBytes: number
  usedBytes: number | null
  vmid: number | null
}

export interface ZfsHostReport {
  host: string
  pools: ZfsPool[]
  disks: HostDisk[]
  guestDisks: GuestDisk[]
  datasets: ZfsDataset[]
}

export type ZfsSourceResult =
  | { status: 'unconfigured'; reason: string }
  | { status: 'error'; message: string }
  | { status: 'ok'; hosts: ZfsHostReport[]; readAt: string }

/** Anything that can say what the hypervisor's ZFS looks like. */
export interface ZfsSource {
  read(): Promise<ZfsSourceResult>
}

// ── Proxmox API shapes (only the fields read) ──────────────────────────────

export interface PveZfsListItem {
  name: string
  size?: number
  alloc?: number
  free?: number
  frag?: number
  health?: string
}

export interface PveZfsNode {
  name: string
  state?: string
  read?: number
  write?: number
  cksum?: number
  msg?: string
  children?: PveZfsNode[]
}

export interface PveZfsDetail {
  name: string
  state?: string
  scan?: string
  errors?: string
  children?: PveZfsNode[]
}

export interface PveDisk {
  devpath: string
  serial?: string
  model?: string
  wwn?: string
  size?: number
  type?: string
  used?: string
  health?: string
  wearout?: number | string
}

export interface PveVm {
  vmid: number
  name?: string
  config: Record<string, string | number | undefined>
}

export interface PveStorageContent {
  storage: string
  items: Array<{ volid: string; size?: number; used?: number; vmid?: number | string }>
}

export interface PveHostPayload {
  node: string
  zfs: PveZfsListItem[]
  details: PveZfsDetail[]
  disks: PveDisk[]
  vms: PveVm[]
  zfsStorages: PveStorageContent[]
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** `Sun Sep 14 01:47:46 2026` → epoch ms, read as the host's local time rendered in UTC. */
function parseZpoolDate(s: string): number | null {
  const m = /([A-Z][a-z]{2})\s+(\d{1,2})\s+(\d{2}):(\d{2}):(\d{2})\s+(\d{4})/.exec(s)
  if (!m) return null
  const month = MONTHS.indexOf(m[1])
  if (month < 0) return null
  return Date.UTC(Number(m[6]), month, Number(m[2]), Number(m[3]), Number(m[4]), Number(m[5]))
}

export function parseScrub(scan: string | undefined): ScrubStatus {
  const text = (scan ?? '').trim()
  if (!text || /none requested/i.test(text)) return { kind: 'none', errors: null, when: null, whenMs: null, text }
  const done = /scrub repaired .* with (\d+) errors on (.+)$/i.exec(text)
  if (done) {
    return { kind: 'done', errors: Number(done[1]), when: done[2].trim(), whenMs: parseZpoolDate(done[2]), text }
  }
  const running = /scrub in progress since (.+?)(?:\n|$)/i.exec(text)
  if (running) {
    return { kind: 'in-progress', errors: null, when: running[1].trim(), whenMs: parseZpoolDate(running[1]), text }
  }
  return { kind: 'other', errors: null, when: null, whenMs: null, text }
}

function toVdev(n: PveZfsNode): ZfsVdev {
  return {
    name: n.name,
    state: n.state ?? 'UNKNOWN',
    read: Number(n.read ?? 0),
    write: Number(n.write ?? 0),
    cksum: Number(n.cksum ?? 0),
    msg: n.msg ?? '',
    children: (n.children ?? []).map(toVdev),
  }
}

/**
 * Proxmox nests the pool itself as the first child of its own status, with
 * `logs`/`cache`/`spares` as siblings. The vdev list is the pool's children
 * plus those siblings.
 */
function vdevsOf(detail: PveZfsDetail | undefined): ZfsVdev[] {
  const top = (detail?.children ?? []).map(toVdev)
  if (top.length > 0 && top[0].name === detail?.name) return [...top[0].children, ...top.slice(1)]
  return top
}

function wearout(v: number | string | undefined): number | null {
  const n = typeof v === 'number' ? v : Number(v)
  return typeof v === 'undefined' || v === 'N/A' || !Number.isFinite(n) ? null : n
}

function smbiosUuid(v: string | number | undefined): string | null {
  const m = /uuid=([0-9a-fA-F-]{36})/.exec(String(v ?? ''))
  return m ? m[1].toLowerCase() : null
}

const DISK_SLOT = /^(scsi|sata|virtio|ide)\d+$/

export function parseProxmoxHost(p: PveHostPayload): ZfsHostReport {
  const pools = p.zfs.map((z): ZfsPool => {
    const d = p.details.find((x) => x.name === z.name)
    return {
      name: z.name,
      health: d?.state || z.health || 'UNKNOWN',
      sizeBytes: z.size ?? 0,
      allocBytes: z.alloc ?? 0,
      freeBytes: z.free ?? 0,
      fragPct: typeof z.frag === 'number' ? z.frag : null,
      scrub: parseScrub(d?.scan),
      errors: d?.errors ?? '',
      vdevs: vdevsOf(d),
    }
  })
  const disks = p.disks.map(
    (d): HostDisk => ({
      devpath: d.devpath,
      serial: (d.serial ?? '').trim(),
      model: d.model ?? '',
      wwn: d.wwn ?? '',
      sizeBytes: d.size ?? 0,
      type: d.type ?? '',
      used: d.used ?? '',
      smartHealth: d.health ?? 'UNKNOWN',
      wearout: wearout(d.wearout),
    }),
  )
  const guestDisks: GuestDisk[] = []
  for (const vm of p.vms) {
    for (const [slot, raw] of Object.entries(vm.config)) {
      if (!DISK_SLOT.test(slot) || typeof raw !== 'string' || /media=cdrom/.test(raw)) continue
      const volume = raw.split(',')[0]
      if (!volume.includes(':')) continue
      guestDisks.push({
        vmid: vm.vmid,
        vmName: vm.name ?? '',
        vmUuid: smbiosUuid(vm.config.smbios1),
        slot,
        volume,
        storage: volume.slice(0, volume.indexOf(':')),
      })
    }
  }
  const datasets: ZfsDataset[] = p.zfsStorages.flatMap((s) =>
    s.items.map((i) => ({
      storage: s.storage,
      volume: i.volid,
      sizeBytes: i.size ?? 0,
      usedBytes: typeof i.used === 'number' ? i.used : null,
      vmid: i.vmid === undefined || i.vmid === '' ? null : Number(i.vmid),
    })),
  )
  return { host: p.node, pools, disks, guestDisks, datasets }
}

/** Leaves with I/O or checksum errors, with their path in the tree. */
export function erroredLeaves(vdevs: ZfsVdev[], path: string[] = []): Array<{ path: string; v: ZfsVdev }> {
  const out: Array<{ path: string; v: ZfsVdev }> = []
  for (const v of vdevs) {
    const here = [...path, v.name]
    if (v.read + v.write + v.cksum > 0) out.push({ path: here.join(' / '), v })
    out.push(...erroredLeaves(v.children, here))
  }
  return out
}

/** A scrub older than this is reported (Proxmox schedules one monthly by default). */
export const SCRUB_MAX_AGE_MS = 35 * 24 * 3600 * 1000

export function zfsFindings(r: ZfsHostReport, now: number): AttentionItem[] {
  const out: AttentionItem[] = []
  for (const p of r.pools) {
    if (p.health !== 'ONLINE') {
      out.push({ severity: 'critical', code: 'zfs-pool-health', text: `ZFS ${r.host}/${p.name}: pool ${p.health}` })
    }
    for (const { path, v } of erroredLeaves(p.vdevs)) {
      out.push({
        severity: 'warning',
        code: 'zfs-vdev-errors',
        text: `ZFS ${r.host}/${p.name}: ${path} — ${v.read} read, ${v.write} write, ${v.cksum} checksum error(s)`,
      })
    }
    if (p.scrub.kind === 'done' && (p.scrub.errors ?? 0) > 0) {
      out.push({ severity: 'critical', code: 'zfs-scrub-errors', text: `ZFS ${r.host}/${p.name}: last scrub found ${p.scrub.errors} error(s)` })
    } else if (p.scrub.kind === 'none') {
      out.push({ severity: 'info', code: 'zfs-scrub-never', text: `ZFS ${r.host}/${p.name}: never scrubbed` })
    } else if (p.scrub.kind === 'done' && p.scrub.whenMs !== null && now - p.scrub.whenMs > SCRUB_MAX_AGE_MS) {
      out.push({ severity: 'info', code: 'zfs-scrub-old', text: `ZFS ${r.host}/${p.name}: last scrub ${p.scrub.when}` })
    }
  }
  for (const d of r.disks) {
    if (/^(PASSED|OK)$/i.test(d.smartHealth) || /^UNKNOWN$/i.test(d.smartHealth) || !d.smartHealth) continue
    out.push({
      severity: 'critical',
      code: 'host-disk-smart',
      text: `${r.host} ${d.devpath} (${d.model || 'disk'} ${d.serial}): SMART ${d.smartHealth}`,
    })
  }
  return out
}

/**
 * The zvol under a VM node's OSD: the OSD's QEMU drive (`drive-scsi2`) on the
 * VM whose SMBIOS UUID is the node's system UUID.
 */
export function zvolForVirtualOsd(
  drive: string,
  nodeSystemUuid: string | undefined,
  hosts: ZfsHostReport[],
): { host: string; vm: GuestDisk } | null {
  const slot = drive.replace(/^drive-/, '')
  const uuid = (nodeSystemUuid ?? '').toLowerCase()
  if (!uuid) return null
  for (const h of hosts) {
    const vm = h.guestDisks.find((g) => g.vmUuid === uuid && g.slot === slot)
    if (vm) return { host: h.host, vm }
  }
  return null
}
