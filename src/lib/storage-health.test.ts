import { describe, it, expect } from 'vitest'
import { checkVerdicts, DEFAULT_STORAGE_THRESHOLDS, mirrorRows, toThresholds, validThreshold } from './storage-health'
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

  it("dit qu'une pile illisible n'a pas été lue, sans lister ses restes", () => {
    const local = g9Local()
    local.md.state = 'busy'
    expect(mirrorRows(machine(local))).toEqual([expect.objectContaining({ name: 'md', tone: 'warning', state: 'busy' })])
  })

  it('reste vide sans rapport local', () => {
    expect(mirrorRows(machine(null))).toEqual([])
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
