import { describe, it, expect } from 'vitest'
import { attentionItems, undeclaredOsdDiskItems, unreadableDiagnosticItems, type AttentionInputs } from './storage-attention'
import type { DiskRow } from './disk-inventory'
import { buildOsdRows } from './ceph-diagnostic'
import { buildPoolRows, daemonQuorum, objectHealth, setFlags } from './ceph-pools'
import { mdArrays } from './disk-inventory'
import { healthyPods, healthyProm, hwMachine, osdPod, setSample } from './testdata/ceph-cluster'

const NOW = Date.parse('2026-09-27T12:20:00Z')

function inputs(prom = healthyProm(), pods = healthyPods(), over: Partial<AttentionInputs> = {}): AttentionInputs {
  const machine = hwMachine()
  machine.storage!.divergences = []
  const pools = buildPoolRows(prom)
  return {
    promAvailable: true,
    osds: buildOsdRows({ prom, pods, machines: [machine], now: NOW }),
    pools,
    flags: setFlags(prom.flags),
    healthDetails: {},
    quorum: daemonQuorum(prom),
    objects: objectHealth(prom, pools),
    mdArrays: mdArrays(prom.mdDisks, prom.mdRequired, prom.mdState),
    machines: [machine],
    frameStorageCount: 1,
    ...over,
  }
}

const codes = (i: AttentionInputs) => attentionItems(i).map((a) => a.code)

describe('bandeau À traiter', () => {
  it('reste vide sur un cluster sain', () => {
    expect(attentionItems(inputs())).toEqual([])
  })

  it('dit que Prometheus manque au lieu de se taire', () => {
    expect(codes(inputs(healthyProm(), healthyPods(), { promAvailable: false }))).toContain('prometheus-unavailable')
  })

  it('nomme les flags posés', () => {
    const prom = healthyProm()
    setSample(prom, 'flags', { __name__: 'ceph_osd_flag_noout' }, 1)
    setSample(prom, 'flags', { __name__: 'ceph_osd_flag_norebalance' }, 1)
    const item = attentionItems(inputs(prom)).find((a) => a.code === 'osd-flags')
    expect(item?.text).toBe('Flags set: noout, norebalance')
    expect(item?.severity).toBe('warning')
  })

  // The migration state of 27/09: four HDD OSDs up and in, holding nothing.
  it('signale les OSD up/in sans aucun PG, groupés', () => {
    const prom = healthyProm()
    for (const id of [3, 4, 5, 6]) setSample(prom, 'osdPgs', { ceph_daemon: `osd.${id}` }, 0)
    const item = attentionItems(inputs(prom)).find((a) => a.code === 'osd-no-pg')
    expect(item?.text).toContain('4 OSD without any PG: osd.3-6')
    expect(item?.text).toContain('hdd')
  })

  it('ne compte pas comme vide un OSD dont le nombre de PG est inconnu', () => {
    const prom = healthyProm()
    prom.osdPgs = []
    expect(codes(inputs(prom))).not.toContain('osd-no-pg')
  })

  it('attribue les slow ops BlueStore aux OSD dont le compteur bouge', () => {
    const prom = healthyProm()
    setSample(prom, 'bluestoreSlow', { ceph_daemon: 'osd.0' }, 2)
    const details = { BLUESTORE_SLOW_OP_ALERT: { message: '1 OSD(s) experiencing slow operations in BlueStore' } }
    const item = attentionItems(inputs(prom, healthyPods(), { healthDetails: details })).find((a) => a.code === 'slow-ops')
    expect(item?.text).toContain('→ osd.0')
  })

  it('dit quand les slow ops ne sont attribuées à aucun OSD', () => {
    const details = { BLUESTORE_SLOW_OP_ALERT: { message: '3 OSD(s) experiencing slow operations in BlueStore' } }
    const item = attentionItems(inputs(healthyProm(), healthyPods(), { healthDetails: details })).find((a) => a.code === 'slow-ops')
    expect(item?.text).toContain('not attributed')
  })

  it('remonte un pool dégradé en le nommant', () => {
    const prom = healthyProm()
    setSample(prom, 'pgStates', { __name__: 'ceph_pg_degraded', pool_id: '2' }, 5)
    const item = attentionItems(inputs(prom)).find((a) => a.code === 'pool-pg-trouble')
    expect(item?.text).toBe('Pool replicapool: 5 degraded')
  })

  it('met un OSD down avant tout le reste', () => {
    const prom = healthyProm()
    setSample(prom, 'osdUp', { ceph_daemon: 'osd.2' }, 0)
    setSample(prom, 'flags', { __name__: 'ceph_osd_flag_noscrub' }, 1)
    const items = attentionItems(inputs(prom))
    expect(items[0]).toMatchObject({ severity: 'critical', code: 'osd-down' })
  })

  it('signale un OSD instable avec l-état de son pod', () => {
    const pods = healthyPods()
    pods[0] = osdPod(0, { phase: 'Pending', ready: false, initDone: 1, initTotal: 4, waiting: 'init activate' })
    const item = attentionItems(inputs(healthyProm(), pods)).find((a) => a.code === 'osd-unstable')
    expect(item?.text).toContain('osd.0 (node-a) unstable — pod in init 1/4')
  })

  it('remonte les divergences BMC ↔ OS sans langage de panne', () => {
    const items = attentionItems(inputs(healthyProm(), healthyPods(), { machines: [hwMachine()] }))
    const div = items.find((a) => a.code === 'disk-divergence')
    expect(div?.text).toContain('SER-MASK')
    expect(div?.text.toLowerCase()).not.toContain('fail')
    expect(div?.severity).toBe('info')
  })

  it('signale un miroir md dégradé', () => {
    const prom = healthyProm()
    setSample(prom, 'mdDisks', { device: 'md0', state: 'active' }, 1)
    setSample(prom, 'mdDisks', { device: 'md0', state: 'failed' }, 1)
    expect(codes(inputs(prom))).toContain('md-degraded')
  })

  it('dit qu-aucune FrameStorage n-est déclarée, mais pas quand la liste est inconnue', () => {
    expect(codes(inputs(healthyProm(), healthyPods(), { frameStorageCount: 0 }))).toContain('no-framestorage')
    expect(codes(inputs(healthyProm(), healthyPods(), { frameStorageCount: null }))).not.toContain('no-framestorage')
  })
})

describe('disque d-OSD absent de la liste du CephCluster', () => {
  it('ne signale que les disques d-OSD au verdict négatif', () => {
    const base = { osd: { name: 'osd.4' }, bay: '2I:6:7', serial: 'SER-HDD4', byIdPath: '/dev/disk/by-id/wwn-x' }
    const rows = [
      { ...base, declaredInCeph: false },
      { ...base, serial: 'SER-HDD3', declaredInCeph: true },
      { ...base, serial: 'SER-HDD1', declaredInCeph: null },
      { ...base, serial: 'SER-SSD1', osd: null, declaredInCeph: false },
    ] as unknown as DiskRow[]
    const items = undeclaredOsdDiskItems('srv-hw', rows)
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ code: 'osd-disk-undeclared', severity: 'warning' })
    expect(items[0].text).toContain('osd.4 on srv-hw 2I:6:7 (SER-HDD4)')
  })
})

describe('absence de données', () => {
  // Prometheus answers but the ceph-mgr series are gone (mgr restarting,
  // scrape broken): the OSDs are only known from their pods.
  it('signale les OSD sans aucune lecture du mgr au lieu d-un bandeau vide', () => {
    const prom = healthyProm()
    prom.osdMeta = []
    prom.osdUp = []
    prom.osdIn = []
    const items = attentionItems(inputs(prom))
    const item = items.find((a) => a.code === 'osd-unknown')
    expect(item?.text).toContain('7 OSD without any ceph-mgr reading: osd.0-6')
    expect(items.length).toBeGreaterThan(0)
  })

  it('ne double pas l-alerte Prometheus injoignable par une ligne par OSD inconnu', () => {
    const prom = healthyProm()
    prom.osdUp = []
    const items = attentionItems(inputs(prom, healthyPods(), { promAvailable: false }))
    expect(items.map((a) => a.code)).toContain('prometheus-unavailable')
    expect(items.map((a) => a.code)).not.toContain('osd-unknown')
  })

  it('rend une ligne quand la lecture du diagnostic échoue', () => {
    expect(unreadableDiagnosticItems('HTTP 503')).toEqual([
      { severity: 'warning', code: 'diagnostic-unreadable', text: 'Storage diagnostic unreadable: HTTP 503 — state unknown' },
    ])
  })

  it('nomme un disque sans série et un doublon de série', () => {
    const m = hwMachine()
    m.storage!.divergences = []
    m.inventory!.drives.push({ ...m.inventory!.drives[0], serialNumber: '', location: '2I:6:9' })
    const clone = hwMachine()
    clone.name = 'srv-clone'
    clone.storage!.divergences = []
    const codes = attentionItems(inputs(healthyProm(), healthyPods(), { machines: [m, clone] })).map((a) => a.text)
    expect(codes.some((t) => t.includes('bay 2I:6:9 reported without a serial'))).toBe(true)
    expect(codes.some((t) => t.startsWith('Serial SER-HDD3 listed by several machines: srv-hw, srv-clone'))).toBe(true)
  })
})

describe('OSD redémarré récemment', () => {
  it('dit depuis quand il est stable, en information, sans l-appeler instable', () => {
    const pods = healthyPods()
    pods[3] = osdPod(3, { restarts: 5, lastTerminationAt: '2026-09-27T11:33:00Z', startedAt: '2026-09-27T11:33:00Z' })
    const items = attentionItems(inputs(healthyProm(), pods))
    const item = items.find((a) => a.code === 'osd-settling')
    expect(item).toMatchObject({ severity: 'info', text: 'osd.3 (node-hw) restarted recently: stable for 47 min, 5 restart(s) in total — watched until 1 h without a restart' })
    expect(items.map((a) => a.code)).not.toContain('osd-unstable')
  })
})
