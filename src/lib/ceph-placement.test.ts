import { describe, it, expect } from 'vitest'
import { cephCapacityEntry, crushWeights, placementOf, placementSourceLine, poolPlacement, type CephPlacement } from './ceph-placement'
import { capacityLine } from './storage'
import { buildPoolRows } from './ceph-pools'
import { attentionItems } from './storage-attention'
import { buildOsdRows } from './ceph-diagnostic'
import { daemonQuorum, objectHealth, setFlags } from './ceph-pools'
import { mdArrays } from './disk-inventory'
import { healthyPods, healthyProm, hwMachine, setSample } from './testdata/ceph-cluster'

// status.cephPlacement as the controller writes it from the anonymised
// dashboard fixtures (internal/cephmgr/testdata).
function collected(): CephPlacement {
  return {
    state: 'Collected',
    collectedAt: '2026-09-27T21:00:00Z',
    pools: [
      { name: 'replicapool', size: 4, minSize: 2, crushRule: '20 hdd2_ssd2' },
      { name: '.mgr', size: 4, minSize: 2, crushRule: '20 hdd2_ssd2' },
    ],
    osds: [0, 1, 2].map((id) => ({ id, crushWeight: '2.0000', deviceClass: 'ssd' })).concat(
      [3, 4, 5, 6].map((id) => ({ id, crushWeight: '1.0900', deviceClass: 'hdd' })),
    ),
  }
}

const entry = (over: Partial<{ type: string; usable: string; placement: CephPlacement | null }> = {}) => ({
  type: over.type ?? 'ceph-rbd',
  capacity: { usable: over.usable ?? '', used: '', raw: '10Ti' },
  cephPlacement: over.placement ?? null,
})

describe('capacité utilisable par la FrameStorage', () => {
  // The prod entry once applied: raw 11398044549120 B / replication 4,
  // as the controller computes it (bytesToQuantityString).
  it('affiche l-utilisable dès qu-une entrée ceph le porte, et le brut seulement sans elle', () => {
    const withEntry = cephCapacityEntry([entry({ type: 'local-path', usable: '1Ti' }), entry({ usable: '2849511137280' })])
    expect(capacityLine(withEntry!.capacity)).toBe('2849511137280 usable (raw 10Ti)')
    expect(cephCapacityEntry([entry({ type: 'local-path', usable: '1Ti' })])).toBeUndefined()
  })
})

describe('placement ceph-mgr', () => {
  it('lit poids CRUSH et règles, et laisse absent ce que la lecture ne couvre pas', () => {
    const p = collected()
    expect(crushWeights(p).get(3)).toBe(1.09)
    expect(crushWeights(p).has(7)).toBe(false)
    const rows = buildPoolRows(healthyProm(), poolPlacement(p))
    expect(rows.find((r) => r.name === 'replicapool')).toMatchObject({ minSize: 2, crushRule: '20 hdd2_ssd2' })
    expect(rows.find((r) => r.name === 'store.rgw.log')).toMatchObject({ minSize: null, crushRule: null })
  })

  it('préfère une lecture réussie parmi les entrées ceph', () => {
    const failed: CephPlacement = { state: 'Error', message: 'x' }
    expect(placementOf([entry({ placement: failed }), entry({ placement: collected() })])?.state).toBe('Collected')
    expect(placementOf([entry({ type: 'local-path', placement: collected() })])).toBeNull()
  })

  it('dit d-où viennent les chiffres, ou pourquoi ils manquent', () => {
    expect(placementSourceLine(null)).toContain('not collected (no ceph FrameStorage entry)')
    expect(placementSourceLine({ state: 'NotConfigured' })).toContain('no read-only dashboard account')
    expect(placementSourceLine({ state: 'AuthRefused', message: 'x', collectedAt: 'T' })).toBe(
      'min_size, CRUSH rule and CRUSH weight: the dashboard refused the account — no retry until the account in the Secret changes; showing the read of T',
    )
    expect(placementSourceLine({ state: 'Error', message: 'dashboard login: HTTP 401', collectedAt: 'T' })).toBe(
      'min_size, CRUSH rule and CRUSH weight: last read failed — dashboard login: HTTP 401; showing the read of T',
    )
    expect(placementSourceLine({ state: 'Error', message: 'partial read', poolsReadAt: 'T2', osdsReadAt: 'T1' })).toBe(
      'min_size, CRUSH rule and CRUSH weight: last read failed — partial read; lists as read: pools T2, OSDs T1',
    )
  })
})

describe('bandeau et poids CRUSH', () => {
  function items(weights?: Map<number, number>) {
    const prom = healthyProm()
    for (const id of [3, 4, 5, 6]) setSample(prom, 'osdPgs', { ceph_daemon: `osd.${id}` }, 0)
    const pools = buildPoolRows(prom)
    const machine = hwMachine()
    machine.storage!.divergences = []
    return attentionItems({
      promAvailable: true,
      osds: buildOsdRows({ prom, pods: healthyPods(), machines: [machine], now: 0, crushWeights: weights }),
      pools,
      flags: setFlags(prom.flags),
      healthDetails: {},
      quorum: daemonQuorum(prom),
      objects: objectHealth(prom, pools),
      mdArrays: mdArrays(prom.mdDisks, prom.mdRequired, prom.mdState),
      machines: [machine],
      frameStorageCount: 1,
    }).find((i) => i.code === 'osd-no-pg')!.text
  }

  it('dit « poids 0 » quand le mgr le dit, « règle » quand le poids est positif, et ne tranche pas sans lecture', () => {
    expect(items()).toContain('CRUSH weight 0 or not selected by any rule? (weight not collected)')
    expect(items(new Map([[3, 0], [4, 0], [5, 0], [6, 0]]))).toContain('CRUSH weight 0: they take no data')
    expect(items(new Map([[3, 1.09], [4, 1.09], [5, 1.09], [6, 1.09]]))).toContain("CRUSH weight 1.09 — not selected by any pool's CRUSH rule")
  })
})
