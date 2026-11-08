import { describe, expect, it } from 'vitest'
import type { OsdRow } from './ceph-diagnostic'
import {
  FILL_CRITICAL_PCT,
  FILL_WARN_PCT,
  WRITE_SLOW_MS,
  buildVolumeRows,
  fillLevel,
  pathLine,
  poolOsds,
  slowSharedOsds,
  toAttachmentInfo,
  toBlockPoolInfo,
  toPodClaimInfo,
  toPvInfo,
  toPvcInfo,
  toStorageClassInfo,
  volumeFillFindings,
  type VolumeLoadInputs,
} from './volume-load'
import { collectFindings } from './findings'

function osd(id: number, host: string, commitMs: number | null, over: Partial<OsdRow> = {}): OsdRow {
  return {
    id,
    name: `osd.${id}`,
    deviceClass: 'hdd',
    host,
    up: true,
    in: true,
    pgs: 10,
    bytesTotal: null,
    bytesUsed: null,
    usePct: null,
    applyMs: commitMs,
    commitMs,
    slowOps: null,
    bluestoreSlow: null,
    crushWeight: null,
    devices: ['sdb'],
    deviceIds: [],
    pod: null,
    state: 'ok',
    stateDetail: '',
    disk: { kind: 'physical', serial: `SER${id}`, machine: host, bay: null, model: 'ST500', osPath: null, hostConflict: false, alsoListedBy: [] },
    ...over,
  } as OsdRow
}

// The cluster this was written against: three hosts, pool size 3, failure
// domain host. cp has one OSD (a zvol, slow), the two others have several.
const OSDS = [
  osd(0, 'cp', 106),
  osd(1, 'g9', 8),
  osd(3, 'g9', 9),
  osd(2, 'r320', 18),
  osd(7, 'r320', 62),
]

const sample = (labels: Record<string, string>, value: number) => ({ labels, value })

function inputs(over: Partial<VolumeLoadInputs> = {}): VolumeLoadInputs {
  return {
    pvcs: [
      toPvcInfo({
        metadata: { name: 'pgdata-neura-db-0', namespace: 'neura' },
        spec: { storageClassName: 'ceph-block', volumeName: 'pv-db', resources: { requests: { storage: '10Gi' } } },
        status: { phase: 'Bound', capacity: { storage: '10Gi' } },
      }),
      toPvcInfo({
        metadata: { name: 'prometheus-data', namespace: 'monitoring' },
        spec: { storageClassName: 'local-path', volumeName: 'pv-prom' },
        status: { phase: 'Bound', capacity: { storage: '16Gi' } },
      }),
      toPvcInfo({
        metadata: { name: 'pending', namespace: 'neura' },
        spec: { storageClassName: 'ceph-block', resources: { requests: { storage: '1Gi' } } },
        status: { phase: 'Pending' },
      }),
    ],
    pvs: [
      toPvInfo({
        metadata: { name: 'pv-db' },
        spec: {
          storageClassName: 'ceph-block',
          persistentVolumeReclaimPolicy: 'Retain',
          csi: { driver: 'rook-ceph.rbd.csi.ceph.com', volumeAttributes: { pool: 'replicapool' } },
        },
      }),
      toPvInfo({
        metadata: { name: 'pv-prom' },
        spec: {
          storageClassName: 'local-path',
          persistentVolumeReclaimPolicy: 'Delete',
          hostPath: { path: '/var/lib/rancher/k3s/storage/pvc-prom' },
          nodeAffinity: {
            required: { nodeSelectorTerms: [{ matchExpressions: [{ key: 'kubernetes.io/hostname', operator: 'In', values: ['g9'] }] }] },
          },
        },
      }),
    ],
    storageClasses: [
      toStorageClassInfo({ metadata: { name: 'ceph-block' }, provisioner: 'rook-ceph.rbd.csi.ceph.com', parameters: { pool: 'replicapool' } }),
      toStorageClassInfo({ metadata: { name: 'local-path' }, provisioner: 'rancher.io/local-path' }),
    ],
    blockPools: [toBlockPoolInfo({ metadata: { name: 'replicapool' }, spec: { failureDomain: 'host', replicated: { size: 3 } } })],
    pods: [
      toPodClaimInfo({
        metadata: { name: 'neura-db-0', namespace: 'neura', ownerReferences: [{ kind: 'StatefulSet', name: 'neura-db', controller: true }] },
        spec: { nodeName: 'r320', volumes: [{ persistentVolumeClaim: { claimName: 'pgdata-neura-db-0' } }] },
        status: { phase: 'Running' },
      })!,
      toPodClaimInfo({
        metadata: { name: 'prom-abc-1', namespace: 'monitoring', ownerReferences: [{ kind: 'ReplicaSet', name: 'prom-abc', controller: true }] },
        spec: { nodeName: 'g9', volumes: [{ persistentVolumeClaim: { claimName: 'prometheus-data' } }] },
        status: { phase: 'Running' },
      })!,
    ],
    replicaSetOwners: new Map([['monitoring/prom-abc', { kind: 'Deployment', name: 'prometheus' }]]),
    attachments: [],
    osds: OSDS,
    mgrPools: new Map([['replicapool', { crushRule: '1 hote_x3', size: 3 }]]),
    osdsKnown: true,
    prom: {
      volUsed: [
        sample({ namespace: 'monitoring', persistentvolumeclaim: 'prometheus-data' }, 16 * 0.97 * 2 ** 30),
        sample({ namespace: 'neura', persistentvolumeclaim: 'pgdata-neura-db-0' }, 2 * 2 ** 30),
      ],
      volCapacity: [
        sample({ namespace: 'monitoring', persistentvolumeclaim: 'prometheus-data' }, 16 * 2 ** 30),
        sample({ namespace: 'neura', persistentvolumeclaim: 'pgdata-neura-db-0' }, 10 * 2 ** 30),
      ],
      osdCommitWorst24h: [sample({ ceph_daemon: 'osd.0' }, 240), sample({ ceph_daemon: 'osd.7' }, 300)],
    },
    ...over,
  }
}

const row = (rows: ReturnType<typeof buildVolumeRows>, key: string) => rows.find((r) => r.key === key)!

describe('buildVolumeRows', () => {
  it('walks a Ceph claim to its pool, rule, OSDs, disks and hosts', () => {
    const r = row(buildVolumeRows(inputs()), 'neura/pgdata-neura-db-0')
    expect(r.consumers.map((c) => c.workload)).toEqual(['StatefulSet/neura-db'])
    expect(r.nodes).toEqual(['r320'])
    expect(r.reclaimPolicy).toBe('Retain')
    expect(r.path.kind).toBe('ceph')
    if (r.path.kind !== 'ceph') return
    expect(r.path.pool).toBe('replicapool')
    expect(r.path.crushRule).toBe('1 hote_x3')
    expect(r.path.hosts).toEqual(['cp', 'g9', 'r320'])
    expect(r.path.osds.map((o) => o.id)).toEqual([0, 1, 2, 3, 7])
    expect(r.path.osds.find((o) => o.id === 0)!.disk).toBe('ST500 SER0')
  })

  it('names the slowest OSD as the likely write latency, and the worst 24 h separately', () => {
    const r = row(buildVolumeRows(inputs()), 'neura/pgdata-neura-db-0')
    expect(r.likelyWrite).toEqual({ osd: 0, ms: 106 })
    // A different OSD may own the worst five minutes of the day.
    expect(r.worst24h).toEqual({ osd: 7, ms: 300 })
  })

  it('resolves a Deployment through its ReplicaSet and pins a local volume to its node', () => {
    const r = row(buildVolumeRows(inputs()), 'monitoring/prometheus-data')
    expect(r.consumers.map((c) => c.workload)).toEqual(['Deployment/prometheus'])
    expect(r.path).toEqual({ kind: 'local', node: 'g9', path: '/var/lib/rancher/k3s/storage/pvc-prom' })
    expect(r.likelyWrite).toBeNull()
    expect(r.fill).toBe('critical')
  })

  it('keeps an unscraped claim at unknown fill, never 0 %', () => {
    const r = row(buildVolumeRows(inputs({ prom: null })), 'neura/pgdata-neura-db-0')
    expect(r.usePct).toBeNull()
    expect(r.fill).toBeNull()
    expect(r.sizeBytes).toBe(10 * 2 ** 30)
  })

  it('lists a pending claim with no volume, and sorts the fullest first', () => {
    const rows = buildVolumeRows(inputs())
    expect(rows[0]!.key).toBe('monitoring/prometheus-data')
    const p = row(rows, 'neura/pending')
    expect(p.consumers).toEqual([])
    expect(p.path.kind).toBe('ceph')
    expect(p.sizeBytes).toBe(2 ** 30)
  })

  it('says so when the pool is not declared by a CephBlockPool, and lists every OSD', () => {
    const r = row(buildVolumeRows(inputs({ blockPools: [] })), 'neura/pgdata-neura-db-0')
    expect(r.path.kind === 'ceph' && r.path.declared).toBe(false)
    expect(pathLine(r.path)).toContain('not declared')
    expect(r.path.kind === 'ceph' && r.path.osds.length).toBe(OSDS.length)
  })

  it('uses the attachment node when no pod mounts the claim', () => {
    const r = row(
      buildVolumeRows(inputs({ pods: [], attachments: [toAttachmentInfo({ spec: { nodeName: 'g9', source: { persistentVolumeName: 'pv-db' } }, status: { attached: true } })!] })),
      'neura/pgdata-neura-db-0',
    )
    expect(r.nodes).toEqual(['g9'])
  })
})

describe('unknowns and special pools', () => {
  it('says the OSDs are not collected rather than showing an empty path', () => {
    const r = row(buildVolumeRows(inputs({ osds: [], osdsKnown: false })), 'neura/pgdata-neura-db-0')
    expect(pathLine(r.path)).toContain('OSDs not collected')
    expect(pathLine(r.path)).not.toContain('0 OSD')
  })

  it('follows the dataPool of an erasure-coded RBD image', () => {
    const r = row(
      buildVolumeRows(
        inputs({
          pvs: [
            toPvInfo({
              metadata: { name: 'pv-db' },
              spec: { csi: { driver: 'rook-ceph.rbd.csi.ceph.com', volumeAttributes: { pool: 'replicapool', dataPool: 'ec-data' } } },
            }),
          ],
        }),
      ),
      'neura/pgdata-neura-db-0',
    )
    expect(r.path.kind === 'ceph' && r.path.pool).toBe('ec-data')
    expect(pathLine(r.path)).toContain('rbd EC')
  })

  it('ignores a finished pod as a consumer', () => {
    expect(
      toPodClaimInfo({
        metadata: { name: 'job-x', namespace: 'n' },
        spec: { volumes: [{ persistentVolumeClaim: { claimName: 'c' } }] },
        status: { phase: 'Succeeded' },
      }),
    ).toBeNull()
  })
})

describe('poolOsds', () => {
  it('marks the only OSD of a host as on every write when copies = hosts', () => {
    const { osds } = poolOsds(OSDS, { deviceClass: null, failureDomain: 'host', size: 3 }, new Map())
    expect(osds.filter((o) => o.onEveryWrite).map((o) => o.id)).toEqual([0])
  })

  it('marks nothing when there are more hosts than copies', () => {
    const { osds } = poolOsds([...OSDS, osd(9, 'r730', 5)], { deviceClass: null, failureDomain: 'host', size: 3 }, new Map())
    expect(osds.some((o) => o.onEveryWrite)).toBe(false)
  })

  it('filters by device class and leaves out OSDs that are out', () => {
    const mixed = [...OSDS, osd(10, 'g9', 1, { deviceClass: 'ssd' }), osd(11, 'cp', 1, { in: false })]
    const { osds } = poolOsds(mixed, { deviceClass: 'ssd', failureDomain: 'host', size: 3 }, new Map())
    expect(osds.map((o) => o.id)).toEqual([10])
    const all = poolOsds(mixed, { deviceClass: null, failureDomain: 'host', size: 3 }, new Map())
    expect(all.osds.map((o) => o.id)).not.toContain(11)
  })

  it('does not claim every-write for a non-host failure domain', () => {
    const { osds } = poolOsds(OSDS, { deviceClass: null, failureDomain: 'osd', size: 3 }, new Map())
    expect(osds.some((o) => o.onEveryWrite)).toBe(false)
  })
})

describe('slowSharedOsds', () => {
  it('raises the slow OSD every write waits for, with its pools and volume count', () => {
    expect(slowSharedOsds(buildVolumeRows(inputs()))).toEqual([
      { osd: 0, host: 'cp', commitMs: 106, pools: ['replicapool'], volumes: 1 },
    ])
  })

  it('does not count a claim with no volume yet', () => {
    const p = row(buildVolumeRows(inputs()), 'neura/pending')
    expect(p.bound).toBe(false)
    expect(p.likelyWrite).toBeNull()
  })

  it('takes the size from the mgr, so an undeclared CephFS data pool still raises the banner', () => {
    const rows = buildVolumeRows(
      inputs({
        pvcs: [toPvcInfo({ metadata: { name: 'shared', namespace: 'apps' }, spec: { storageClassName: 'cephfs', volumeName: 'pv-fs' }, status: { phase: 'Bound' } })],
        pvs: [
          toPvInfo({
            metadata: { name: 'pv-fs' },
            spec: { csi: { driver: 'rook-ceph.cephfs.csi.ceph.com', volumeAttributes: { pool: 'ceph-filesystem-replicated' } } },
          }),
        ],
        mgrPools: new Map([['ceph-filesystem-replicated', { crushRule: '2 fs_rule', size: 3 }]]),
      }),
    )
    expect(rows[0]!.path.kind === 'ceph' && rows[0]!.path.declared).toBe(false)
    expect(slowSharedOsds(rows).map((s) => [s.osd, s.pools])).toEqual([[0, ['ceph-filesystem-replicated']]])
  })

  it('a host whose other OSD is out leaves the remaining one on every write', () => {
    const outOne = OSDS.map((o) => (o.id === 3 ? osd(3, 'g9', 9, { in: false }) : o.id === 1 ? osd(1, 'g9', 70) : o))
    expect(slowSharedOsds(buildVolumeRows(inputs({ osds: outOne }))).map((s) => s.osd)).toEqual([0, 1])
  })

  it('stays quiet when that OSD is fast', () => {
    const fast = OSDS.map((o) => (o.id === 0 ? osd(0, 'cp', WRITE_SLOW_MS - 1) : o))
    expect(slowSharedOsds(buildVolumeRows(inputs({ osds: fast })))).toEqual([])
  })
})

describe('fill', () => {
  it('bands at the documented thresholds', () => {
    expect(fillLevel(null)).toBeNull()
    expect(fillLevel(FILL_WARN_PCT - 0.1)).toBe('ok')
    expect(fillLevel(FILL_WARN_PCT)).toBe('warning')
    expect(fillLevel(FILL_CRITICAL_PCT)).toBe('critical')
  })

  it('feeds the Overview only volumes past the warning threshold', () => {
    const f = volumeFillFindings(inputs().prom)
    expect(f.map((v) => [v.key, v.severity])).toEqual([['monitoring/prometheus-data', 'critical']])
    const findings = collectFindings({ volumes: f })
    expect(findings).toContainEqual(
      expect.objectContaining({ key: 'volume-fill-monitoring/prometheus-data', tone: 'destructive', screen: 'storage', tab: 'storage-load' }),
    )
  })

  it('ignores a volume with no capacity figure', () => {
    expect(volumeFillFindings({ volUsed: [sample({ namespace: 'a', persistentvolumeclaim: 'b' }, 100)], volCapacity: [] })).toEqual([])
  })
})

describe('converters', () => {
  it('reads a CephBlockPool with Rook defaults', () => {
    expect(toBlockPoolInfo({ metadata: { name: 'p' } })).toEqual({ poolName: 'p', deviceClass: null, failureDomain: 'host', size: null })
    expect(toBlockPoolInfo({ metadata: { name: 'cr' }, spec: { name: 'real', deviceClass: 'ssd' } }).poolName).toBe('real')
  })

  it('ignores pods without claims and attachments without a PV', () => {
    expect(toPodClaimInfo({ metadata: { name: 'x', namespace: 'n' }, spec: { volumes: [{}] } })).toBeNull()
    expect(toAttachmentInfo({ spec: { nodeName: 'n' } })).toBeNull()
  })

  it('pins a PV only to a single hostname', () => {
    const two = toPvInfo({
      metadata: { name: 'x' },
      spec: { nodeAffinity: { required: { nodeSelectorTerms: [{ matchExpressions: [{ key: 'kubernetes.io/hostname', values: ['a', 'b'] }] }] } } },
    })
    expect(two.pinnedNode).toBeNull()
  })
})
