import { describe, it, expect } from 'vitest'
import { buildPoolRows, capacityByClass, daemonQuorum, objectHealth, pgTrouble, setFlags, tallyPgs } from './ceph-pools'
import { healthyProm, setSample } from './testdata/ceph-cluster'

describe('pools', () => {
  it('liste les 10 pools du mgr, pas le seul CephBlockPool', () => {
    const pools = buildPoolRows(healthyProm())
    expect(pools).toHaveLength(10)
    expect(pools.map((p) => p.name)).toContain('store.rgw.buckets.data')
    expect(pools.map((p) => p.name)).toContain('.mgr')
  })

  it('lit la taille de réplication dans la description du mgr', () => {
    const p = buildPoolRows(healthyProm())[1]
    expect(p).toMatchObject({ name: 'replicapool', size: 4, erasure: null })
  })

  // min_size and the CRUSH rule are not exported by the mgr; a fabricated
  // value would be worse than none.
  it('laisse min_size et la règle CRUSH à null, non collectés', () => {
    for (const p of buildPoolRows(healthyProm())) {
      expect(p.minSize).toBeNull()
      expect(p.crushRule).toBeNull()
    }
  })

  it('convertit percent_used (ratio) en pourcentage', () => {
    const prom = healthyProm()
    setSample(prom, 'poolPercent', { pool_id: '2' }, 0.25)
    expect(buildPoolRows(prom)[1].percentUsed).toBe(25)
  })

  it('compte 121 PG clean sur le cluster sain', () => {
    const { cluster } = tallyPgs(healthyProm().pgStates)
    expect(cluster.total).toBe(121)
    expect(cluster.byState.clean).toBe(121)
    expect(buildPoolRows(healthyProm()).every((p) => p.trouble === null)).toBe(true)
  })

  it('marque un pool dégradé en avertissement, avec le décompte', () => {
    const prom = healthyProm()
    setSample(prom, 'pgStates', { __name__: 'ceph_pg_degraded', pool_id: '2' }, 12)
    setSample(prom, 'pgStates', { __name__: 'ceph_pg_undersized', pool_id: '2' }, 3)
    const p = buildPoolRows(prom)[1]
    expect(p.trouble).toBe('warning')
    expect(p.troubleDetail).toContain('12 degraded')
    expect(p.troubleDetail).toContain('3 undersized')
  })

  it('marque critique un pool avec des PG down ou inactifs', () => {
    const prom = healthyProm()
    setSample(prom, 'pgStates', { __name__: 'ceph_pg_down', pool_id: '10' }, 1)
    expect(buildPoolRows(prom)[9].trouble).toBe('critical')
    expect(pgTrouble({ total: 8, byState: { active: 6 } })).toMatchObject({ trouble: 'critical' })
  })
})

describe('flags', () => {
  it('ne rend que les flags posés', () => {
    const prom = healthyProm()
    expect(setFlags(prom.flags)).toEqual([])
    setSample(prom, 'flags', { __name__: 'ceph_osd_flag_norebalance' }, 1)
    setSample(prom, 'flags', { __name__: 'ceph_osd_flag_noout' }, 1)
    expect(setFlags(prom.flags)).toEqual(['noout', 'norebalance'])
  })
})

describe('capacité par classe', () => {
  it('rattache OSD et nœuds à leur classe', () => {
    const c = capacityByClass(healthyProm(), [
      { id: 0, deviceClass: 'ssd', host: 'node-a' },
      { id: 3, deviceClass: 'hdd', host: 'node-hw' },
      { id: 4, deviceClass: 'hdd', host: 'node-hw' },
    ])
    expect(c.map((x) => x.deviceClass)).toEqual(['hdd', 'ssd'])
    expect(c[0]).toMatchObject({ osds: [3, 4], hosts: ['node-hw'] })
  })
})

describe('objets et quorum', () => {
  it('rapporte les objets dégradés aux copies attendues', () => {
    const prom = healthyProm()
    setSample(prom, 'objectsDegraded', {}, 2170)
    const o = objectHealth(prom, buildPoolRows(prom))
    // 121 PG × 100 objects × size 4
    expect(o.copies).toBe(48400)
    expect(o.degradedPct).toBeCloseTo((100 * 2170) / 48400, 5)
  })

  it('ne calcule pas de ratio si une taille manque', () => {
    const prom = healthyProm()
    prom.poolMeta![0].labels.description = 'ec:2+1'
    expect(objectHealth(prom, buildPoolRows(prom)).degradedPct).toBeNull()
  })

  it('sépare les mon hors quorum', () => {
    const prom = healthyProm()
    setSample(prom, 'monQuorum', { ceph_daemon: 'mon.e' }, 0)
    expect(daemonQuorum(prom)).toMatchObject({ monsInQuorum: ['a', 'f'], monsOut: ['e'], mgrActive: ['a'] })
  })
})
