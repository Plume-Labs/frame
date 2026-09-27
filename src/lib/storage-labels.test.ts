import { describe, it, expect } from 'vitest'
import { osdDiskLabel, pgLine } from './storage-labels'
import { buildOsdRows } from './ceph-diagnostic'
import { parseProxmoxHost } from './zfs-source'
import { healthyPods, healthyProm, hwMachine } from './testdata/ceph-cluster'
import { pvePayload } from './testdata/proxmox-host'

const osds = buildOsdRows({ prom: healthyProm(), pods: healthyPods(), machines: [hwMachine()], now: 0 })

describe('osdDiskLabel', () => {
  it('nomme la machine, la baie et la série d-un OSD physique', () => {
    expect(osdDiskLabel(osds[3], null, {})).toBe('srv-hw 2I:6:6 · SER-HDD3')
  })

  it('dit "source non configurée" pour le zvol d-un OSD de VM sans source ZFS', () => {
    expect(osdDiskLabel(osds[0], { status: 'unconfigured', reason: 'x' }, {})).toContain('ZFS source not configured')
  })

  it('relie l-OSD de VM à son zvol quand la source ZFS répond', () => {
    const zfs = { status: 'ok' as const, hosts: [parseProxmoxHost(pvePayload())], readAt: '' }
    expect(osdDiskLabel(osds[1], zfs, { 'node-b': '00000000-0000-4000-8000-000000000202' })).toBe(
      'VM 202 scsi2 → host-01:vm-202-disk-2',
    )
  })
})

describe('pgLine', () => {
  it('ne liste que les états présents', () => {
    expect(pgLine({ total: 121, byState: { active: 121, clean: 121, degraded: 0 } })).toBe('active 121 · clean 121')
  })
})
