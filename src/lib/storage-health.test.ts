import { describe, it, expect } from 'vitest'
import { alertReleaseBody, checkVerdicts, DEFAULT_STORAGE_THRESHOLDS, heldDevices, mirrorRows, readingNotes, toThresholds, validThreshold } from './storage-health'
import type { RegistryAlert } from './alert-registry'
import { toMachine } from './machines'
import { hwMachine } from './testdata/ceph-cluster'
import { g9Local, zfsLocal } from './testdata/local-storage'
import type { LocalStorage, Machine } from './machines'

function machine(local: LocalStorage | null, conditions: Machine['storageConditions'] = []): Machine {
  const m = hwMachine()
  m.storage!.local = local
  m.storageConditions = conditions
  return m
}

const verdict = (m: Machine, type: string) => checkVerdicts(m).find((v) => v.type === type)!

describe('checkVerdicts', () => {
  it('lit le verdict du contrôleur, sans le recalculer', () => {
    const local = g9Local()
    local.md.arrays![0].degraded = true
    const m = machine(local, [{ type: 'StorageDegraded', status: 'False', reason: 'Healthy', message: '' }])
    expect(verdict(m, 'StorageDegraded')).toMatchObject({ tone: 'accent', word: 'ok' })
  })

  it('distingue une alerte maintenue faute de lecture', () => {
    const m = machine(g9Local(), [{ type: 'StorageDegraded', status: 'True', reason: 'LastReadHeld', message: 'held' }])
    expect(verdict(m, 'StorageDegraded')).toMatchObject({ tone: 'destructive', word: 'alert held', detail: 'held' })
  })

  it('ne dit pas sain quand le contrôleur ne sait pas', () => {
    const m = machine(g9Local(), [{ type: 'DiskWearHigh', status: 'Unknown', reason: 'WearNotReported', message: 'no figure' }])
    expect(verdict(m, 'DiskWearHigh')).toMatchObject({ tone: 'warning', word: 'unknown' })
  })

  it('ne dit pas sain sans condition écrite', () => {
    expect(verdict(machine(g9Local()), 'ScrubErrors')).toMatchObject({ tone: 'muted', word: 'not assessed' })
  })
})

describe('mirrorRows', () => {
  it('donne md0 du G9 avec ses disques actifs', () => {
    expect(mirrorRows(machine(g9Local()))).toEqual([
      expect.objectContaining({ stack: 'md', name: 'md0', layout: 'raid1', tone: 'accent', detail: expect.stringContaining('2/2') }),
    ])
  })

  it('nomme le membre en échec et la reconstruction', () => {
    const local = g9Local()
    Object.assign(local.md.arrays![0], { degraded: true, activeDisks: 1, slots: 'U_', sync: 'recovery 8.5%' })
    local.md.arrays![0].members![1].failed = true
    const row = mirrorRows(machine(local))[0]
    expect(row).toMatchObject({ tone: 'destructive', state: 'degraded' })
    expect(row.detail).toContain(`failed: ${local.md.arrays![0].members![1].device}`)
    expect(row.detail).toContain('recovery 8.5%')
  })

  it('remonte un pool dégradé', () => {
    expect(mirrorRows(machine(zfsLocal())).find((r) => r.stack === 'zfs')).toMatchObject({ tone: 'destructive', state: 'DEGRADED' })
  })

  it('ne montre pas en vert un pool ONLINE à la feuille en panne', () => {
    const local = zfsLocal()
    const p = local.zfs.pools![0]
    p.health = 'ONLINE'
    p.vdevs = [{ name: 'mirror-0', depth: 0, state: 'ONLINE' }, { name: '/dev/x', depth: 1, state: 'FAULTED' }]
    expect(mirrorRows(machine(local)).find((r) => r.stack === 'zfs')).toMatchObject({ tone: 'destructive', detail: expect.stringContaining('FAULTED') })
  })

  it("dit qu'une pile illisible n'a pas été lue, sans lister ses restes", () => {
    const local = g9Local()
    local.md.state = 'busy'
    expect(mirrorRows(machine(local))).toEqual([expect.objectContaining({ name: 'md', tone: 'warning', state: 'busy' })])
  })

  it('reste vide sans rapport local', () => {
    expect(mirrorRows(machine(null))).toEqual([])
  })
})

describe('toMachine', () => {
  it('garde les conditions de stockage, sans Reachable', () => {
    const m = toMachine({
      metadata: { name: 'm', namespace: 'd' },
      status: { conditions: [
        { type: 'Reachable', status: 'True' },
        { type: 'StorageDegraded', status: 'True', reason: 'Detected', message: 'md0' },
      ] },
    })
    expect(m.storageConditions.map((c) => c.type)).toEqual(['StorageDegraded'])
    expect(verdict(m, 'StorageDegraded')).toMatchObject({ tone: 'destructive', word: 'alert' })
  })
})

describe('readingNotes', () => {
  const now = Date.parse('2026-11-06T12:00:00Z')
  const fresh = '2026-11-06T11:59:30Z'

  it('ne dit rien d’une lecture fraîche et complète', () => {
    const m = machine(g9Local())
    m.storage!.observedAt = fresh
    expect(readingNotes(m, now)).toEqual([])
  })

  it('date une lecture dont l’agent s’est tu', () => {
    const m = machine(g9Local())
    m.storage!.observedAt = '2026-11-06T11:40:00Z'
    expect(readingNotes(m, now)[0]).toContain('20 min')
  })

  it('dit ce que les listes ont laissé de côté', () => {
    const local = g9Local()
    local.md.arraysOmitted = 2
    const m = machine(local)
    m.storage!.observedAt = fresh
    expect(readingNotes(m, now)[0]).toContain('2 array(s)')
  })
})

describe('heldDevices', () => {
  const alert = (over: Partial<RegistryAlert>, labels: Record<string, string> = {}): RegistryAlert => ({
    name: 'fa', fingerprint: 'f', alertName: 'StorageDegraded', severity: 'critical', namespace: '', state: 'Firing',
    startsAt: '', endsAt: '', deliveries: [],
    labels: { source: 'frame-storage-health', machine: 'm', machine_namespace: 'd', device: 'tank', ...labels },
    ...over,
  })
  const m = () => {
    const x = machine(g9Local(), [{ type: 'StorageDegraded', status: 'True', reason: 'LastReadHeld' }])
    x.name = 'm'
    x.namespace = 'd'
    return x
  }

  it('regroupe par périphérique ce qui est tenu', () => {
    const x = m()
    x.storageConditions.push({ type: 'ScrubErrors', status: 'True', reason: 'LastReadHeld' })
    expect(heldDevices(x, [alert({}), alert({ alertName: 'ScrubErrors' })])).toEqual([{ device: 'tank', checks: ['StorageDegraded', 'ScrubErrors'] }])
  })

  it('ne propose pas une alerte que la lecture confirme', () => {
    const x = machine(g9Local(), [{ type: 'StorageDegraded', status: 'True', reason: 'Detected' }])
    x.name = 'm'
    x.namespace = 'd'
    expect(heldDevices(x, [alert({})])).toEqual([])
  })

  it('ignore les autres machines, les alertes closes et Alertmanager', () => {
    expect(heldDevices(m(), [
      alert({}, { machine: 'autre' }),
      alert({}, { machine_namespace: 'autre' }),
      alert({ state: 'Resolved' }),
      alert({}, { source: 'alertmanager' }),
    ])).toEqual([])
  })

  it('date la demande pour que le contrôleur agisse une fois', () => {
    const b = alertReleaseBody('tank')
    expect(b.spec.alertRelease.device).toBe('tank')
    expect(Date.parse(b.spec.alertRelease.requestedAt)).not.toBeNaN()
  })
})

describe('seuils', () => {
  it('garde le défaut du champ absent', () => {
    expect(toThresholds({ spec: { ssdWearPercent: 60 } })).toEqual({ ...DEFAULT_STORAGE_THRESHOLDS, ssdWearPercent: 60 })
  })

  it("s'en tient aux bornes du CRD", () => {
    expect([1, 100].every(validThreshold)).toBe(true)
    expect([0, 101, 79.5, NaN].some(validThreshold)).toBe(false)
  })
})
