// An anonymised Proxmox VE host in the shape of its API answers
// (`/nodes/{node}/disks/zfs`, `…/disks/zfs/{pool}`, `…/disks/list`,
// `…/qemu/{vmid}/config`, `…/storage/{id}/content`): a raidz1 `data` pool
// of four 4 TB disks with a mirrored SLOG and an L2ARC partition, a mirrored
// `rpool`, and three VMs whose `scsi2` disks are the OSDs of the VM nodes.
// Only test files import this module.

import type { PveHostPayload } from '../zfs-source'

const TiB = 1024 ** 4

export function pvePayload(): PveHostPayload {
  const leaf = (name: string) => ({ name, state: 'ONLINE', read: 0, write: 0, cksum: 0, msg: '' })
  const vm = (vmid: number, name: string) => ({
    vmid,
    name,
    config: {
      smbios1: `uuid=00000000-0000-4000-8000-000000000${vmid}`,
      scsi0: `data:vm-${vmid}-disk-0,size=64G`,
      scsi2: `data:vm-${vmid}-disk-2,size=2T`,
      ide2: 'none,media=cdrom',
      net0: 'virtio=00:00:00:00:00:01,bridge=vmbr0',
    },
  })
  return {
    node: 'host-01',
    zfs: [
      { name: 'data', size: 16 * 10 ** 12, alloc: 5 * 10 ** 12, free: 11 * 10 ** 12, frag: 12, health: 'ONLINE' },
      { name: 'rpool', size: 248 * 10 ** 9, alloc: 30 * 10 ** 9, free: 218 * 10 ** 9, frag: 4, health: 'ONLINE' },
    ],
    details: [
      {
        name: 'data',
        state: 'ONLINE',
        scan: 'scrub repaired 0B in 05:12:44 with 0 errors on Sun Sep 14 00:24:01 2026',
        errors: 'No known data errors',
        children: [
          {
            name: 'data',
            state: 'ONLINE',
            read: 0,
            write: 0,
            cksum: 0,
            children: [
              {
                name: 'raidz1-0',
                state: 'ONLINE',
                read: 0,
                write: 0,
                cksum: 0,
                children: ['ata-HDD4T_SER-Z1', 'ata-HDD4T_SER-Z2', 'ata-HDD4T_SER-Z3', 'ata-HDD4T_SER-Z4'].map(leaf),
              },
            ],
          },
          { name: 'logs', state: '', children: [{ name: 'mirror-1', state: 'ONLINE', children: [leaf('ata-SSD-A_SER-L1-part1'), leaf('ata-SSD-B_SER-L2-part1')] }] },
          { name: 'cache', state: '', children: [leaf('nvme-NVME1T_SER-N1-part4')] },
        ],
      },
      {
        name: 'rpool',
        state: 'ONLINE',
        scan: 'scrub repaired 0B in 00:01:10 with 0 errors on Sun Sep 14 00:25:12 2026',
        errors: 'No known data errors',
        children: [
          {
            name: 'rpool',
            state: 'ONLINE',
            children: [{ name: 'mirror-0', state: 'ONLINE', children: [leaf('ata-SSD250_SER-R1-part3'), leaf('ata-SSD250_SER-R2-part3')] }],
          },
        ],
      },
    ],
    disks: [
      { devpath: '/dev/sdf', serial: 'SER-Z1', model: 'HDD4T', wwn: '0x5000c500aaaa0001', size: 4 * 10 ** 12, type: 'hdd', used: 'ZFS', health: 'PASSED', wearout: 'N/A' },
      { devpath: '/dev/sdg', serial: 'SER-Z2', model: 'HDD4T', wwn: '0x5000c500aaaa0002', size: 4 * 10 ** 12, type: 'hdd', used: 'ZFS', health: 'PASSED', wearout: 'N/A' },
      { devpath: '/dev/nvme0n1', serial: 'SER-N1', model: 'NVME1T', wwn: 'eui.0000000000000001', size: 10 ** 12, type: 'nvme', used: 'partitions', health: 'PASSED', wearout: 97 },
    ],
    vms: [vm(201, 'vm-node-a'), vm(202, 'vm-node-b'), vm(203, 'vm-node-c')],
    zfsStorages: [
      {
        storage: 'data',
        items: [201, 202, 203].flatMap((id) => [
          { volid: `data:vm-${id}-disk-0`, size: 64 * 1024 ** 3, used: 20 * 1024 ** 3, vmid: id },
          { volid: `data:vm-${id}-disk-2`, size: 2 * TiB, used: 40 * 1024 ** 3, vmid: id },
        ]),
      },
    ],
  }
}
