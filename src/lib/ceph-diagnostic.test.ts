import { describe, it, expect } from 'vitest'
import {
  buildOsdRows,
  currentOsdPods,
  linkOsdDisk,
  osdRange,
  parseDeviceIds,
  podInstability,
  promAnswered,
  serialFromDeviceId,
  toOsdPod,
} from './ceph-diagnostic'
import { healthyPods, healthyProm, hwMachine, osdPod, setSample, HW_MACHINE, HW_NODE } from './testdata/ceph-cluster'

const NOW = Date.parse('2026-09-27T12:20:00Z')

function rows(over: { prom?: ReturnType<typeof healthyProm>; pods?: ReturnType<typeof healthyPods> } = {}) {
  return buildOsdRows({ prom: over.prom ?? healthyProm(), pods: over.pods ?? healthyPods(), machines: [hwMachine()], now: NOW })
}

describe('buildOsdRows', () => {
  it('rend une ligne par OSD, triée par id, avec nœud et classe', () => {
    const r = rows()
    expect(r.map((o) => o.name)).toEqual(['osd.0', 'osd.1', 'osd.2', 'osd.3', 'osd.4', 'osd.5', 'osd.6'])
    expect(r[3]).toMatchObject({ host: HW_NODE, deviceClass: 'hdd', up: true, in: true, pgs: 57, state: 'up-in' })
  })

  it('calcule le %USE sur les octets de l-OSD', () => {
    const r = rows()
    expect(r[0].usePct).toBeCloseTo((100 * 33e9) / (2 * 1024 ** 4), 5)
  })

  // Rule 1 of the module: a CRUSH weight nobody collected is not zero.
  it('ne publie jamais un poids CRUSH à 0 faute de source', () => {
    for (const o of rows()) expect(o.crushWeight).toBeNull()
  })

  it('laisse null une métrique absente au lieu de 0', () => {
    const prom = healthyProm()
    prom.osdApply = prom.osdApply!.filter((x) => x.labels.ceph_daemon !== 'osd.2')
    expect(rows({ prom })[2].applyMs).toBeNull()
  })

  it('marque down un OSD que Ceph donne down, même si son pod tourne', () => {
    const prom = healthyProm()
    setSample(prom, 'osdUp', { ceph_daemon: 'osd.1' }, 0)
    expect(rows({ prom })[1].state).toBe('down')
  })

  it('marque instable un OSD up dont le pod est en init', () => {
    const pods = healthyPods()
    pods[0] = osdPod(0, { phase: 'Pending', ready: false, initDone: 1, initTotal: 4, waiting: 'init expand-bluefs' })
    const r = rows({ pods })[0]
    expect(r.state).toBe('unstable')
    expect(r.stateDetail).toContain('init 1/4')
  })

  it('marque out un OSD sorti du placement', () => {
    const prom = healthyProm()
    setSample(prom, 'osdIn', { ceph_daemon: 'osd.4' }, 0)
    expect(rows({ prom })[4].state).toBe('out')
  })
})

describe('jointure OSD ↔ disque', () => {
  it('lit la série dans device_ids du mgr', () => {
    expect(parseDeviceIds('sdf=HITACHI_HUC10121CLAR1200_SER-HDD3')).toEqual(['HITACHI_HUC10121CLAR1200_SER-HDD3'])
    expect(parseDeviceIds('sda=A_B_S1,sdb=C_D_S2')).toEqual(['A_B_S1', 'C_D_S2'])
    expect(parseDeviceIds(undefined)).toEqual([])
  })

  it('préfère la série connue la plus longue, même avec des _ dans la série', () => {
    expect(serialFromDeviceId('VENDOR_MODEL_AB_12', ['12', 'AB_12'])).toBe('AB_12')
    expect(serialFromDeviceId('VENDOR_MODEL_XYZ')).toBe('XYZ')
  })

  // The load-bearing test of the whole join. The mgr says osd.3 is on sdf;
  // the agent's reading (later boot order) has SER-HDD4 on sdf and osd.3's
  // disk, SER-HDD3, on sdg. A join on sdX would put osd.3 in bay 2I:6:7.
  it('joint par numéro de série, jamais par /dev/sdX', () => {
    const osd3 = rows()[3]
    expect(osd3.devices).toEqual(['sdf'])
    expect(osd3.disk).toMatchObject({
      kind: 'physical',
      serial: 'SER-HDD3',
      machine: HW_MACHINE,
      bay: '2I:6:6',
      osPath: '/dev/sdg',
      hostConflict: false,
    })
  })

  it('reconnaît un disque virtuel QEMU sur un nœud VM', () => {
    expect(rows()[0].disk).toEqual({ kind: 'virtual', serial: 'drive-scsi2', drive: 'drive-scsi2' })
  })

  it('signale une série trouvée sur une machine qui n-est pas le nœud de l-OSD', () => {
    const link = linkOsdDisk(['HITACHI_X_SER-HDD1'], 'another-node', [hwMachine()])
    expect(link).toMatchObject({ kind: 'physical', machine: HW_MACHINE, hostConflict: true })
  })

  it('garde la série quand aucune FrameMachine ne la connaît', () => {
    expect(linkOsdDisk(['V_M_SER-ELSEWHERE'], 'x', [hwMachine()])).toMatchObject({
      kind: 'physical',
      serial: 'SER-ELSEWHERE',
      machine: null,
      bay: null,
    })
  })

  it('dit inconnu quand le mgr ne donne aucun identifiant', () => {
    expect(linkOsdDisk([], 'x', []).kind).toBe('unknown')
  })
})

describe('pods OSD', () => {
  // Rook leaves Completed pods of older ReplicaSets behind; one of those,
  // newer than nothing, must never stand for the OSD.
  it('préfère le pod vivant aux pods Completed laissés par Rook', () => {
    const old = osdPod(1, { phase: 'Succeeded', createdAt: '2026-09-28T00:00:00Z', name: 'old' })
    const live = osdPod(1, { phase: 'Running', createdAt: '2026-09-27T01:00:00Z', name: 'live' })
    expect(currentOsdPods([old, live]).get(1)?.name).toBe('live')
    expect(currentOsdPods([live, old]).get(1)?.name).toBe('live')
  })

  it('ne compte un redémarrage récent comme instabilité que si le pod ne tourne pas', () => {
    const recentRunning = osdPod(0, { restarts: 1, lastTerminationAt: '2026-09-27T12:00:00Z' })
    const recentDown = osdPod(0, { restarts: 1, lastTerminationAt: '2026-09-27T12:00:00Z', ready: false })
    expect(podInstability(recentRunning, NOW)).toBe('')
    expect(podInstability(recentDown, NOW)).toContain('1 restart(s), last 20 min ago')
  })

  it('lit un pod Kubernetes en init', () => {
    const p = toOsdPod({
      metadata: { name: 'rook-ceph-osd-0-x', labels: { 'ceph-osd-id': '0' }, creationTimestamp: '2026-09-27T10:00:00Z' },
      spec: { nodeName: 'node-a' },
      status: {
        phase: 'Pending',
        initContainerStatuses: [
          { name: 'activate', state: { terminated: { reason: 'Completed' } } },
          { name: 'expand-bluefs', state: { running: {} }, restartCount: 1, lastState: { terminated: { finishedAt: '2026-09-27T10:05:00Z' } } },
          { name: 'chown', state: { waiting: { reason: 'PodInitializing' } } },
        ],
        containerStatuses: [{ name: 'osd', ready: false, state: { waiting: { reason: 'PodInitializing' } } }],
      },
    })
    expect(p).toMatchObject({ osdId: 0, initDone: 1, initTotal: 3, waiting: 'init expand-bluefs', restarts: 0, ready: false })
  })

  it('ignore un pod sans label ceph-osd-id', () => {
    expect(toOsdPod({ metadata: { name: 'x' } })).toBeNull()
  })
})

describe('osdRange', () => {
  it('compresse une suite contiguë et liste le reste', () => {
    expect(osdRange([6, 3, 4, 5])).toBe('osd.3-6')
    expect(osdRange([1, 4])).toBe('osd.1, osd.4')
  })
})

describe('promAnswered', () => {
  // WAL replay: the pod runs, every query fails. That is "unreachable", not
  // an empty and therefore healthy-looking snapshot.
  it('rend null quand aucune requête n-a répondu', () => {
    expect(promAnswered({})).toBeNull()
  })

  it('garde un instantané dès qu-une requête a répondu, même vide', () => {
    expect(promAnswered({ osdUp: [] })).toEqual({ osdUp: [] })
  })
})

describe('doublon de série entre machines', () => {
  it('préfère la machine du nœud de l-OSD et nomme le doublon', () => {
    const other = hwMachine()
    other.name = 'srv-clone'
    other.nodeRef = 'node-other'
    const link = linkOsdDisk(['HITACHI_X_SER-HDD3'], HW_NODE, [other, hwMachine()])
    expect(link).toMatchObject({ kind: 'physical', machine: HW_MACHINE, hostConflict: false, duplicateOn: ['srv-clone', HW_MACHINE] })
  })

  it('ne signale aucun doublon quand une seule machine a la série', () => {
    expect(linkOsdDisk(['HITACHI_X_SER-HDD3'], HW_NODE, [hwMachine()])).toMatchObject({ duplicateOn: [] })
  })
})

// The prod case of 27/09: osd.3-6 up, in, running and ready, 5 restarts,
// the last one at 19:36 (the hardware node rebooted).
describe('fenêtre de stabilité après redémarrage', () => {
  const last = Date.parse('2026-09-27T19:36:00Z')
  const pod = () => osdPod(3, { restarts: 5, lastTerminationAt: '2026-09-27T19:36:00Z', startedAt: '2026-09-27T19:36:00Z' })
  const row = (now: number, windowMs?: number) =>
    buildOsdRows({ prom: healthyProm(), pods: [pod()], machines: [hwMachine()], now, stableAfterMs: windowMs }).find((o) => o.id === 3)!

  it('dit depuis quand c-est stable pendant la fenêtre', () => {
    const r = row(last + 47 * 60_000)
    expect(r.state).toBe('settling')
    expect(r.stateDetail).toBe('stable for 47 min, 5 restart(s) in total — watched until 1 h without a restart')
  })

  it('oublie le redémarrage au bord exact de la fenêtre, pas une milliseconde avant', () => {
    expect(row(last + 60 * 60_000 - 1).state).toBe('settling')
    const r = row(last + 60 * 60_000)
    expect(r.state).toBe('up-in')
    expect(r.stateDetail).toBe('stable for 1 h, 5 restart(s) in total')
  })

  it('suit une fenêtre configurée', () => {
    expect(row(last + 20 * 60_000, 15 * 60_000).state).toBe('up-in')
    expect(row(last + 20 * 60_000, 30 * 60_000).state).toBe('settling')
  })

  it('lit le dernier redémarrage dans containerStatuses.lastState.terminated.finishedAt', () => {
    const p = toOsdPod({
      metadata: { name: 'rook-ceph-osd-3-x', labels: { 'ceph-osd-id': '3' } },
      status: {
        phase: 'Running',
        initContainerStatuses: [{ name: 'activate', restartCount: 9, lastState: { terminated: { finishedAt: '2026-09-27T20:10:00Z' } }, state: { terminated: { reason: 'Completed' } } }],
        containerStatuses: [{ name: 'osd', ready: true, restartCount: 5, state: { running: { startedAt: '2026-09-27T19:36:05Z' } }, lastState: { terminated: { finishedAt: '2026-09-27T19:36:00Z' } } }],
      },
    })!
    expect(p.lastTerminationAt).toBe('2026-09-27T19:36:00Z')
    expect(p.restarts).toBe(5)
  })

  it('porte le poids CRUSH lu par le mgr, et null pour un OSD non couvert', () => {
    const rows = buildOsdRows({ prom: healthyProm(), pods: healthyPods(), machines: [hwMachine()], now: NOW, crushWeights: new Map([[3, 1.09], [0, 0]]) })
    expect(rows[3].crushWeight).toBe(1.09)
    expect(rows[0].crushWeight).toBe(0)
    expect(rows[1].crushWeight).toBeNull()
  })
})
