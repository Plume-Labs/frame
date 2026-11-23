// The one-cell wordings the storage tables share. Pure, so the phrasing an
// operator reads is tested rather than buried in a component.

import type { OsdRow } from './ceph-diagnostic'
import type { PgTally } from './ceph-pools'
import { PG_DISPLAY_STATES } from './ceph-pools'
import type { Message, MessageKey } from '@/i18n'

/** "active 121 · clean 121" — only states present, in reading order. States overlap. Ceph's state names stay raw. */
export function pgLine(t: PgTally): string | Message {
  const parts = PG_DISPLAY_STATES.filter((s) => (t.byState[s] ?? 0) > 0).map((s) => `${s} ${t.byState[s]}`)
  return parts.join(' · ') || { key: 'pool.noState' }
}

/** Where an OSD's data physically sits: machine and bay, or the VM disk. */
export function osdDiskLabel(o: OsdRow): Message {
  switch (o.disk.kind) {
    case 'physical':
      if (!o.disk.machine) return { key: 'osd.diskNoMachine', params: { serial: o.disk.serial } }
      return {
        key: 'osd.diskPhysical',
        params: { machine: o.disk.machine, bay: o.disk.bay ?? { key: 'osd.bayUnknown' }, serial: o.disk.serial },
      }
    case 'virtual':
      // Hypervisor storage is out of Frame's scope (arbitration of 27/09):
      // the VM disk is named, what backs it on the host is not read.
      return { key: 'osd.vmDisk', params: { drive: o.disk.drive } }
    default:
      return o.disk.reason
  }
}

/** The OSD state words of the tables (Ceph's up/in, plus the console's own readings). */
export const OSD_STATE_LABEL: Record<OsdRow['state'], MessageKey> = {
  'up-in': 'osd.state.upIn',
  down: 'osd.state.down',
  out: 'osd.state.out',
  unstable: 'osd.state.unstable',
  settling: 'osd.state.settling',
  unknown: 'osd.state.unknown',
}
