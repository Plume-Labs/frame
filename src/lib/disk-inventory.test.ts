import { describe, it, expect } from 'vitest'
import { agentFreshness, buildDiskRows, chassisDiskTemp, controllerLines, diskRole, ioIndex, mdArrays, osdsWithoutMachine } from './disk-inventory'
import { buildOsdRows } from './ceph-diagnostic'
import { healthyPods, healthyProm, hwMachine, setSample, HW_NODE } from './testdata/ceph-cluster'

const NOW = Date.parse('2026-09-27T12:20:00Z')

function diskRows(machine = hwMachine(), prom = healthyProm()) {
  const osds = buildOsdRows({ prom, pods: healthyPods(), machines: [machine], now: NOW })
  return buildDiskRows(machine, osds, ioIndex(prom.diskUtil, prom.diskAwait))
}

describe('buildDiskRows', () => {
  it('rend une ligne par série, dans l-ordre des baies', () => {
    const r = diskRows()
    expect(r).toHaveLength(8)
    expect(r.map((d) => d.bay)).toEqual(['1I:6:1', '1I:6:2', '1I:6:3', '1I:6:4', '2I:6:5', '2I:6:6', '2I:6:7', '2I:6:8'])
  })

  it('porte le bon OSD sur la bonne baie, par la série', () => {
    const r = diskRows()
    const bay = (b: string) => r.find((d) => d.bay === b)!
    expect(bay('2I:6:6').role).toBe('osd.3 (hdd)')
    expect(bay('2I:6:7').role).toBe('osd.4 (hdd)')
    expect(bay('1I:6:4').role).toBe('osd.5 (hdd)')
    expect(bay('2I:6:6').osPath).toBe('/dev/sdg')
  })

  // A disk the BMC lists and the kernel does not is a row, not a hole.
  it('garde le disque vu par le BMC seul, marqué bmc-only', () => {
    const masked = diskRows().find((d) => d.serial === 'SER-MASK')!
    expect(masked).toMatchObject({ presence: 'bmc-only', osPath: null, bmcHealth: 'OK', role: 'not presented to the OS' })
  })

  it('marque os-only un disque que le BMC ne liste pas', () => {
    const m = hwMachine()
    m.storage!.observed.push({ path: '/dev/sdh', serialNumber: 'SER-USB', sizeGB: 64, occupancy: 'free' })
    expect(diskRows(m).find((d) => d.serial === 'SER-USB')?.presence).toBe('os-only')
  })

  it('ne conclut rien sur la présence OS quand l-agent n-a jamais publié', () => {
    const m = hwMachine()
    m.storage = null
    expect(diskRows(m).every((d) => d.presence === 'os-unreported')).toBe(true)
  })

  it('rattache les I/O par nœud et nom noyau courant', () => {
    const r = diskRows()
    expect(r.find((d) => d.serial === 'SER-HDD1')?.io.utilPct).toBe(2.6)
    // The agent now sees SER-HDD4 on sdf: node-exporter's sdf is that disk.
    expect(r.find((d) => d.serial === 'SER-HDD4')?.io).toEqual({ utilPct: 3.5, awaitMs: 4.2 })
  })

  it('ne remonte pas "None" comme motif BMC', () => {
    expect(diskRows().every((d) => d.bmcReasons.length === 0)).toBe(true)
  })
})

describe('diskRole', () => {
  it('nomme les occupations', () => {
    expect(diskRole(null, 'md-member', 'both')).toBe('mdadm member')
    expect(diskRole(null, 'zfs-member', 'both')).toBe('ZFS member')
    expect(diskRole(null, 'ceph-osd', 'both')).toBe('ceph-osd (OSD not identified)')
  })
})

describe('mdArrays', () => {
  it('lit md0 sain', () => {
    const p = healthyProm()
    expect(mdArrays(p.mdDisks, p.mdRequired, p.mdState)).toEqual([
      { node: HW_NODE, device: 'md0', active: 2, failed: 0, spare: 0, required: 2, state: 'active', degraded: false },
    ])
  })

  it('déclare dégradé un miroir à un disque actif sur deux', () => {
    const p = healthyProm()
    setSample(p, 'mdDisks', { device: 'md0', state: 'active' }, 1)
    expect(mdArrays(p.mdDisks, p.mdRequired, p.mdState)[0].degraded).toBe(true)
  })
})

describe('divers', () => {
  it('lit la température châssis HD Max et refuse un 0', () => {
    expect(chassisDiskTemp(hwMachine())).toEqual({ name: '08-HD Max', celsius: 46 })
    const m = hwMachine()
    m.sensors!.temperatures[0].celsius = 0
    expect(chassisDiskTemp(m)).toBeNull()
  })

  it('juge la fraîcheur du relevé agent', () => {
    expect(agentFreshness('2026-09-27T12:19:30Z', NOW)).toEqual({ ageS: 30, stale: false })
    expect(agentFreshness('2026-09-27T12:10:00Z', NOW).stale).toBe(true)
    expect(agentFreshness(null, NOW)).toEqual({ ageS: null, stale: true })
  })

  it('liste les OSD des nœuds sans FrameMachine', () => {
    const osds = buildOsdRows({ prom: healthyProm(), pods: healthyPods(), machines: [hwMachine()], now: NOW })
    expect(osdsWithoutMachine(osds, [hwMachine()]).map((o) => o.id)).toEqual([0, 1, 2])
  })
})

describe('lot 2 — identité et appartenance publiées par l-agent et le BMC', () => {
  function lot2Machine() {
    const m = hwMachine()
    const hdd3 = m.storage!.observed.find((d) => d.serialNumber === 'SER-HDD3')!
    hdd3.wwn = '0x5000c500deadbe03'
    hdd3.byIdPath = '/dev/disk/by-id/wwn-0x5000c500deadbe03'
    const hdd4 = m.storage!.observed.find((d) => d.serialNumber === 'SER-HDD4')!
    hdd4.wwn = '0x5000c500deadbe04'
    hdd4.byIdPath = '/dev/disk/by-id/wwn-0x5000c500deadbe04'
    const ssd1 = m.storage!.observed.find((d) => d.serialNumber === 'SER-SSD1')!
    ssd1.occupancy = 'md-member'
    ssd1.members = ['md0']
    const ssd2 = m.storage!.observed.find((d) => d.serialNumber === 'SER-SSD2')!
    ssd2.mountPoint = '/var/lib/data'
    const bmc = m.inventory!.drives.find((d) => d.serialNumber === 'SER-HDD3')!
    bmc.firmware = 'FW01'
    bmc.temperatureCelsius = 38
    return m
  }
  const declared = ['/dev/disk/by-id/wwn-0x5000c500deadbe03']

  function rows(m = lot2Machine(), devices = declared) {
    const osds = buildOsdRows({ prom: healthyProm(), pods: healthyPods(), machines: [m], now: NOW })
    return buildDiskRows(m, osds, new Map(), devices)
  }

  it('nomme le miroir md et le point de montage dans le rôle', () => {
    const r = rows()
    expect(r.find((d) => d.serial === 'SER-SSD1')?.role).toBe('mdadm member (md0)')
    expect(r.find((d) => d.serial === 'SER-SSD2')?.role).toBe('mounted /var/lib/data')
  })

  it('confronte le by-id WWN à la liste de devices du CephCluster', () => {
    const r = rows()
    expect(r.find((d) => d.serial === 'SER-HDD3')?.declaredInCeph).toBe(true)
    expect(r.find((d) => d.serial === 'SER-HDD4')?.declaredInCeph).toBe(false)
    // No WWN published: no verdict.
    expect(r.find((d) => d.serial === 'SER-HDD1')?.declaredInCeph).toBeNull()
    // No device list (Rook picks devices itself): no verdict either.
    expect(rows(lot2Machine(), []).every((d) => d.declaredInCeph === null)).toBe(true)
  })

  it('rend la température BMC quand elle est rapportée, jamais un 0', () => {
    const m = lot2Machine()
    m.inventory!.drives.find((d) => d.serialNumber === 'SER-HDD4')!.temperatureCelsius = 0
    const r = rows(m)
    expect(r.find((d) => d.serial === 'SER-HDD3')?.temperatureCelsius).toBe(38)
    expect(r.find((d) => d.serial === 'SER-HDD4')?.temperatureCelsius).toBeNull()
    expect(r.find((d) => d.serial === 'SER-HDD3')?.firmware).toBe('FW01')
  })

  it('décrit le contrôleur, « not reported » pour ce qui manque', () => {
    const m = lot2Machine()
    m.inventory!.storageControllers = [{ location: 'Slot 0', model: 'P440ar', firmware: '7.00', health: 'OK' }]
    expect(controllerLines(m)).toEqual(['Slot 0 · P440ar · fw 7.00 · health OK · cache battery not reported'])
    expect(controllerLines(hwMachine())).toEqual([])
  })
})
