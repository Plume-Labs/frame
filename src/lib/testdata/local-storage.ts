// status.storage.local as the node agent publishes it, for the console
// tests. g9Local() is the hardware node as read on 27/09 (md0 raid1 on two
// SSD partitions, no ZFS, LVM tools without PV); zfsLocal() mirrors the Go
// fixtures in internal/agent/testdata/localstorage/ (a degraded raidz1 with a
// faulted leaf and a scrub that found errors). Invented serials only.
// Only test files import this module.

import type { LocalStorage } from '../machines'

export function g9Local(): LocalStorage {
  return {
    zfs: { state: 'absent' },
    md: {
      state: 'present',
      arrays: [
        {
          name: 'md0', level: 'raid1', state: 'active', raidDisks: 2, activeDisks: 2, slots: 'UU', degraded: false,
          members: [{ device: 'sdd2', serial: 'SER-SSD2' }, { device: 'sdc2', serial: 'SER-SSD1' }],
        },
      ],
    },
    lvm: { state: 'absent' },
  }
}

export function zfsLocal(): LocalStorage {
  return {
    zfs: {
      state: 'present',
      pools: [
        {
          name: 'data', health: 'DEGRADED', sizeBytes: 15994458210304, allocatedBytes: 5e12, freeBytes: 10994458210304,
          fragmentationPercent: 12, capacityPercent: 31,
          scan: 'scrub repaired 0B in 05:12:44 with 2 errors on Sun Sep 14 00:24:01 2026', scrubErrors: 2,
          lastScrub: 'Sun Sep 14 00:24:01 2026', errors: 'No known data errors',
          vdevs: [
            { name: 'raidz1-0', depth: 0, state: 'DEGRADED' },
            { name: '/dev/disk/by-id/ata-HUC10121CLAR1200_SER-HDD1-part1', depth: 1, state: 'ONLINE' },
            { name: '/dev/disk/by-id/wwn-0x5000c500deadbe03-part1', depth: 1, state: 'FAULTED', readErrors: 12, checksumErrors: 1200, note: 'too many errors' },
            { name: 'logs', depth: 0 },
            { name: 'mirror-1', depth: 1, state: 'ONLINE' },
            { name: '/dev/disk/by-id/ata-SSD-A_SER-SSD1-part1', depth: 2, state: 'ONLINE' },
          ],
        },
        {
          name: 'rpool', health: 'ONLINE', sizeBytes: 248034361344, allocatedBytes: 3e10, freeBytes: 218034361344,
          scan: 'scrub in progress since Sat Sep 27 01:00:00 2026', vdevs: [{ name: 'mirror-0', depth: 0, state: 'ONLINE' }],
        },
      ],
      datasets: [{ name: 'data', type: 'filesystem', usedBytes: 5e12, availBytes: 1e13, referBytes: 2e5, mountPoint: '/data' }],
    },
    md: { state: 'absent' },
    lvm: {
      state: 'present',
      pvs: [{ name: '/dev/sdb2', vg: 'vg0', sizeBytes: 499570991104, freeBytes: 0, attr: 'a--', serial: 'SER-SYS1' }],
      vgs: [{ name: 'vg0', sizeBytes: 999674634240, freeBytes: 1e11, pvCount: 2, lvCount: 2, attr: 'wz--n-' }],
      lvs: [{ name: 'root', vg: 'vg0', sizeBytes: 53687091200, attr: '-wi-ao----', segType: 'linear', dataPercent: '' }],
    },
  }
}
