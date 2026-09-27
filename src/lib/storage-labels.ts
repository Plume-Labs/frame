// The one-cell wordings the storage tables share. Pure, so the phrasing an
// operator reads is tested rather than buried in a component.

import type { OsdRow } from './ceph-diagnostic'
import type { PgTally } from './ceph-pools'
import { PG_DISPLAY_STATES } from './ceph-pools'
import { zvolForVirtualOsd, type ZfsSourceResult } from './zfs-source'

/** "active 121 · clean 121" — only states present, in reading order. States overlap. */
export function pgLine(t: PgTally): string {
  const parts = PG_DISPLAY_STATES.filter((s) => (t.byState[s] ?? 0) > 0).map((s) => `${s} ${t.byState[s]}`)
  return parts.join(' · ') || 'no state reported'
}

/** Where an OSD's data physically sits: machine and bay, or VM disk and zvol. */
export function osdDiskLabel(o: OsdRow, zfs: ZfsSourceResult | null, nodeUuids: Record<string, string>): string {
  switch (o.disk.kind) {
    case 'physical':
      if (!o.disk.machine) return `serial ${o.disk.serial} (no FrameMachine)`
      return `${o.disk.machine} ${o.disk.bay ?? 'bay ?'} · ${o.disk.serial}`
    case 'virtual': {
      const hosts = zfs?.status === 'ok' ? zfs.hosts : []
      const z = zvolForVirtualOsd(o.disk.drive, nodeUuids[o.host], hosts)
      if (z) return `VM ${z.vm.vmid} ${z.vm.slot} → ${z.host}:${z.vm.volume.slice(z.vm.volume.indexOf(':') + 1)}`
      return `VM (QEMU ${o.disk.drive}) · zvol: ${zfs?.status === 'ok' ? 'not matched' : 'ZFS source not configured'}`
    }
    default:
      return o.disk.reason
  }
}
