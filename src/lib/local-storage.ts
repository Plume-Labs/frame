// Judgements on a machine's local storage stacks — ZFS pools, mdadm arrays,
// LVM — as the node agent publishes them in `status.storage.local`. Pure.
//
// Two rules carried over from the rest of the storage screens:
//  - a stack the node does not run ("absent") is normal and says so; a stack
//    that could not be read ("error") is a finding, never an empty list;
//  - a vdev or md member is tied to a disk by serial (by-id path or the
//    agent's own lsblk), never by /dev/sdX.

import type { LocalStorage, Machine, ObservedDisk, ZPool, ZVdev } from './machines'
import type { AttentionItem } from './storage-attention'

/** Pool states zpool reports that mean data is at risk or unavailable. */
const POOL_OK = 'ONLINE'

/** Interior vdevs and group headers; every other row is a leaf (a path, or a GUID for a missing device). */
const INTERIOR = /^(mirror|raidz\d?|draid\d?|spare|replacing|logs|cache|spares|special|dedup)(-|$|:)/

function isLeaf(v: ZVdev): boolean {
  return !INTERIOR.test(v.name)
}

/**
 * The disk behind a vdev leaf (`zpool status -P` prints device paths). The
 * partition suffix is dropped, then the by-id path is compared with the
 * agent's WWN link, or its tail with a disk serial (`ata-MODEL_SERIAL`).
 * A bare /dev/sdX leaf is not joined: its name changes across boots.
 */
export function vdevDisk(name: string, observed: ObservedDisk[], note = ''): ObservedDisk | null {
  // A leaf zpool can no longer open is printed by GUID, with its last path
  // in the note ("was /dev/disk/by-id/…"): join on that path.
  const was = /^was (\/dev\/\S+)/.exec(note)
  if (!name.startsWith('/dev/') && was) name = was[1]
  if (!name.startsWith('/dev/disk/by-id/')) return null
  const base = name.replace(/-part\d+$/, '')
  const byWwn = observed.find((d) => d.byIdPath && d.byIdPath === base)
  if (byWwn) return byWwn
  const tail = base.slice('/dev/disk/by-id/'.length)
  let best: ObservedDisk | null = null
  for (const d of observed) {
    if (!d.serialNumber) continue
    if ((tail.endsWith(`_${d.serialNumber}`) || tail === d.serialNumber) && d.serialNumber.length > (best?.serialNumber.length ?? 0)) {
      best = d
    }
  }
  return best
}

function vdevErrors(v: ZVdev): number {
  return (v.readErrors ?? 0) + (v.writeErrors ?? 0) + (v.checksumErrors ?? 0)
}

/** "raidz1-0 / /dev/disk/by-id/…" — the path of a row in the flattened tree. */
export function vdevPath(vdevs: ZVdev[], index: number): string {
  const parts = [vdevs[index].name]
  let depth = vdevs[index].depth
  for (let i = index - 1; i >= 0 && depth > 0; i--) {
    if (vdevs[i].depth < depth) {
      parts.unshift(vdevs[i].name)
      depth = vdevs[i].depth
    }
  }
  return parts.join(' / ')
}

function poolItems(machine: string, p: ZPool): AttentionItem[] {
  const out: AttentionItem[] = []
  const where = `${machine} ZFS ${p.name}`
  if ((p.health ?? '') !== POOL_OK) {
    out.push({ severity: 'critical', code: 'zfs-pool-health', text: `${where}: pool ${p.health || 'state unknown'}` })
  }
  const vdevs = p.vdevs ?? []
  vdevs.forEach((v, i) => {
    const n = vdevErrors(v)
    const bad = v.state && !['ONLINE', 'AVAIL', 'INUSE'].includes(v.state)
    if (n > 0 || (bad && isLeaf(v))) {
      out.push({
        severity: 'warning',
        code: 'zfs-vdev',
        text: `${where}: ${vdevPath(vdevs, i)} ${v.state ?? ''} — ${v.readErrors ?? 0} read, ${v.writeErrors ?? 0} write, ${v.checksumErrors ?? 0} checksum error(s)${v.note ? ` (${v.note})` : ''}`,
      })
    }
  })
  if ((p.scrubErrors ?? 0) > 0) {
    out.push({ severity: 'critical', code: 'zfs-scrub-errors', text: `${where}: last scrub (${p.lastScrub ?? '?'}) found ${p.scrubErrors} error(s)` })
  }
  // "Permanent errors have been detected…": files already lost or damaged.
  if (p.errors && !/^No known data errors/i.test(p.errors)) {
    out.push({ severity: 'critical', code: 'zfs-data-errors', text: `${where}: ${p.errors}` })
  }
  if ((p.vdevsOmitted ?? 0) > 0) {
    out.push({ severity: 'info', code: 'zfs-vdevs-omitted', text: `${where}: ${p.vdevsOmitted} vdev row(s) not listed (status size limit)` })
  }
  return out
}

/** The banner findings of one machine's local stacks. */
export function localStackItems(machine: Machine): AttentionItem[] {
  const local: LocalStorage | null | undefined = machine.storage?.local
  if (!local) return []
  const out: AttentionItem[] = []
  const m = machine.name
  for (const [stack, s] of [['ZFS', local.zfs], ['md', local.md], ['LVM', local.lvm]] as const) {
    if (s.state === 'error') {
      out.push({ severity: 'warning', code: 'local-stack-error', text: `${m} ${stack}: could not be read — ${s.error || 'no reason given'} (state unknown)` })
    } else if (s.state === 'busy') {
      out.push({
        severity: 'warning',
        code: 'local-stack-busy',
        text: `${m} ${stack}: previous read still running on the node — showing the read of ${s.readAt ?? 'an earlier cycle'}`,
      })
    }
  }
  for (const p of local.zfs.pools ?? []) out.push(...poolItems(m, p))
  if ((local.zfs.poolsOmitted ?? 0) > 0) {
    out.push({ severity: 'warning', code: 'zfs-pools-omitted', text: `${m} ZFS: ${local.zfs.poolsOmitted} pool(s) not listed (status size limit)` })
  }
  for (const a of local.md.arrays ?? []) {
    if (!a.degraded) continue
    const failed = (a.members ?? []).filter((x) => x.failed).map((x) => x.device)
    out.push({
      severity: 'critical',
      code: 'md-degraded',
      text: `${m} ${a.name} (${a.level || 'md'}): ${a.state}${a.slots ? ` [${a.slots}]` : ''}${a.raidDisks ? ` ${a.activeDisks}/${a.raidDisks} active` : ''}${failed.length ? `, failed ${failed.join(', ')}` : ''}${a.sync ? `, ${a.sync}` : ''}`,
    })
  }
  if ((local.md.arraysOmitted ?? 0) > 0) {
    out.push({ severity: 'warning', code: 'md-arrays-omitted', text: `${m} md: ${local.md.arraysOmitted} array(s) not listed (status size limit) — their state is unknown` })
  }
  for (const vg of local.lvm.vgs ?? []) {
    // vg_attr's 4th character: 'p' = partial, a PV is missing.
    if ((vg.attr ?? '')[3] === 'p') {
      out.push({ severity: 'critical', code: 'lvm-vg-partial', text: `${m} LVM ${vg.name}: volume group partial — a physical volume is missing` })
    }
  }
  return out
}

/** Nodes whose md arrays the agent publishes: node-exporter's md view is then redundant for them. */
export function nodesWithPublishedMd(machines: Machine[]): Set<string> {
  return new Set(machines.filter((m) => m.storage?.local?.md.state === 'present' && m.nodeRef).map((m) => m.nodeRef))
}

/** One line per stack for the card header: what the node runs. */
export function stackSummary(local: LocalStorage | null | undefined): string {
  if (!local) return "not published by this node's agent"
  const one = (label: string, state: string, n: number, unit: string) =>
    state === 'present'
      ? `${label}: ${n} ${unit}`
      : state === 'absent'
        ? `${label}: not in use`
        : state === 'busy'
          ? `${label}: ${n} ${unit} (previous read still running)`
          : `${label}: unreadable`
  return [
    one('ZFS', local.zfs.state, (local.zfs.pools ?? []).length, 'pool(s)'),
    one('md', local.md.state, (local.md.arrays ?? []).length, 'array(s)'),
    one('LVM', local.lvm.state, (local.lvm.vgs ?? []).length, 'VG(s)'),
  ].join(' · ')
}
