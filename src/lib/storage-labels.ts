// The one-cell wordings the storage tables share. Pure, so the phrasing an
// operator reads is tested rather than buried in a component.

import type { OsdRow } from './ceph-diagnostic'
import type { PgTally } from './ceph-pools'
import { PG_DISPLAY_STATES } from './ceph-pools'

/** "active 121 · clean 121" — only states present, in reading order. States overlap. */
export function pgLine(t: PgTally): string {
  const parts = PG_DISPLAY_STATES.filter((s) => (t.byState[s] ?? 0) > 0).map((s) => `${s} ${t.byState[s]}`)
  return parts.join(' · ') || 'no state reported'
}

/** Where an OSD's data physically sits: machine and bay, or the VM disk. */
export function osdDiskLabel(o: OsdRow): string {
  switch (o.disk.kind) {
    case 'physical':
      if (!o.disk.machine) return `serial ${o.disk.serial} (no FrameMachine)`
      return `${o.disk.machine} ${o.disk.bay ?? 'bay ?'} · ${o.disk.serial}`
    case 'virtual':
      // Hypervisor storage is out of Frame's scope (arbitration of 27/09):
      // the VM disk is named, what backs it on the host is not read.
      return `VM disk (QEMU ${o.disk.drive})`
    default:
      return o.disk.reason
  }
}
