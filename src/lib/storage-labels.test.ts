import { describe, it, expect } from 'vitest'
import { osdDiskLabel, pgLine } from './storage-labels'
import { buildOsdRows } from './ceph-diagnostic'
import { healthyPods, healthyProm, hwMachine } from './testdata/ceph-cluster'

const osds = buildOsdRows({ prom: healthyProm(), pods: healthyPods(), machines: [hwMachine()], now: 0 })

describe('osdDiskLabel', () => {
  it('nomme la machine, la baie et la série d-un OSD physique', () => {
    expect(osdDiskLabel(osds[3])).toBe('srv-hw 2I:6:6 · SER-HDD3')
  })

  it('nomme le disque QEMU d-un OSD de VM, sans rien lire de l-hôte', () => {
    expect(osdDiskLabel(osds[0])).toBe('VM disk (QEMU drive-scsi2)')
  })
})

describe('pgLine', () => {
  it('ne liste que les états présents', () => {
    expect(pgLine({ total: 121, byState: { active: 121, clean: 121, degraded: 0 } })).toBe('active 121 · clean 121')
  })
})
