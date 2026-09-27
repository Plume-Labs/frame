// A realistic, anonymised Ceph cluster for the storage tests: the shape of
// the cluster this screen was written against (27/09/2026), with every
// hostname, serial and address replaced.
//
//  - 7 OSDs: osd.0-2 on three VM nodes, one virtual 2 TiB disk each
//    (CRUSH weight 2.0 — not exported by Prometheus, so absent below);
//    osd.3-6 on one hardware node, 1.2 TB SAS HDDs (CRUSH weight 1.09).
//  - 10 replicated pools, size 4 / min_size 2 under a custom CRUSH rule
//    (id 20) — min_size and the rule are not exported either.
//  - 121 PGs, all active+clean.
//
// Only test files import this module.

import type { PromSample, PromSnapshot, OsdPod } from '../ceph-diagnostic'
import type { Machine } from '../machines'

export const VM_NODES = ['node-a', 'node-b', 'node-c']
export const HW_NODE = 'node-hw'
export const HW_MACHINE = 'srv-hw'

const TiB = 1024 ** 4

function s(labels: Record<string, string>, value: number): PromSample {
  return { labels, value }
}

/** osd id → [host, class, bytes, used, pgs, sdX, device id] */
const OSDS: Array<[number, string, string, number, number, number, string, string]> = [
  [0, 'node-a', 'ssd', 2 * TiB, 33e9, 79, 'sda', 'QEMU_HARDDISK_drive-scsi2'],
  [1, 'node-b', 'ssd', 2 * TiB, 26e9, 77, 'sdb', 'QEMU_HARDDISK_drive-scsi2'],
  [2, 'node-c', 'ssd', 2 * TiB, 29e9, 86, 'sda', 'QEMU_HARDDISK_drive-scsi2'],
  [3, HW_NODE, 'hdd', 1_200_243_695_616, 19e9, 57, 'sdf', 'HITACHI_HUC10121CLAR1200_SER-HDD3'],
  [4, HW_NODE, 'hdd', 1_200_243_695_616, 24e9, 64, 'sdg', 'HITACHI_HUC10121CLAR1200_SER-HDD4'],
  [5, HW_NODE, 'hdd', 1_200_243_695_616, 20e9, 57, 'sdb', 'HITACHI_HUC10121CLAR1200_SER-HDD1'],
  [6, HW_NODE, 'hdd', 1_200_243_695_616, 23e9, 64, 'sde', 'HITACHI_HUC10121CLAR1200_SER-HDD2'],
]

const POOLS: Array<[string, string, string, number, number]> = [
  ['1', '.mgr', 'mgr', 1, 1_082_160],
  ['2', 'replicapool', 'Block', 32, 32_261_733_229],
  ['3', 'store.rgw.control', 'Object', 8, 171],
  ['4', 'store.rgw.meta', 'Object', 8, 18_373],
  ['5', 'store.rgw.buckets.non-ec', 'Object', 8, 1_218],
  ['6', 'store.rgw.otp', 'Object', 8, 189],
  ['7', 'store.rgw.buckets.index', 'Object', 8, 7_094_307],
  ['8', 'store.rgw.log', 'Object', 8, 5_805_327],
  ['9', '.rgw.root', 'Object', 8, 5_230],
  ['10', 'store.rgw.buckets.data', 'Object', 32, 10_563_984_759],
]

const FLAGS = ['noout', 'norebalance', 'nobackfill', 'norecover', 'noscrub', 'nodeep_scrub', 'nodown', 'noup', 'noin']

/** A fresh, healthy snapshot. Tests mutate their own copy. */
export function healthyProm(): PromSnapshot {
  const osd = (f: (o: (typeof OSDS)[number]) => number) => OSDS.map((o) => s({ ceph_daemon: `osd.${o[0]}` }, f(o)))
  const pgStates: PromSample[] = []
  for (const [id, , , pgs] of POOLS) {
    pgStates.push(s({ __name__: 'ceph_pg_total', pool_id: id }, pgs))
    for (const st of ['active', 'clean']) pgStates.push(s({ __name__: `ceph_pg_${st}`, pool_id: id }, pgs))
    for (const st of ['degraded', 'undersized', 'down', 'backfilling', 'backfill_wait', 'remapped', 'inconsistent', 'stale']) {
      pgStates.push(s({ __name__: `ceph_pg_${st}`, pool_id: id }, 0))
    }
  }
  return {
    osdMeta: OSDS.map((o) => s({ ceph_daemon: `osd.${o[0]}`, hostname: o[1], device_class: o[2] }, 1)),
    osdUp: osd(() => 1),
    osdIn: osd(() => 1),
    osdPgs: osd((o) => o[5]),
    osdBytes: osd((o) => o[3]),
    osdUsed: osd((o) => o[4]),
    osdApply: osd((o) => (o[0] === 0 ? 7 : 5)),
    osdCommit: osd((o) => (o[0] === 0 ? 7 : 5)),
    osdDisk: OSDS.map((o) =>
      s({ ceph_daemon: `osd.${o[0]}`, device: `/dev/${o[6]}`, devices: o[6], device_ids: `${o[6]}=${o[7]}`, exported_instance: o[1] }, 1),
    ),
    slowOps: osd(() => 0),
    bluestoreSlow: osd(() => 0),
    flags: FLAGS.map((f) => s({ __name__: `ceph_osd_flag_${f}` }, 0)),
    pgStates,
    objectsDegraded: [s({}, 0)],
    objectsMisplaced: [s({}, 0)],
    recoveryBytes: [s({}, 0)],
    poolMeta: POOLS.map(([id, name, app]) =>
      s({ pool_id: id, name, application: app, description: 'replica:4', type: 'replicated' }, 1),
    ),
    poolStored: POOLS.map(([id, , , , stored]) => s({ pool_id: id }, stored)),
    poolMaxAvail: POOLS.map(([id]) => s({ pool_id: id }, 2_651_575_812_096)),
    poolPercent: POOLS.map(([id, , , , stored]) => s({ pool_id: id }, stored / 2_651_575_812_096 / 4)),
    poolObjects: POOLS.map(([id, , , pgs]) => s({ pool_id: id }, pgs * 100)),
    monQuorum: ['a', 'e', 'f'].map((m) => s({ ceph_daemon: `mon.${m}` }, 1)),
    mgrStatus: [s({ ceph_daemon: 'mgr.a' }, 1)],
    classTotal: [s({ device_class: 'ssd' }, 6 * TiB), s({ device_class: 'hdd' }, 4 * 1_200_243_695_616)],
    classUsed: [s({ device_class: 'ssd' }, 88e9), s({ device_class: 'hdd' }, 87e9)],
    diskUtil: [s({ nodename: HW_NODE, device: 'sdf' }, 3.5), s({ nodename: HW_NODE, device: 'sdb' }, 2.6)],
    diskAwait: [s({ nodename: HW_NODE, device: 'sdf' }, 4.2)],
    mdDisks: [
      s({ nodename: HW_NODE, device: 'md0', state: 'active' }, 2),
      s({ nodename: HW_NODE, device: 'md0', state: 'failed' }, 0),
      s({ nodename: HW_NODE, device: 'md0', state: 'spare' }, 0),
    ],
    mdRequired: [s({ nodename: HW_NODE, device: 'md0' }, 2)],
    mdState: ['active', 'inactive', 'recovering', 'resync', 'check'].map((st) =>
      s({ nodename: HW_NODE, device: 'md0', state: st }, st === 'active' ? 1 : 0),
    ),
  }
}

/** Set one sample's value, by query key and label match. */
export function setSample(
  prom: PromSnapshot,
  key: keyof PromSnapshot,
  match: Record<string, string>,
  value: number,
): void {
  for (const x of prom[key] ?? []) {
    if (Object.entries(match).every(([k, v]) => x.labels[k] === v)) x.value = value
  }
}

export function osdPod(id: number, over: Partial<OsdPod> = {}): OsdPod {
  const [, host] = OSDS[id]
  return {
    osdId: id,
    name: `rook-ceph-osd-${id}-abc`,
    node: host,
    phase: 'Running',
    ready: true,
    restarts: 0,
    initDone: 4,
    initTotal: 4,
    waiting: '',
    startedAt: '2026-09-27T01:39:23Z',
    lastTerminationAt: null,
    createdAt: '2026-09-27T01:38:00Z',
    ...over,
  }
}

export function healthyPods(): OsdPod[] {
  return OSDS.map(([id]) => osdPod(id))
}

/**
 * The hardware node's FrameMachine: 8 bays, 4 OSD HDDs, a system disk, two
 * SSDs, and one disk the BMC lists that the kernel does not see.
 *
 * The agent's sdX names are deliberately NOT the mgr's for osd.3: the
 * kernel renumbered the disks between the two readings. The serial join must
 * not care.
 */
export function hwMachine(): Machine {
  const drive = (bay: string, serial: string, model: string, sizeGB: number, protocol: string, mediaType: string) => ({
    name: bay, location: bay, serialNumber: serial, model, sizeGB, protocol, mediaType, health: 'OK', statusReasons: ['None'],
  })
  return {
    name: HW_MACHINE,
    namespace: 'default',
    address: '',
    nodeRef: HW_NODE,
    powerState: 'On',
    postState: 'FinishedPost',
    indicatorLED: 'Off',
    reachable: true,
    reachableReason: '',
    reachableMessage: '',
    lastProbeAt: null,
    sensorsValidAt: null,
    inventory: {
      manufacturer: 'HPE', model: 'ProLiant', serialNumber: 'SER-CHASSIS', biosVersion: '', bmcFirmware: '',
      processors: [], memoryModules: [], totalMemoryGiB: 0, networkAdapters: [],
      drives: [
        drive('1I:6:1', 'SER-SSD1', 'SSD-A', 1024, 'SATA', 'SSD'),
        drive('1I:6:2', 'SER-SSD2', 'SSD-B', 1024, 'SATA', 'SSD'),
        drive('1I:6:3', 'SER-SYS1', 'EG0300', 300, 'SAS', 'HDD'),
        drive('1I:6:4', 'SER-HDD1', 'HUC10121CLAR1200', 1200, 'SAS', 'HDD'),
        drive('2I:6:5', 'SER-HDD2', 'HUC10121CLAR1200', 1200, 'SAS', 'HDD'),
        drive('2I:6:6', 'SER-HDD3', 'HUC10121CLAR1200', 1200, 'SAS', 'HDD'),
        drive('2I:6:7', 'SER-HDD4', 'HUC10121CLAR1200', 1200, 'SAS', 'HDD'),
        drive('2I:6:8', 'SER-MASK', 'MM1000', 1000, 'SATA', 'HDD'),
      ],
    },
    storage: {
      observedAt: '2026-09-27T12:17:04Z',
      observed: [
        { path: '/dev/sda', serialNumber: 'SER-SYS1', sizeGB: 300, occupancy: 'partitioned' },
        { path: '/dev/sdb', serialNumber: 'SER-HDD1', sizeGB: 1200, occupancy: 'ceph-osd' },
        { path: '/dev/sdc', serialNumber: 'SER-SSD1', sizeGB: 1024, occupancy: 'partitioned' },
        { path: '/dev/sdd', serialNumber: 'SER-SSD2', sizeGB: 1024, occupancy: 'mounted' },
        { path: '/dev/sde', serialNumber: 'SER-HDD2', sizeGB: 1200, occupancy: 'ceph-osd' },
        // osd.3's disk: the mgr says sdf, the agent's later reading says sdg.
        { path: '/dev/sdg', serialNumber: 'SER-HDD3', sizeGB: 1200, occupancy: 'ceph-osd' },
        { path: '/dev/sdf', serialNumber: 'SER-HDD4', sizeGB: 1200, occupancy: 'ceph-osd' },
      ],
      divergences: [
        { serialNumber: 'SER-MASK', reason: 'bmc-only', detail: "bay 2I:6:8: the BMC lists it, the node's kernel does not" },
      ],
    },
    sensors: {
      temperatures: [{ name: '08-HD Max', celsius: 46, upperCritical: 60, health: 'OK' }],
      fans: [],
      powerSupplies: [],
      powerConsumedWatts: 0,
    },
    eventLog: [],
    eventLogCounts: {},
    eventLogTotal: 0,
  } as unknown as Machine
}
