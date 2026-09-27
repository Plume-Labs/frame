import { describe, it, expect } from 'vitest'
import { localStackItems, nodesWithPublishedMd, stackSummary, vdevDisk, vdevPath } from './local-storage'
import { attentionItems } from './storage-attention'
import { buildOsdRows } from './ceph-diagnostic'
import { buildPoolRows, daemonQuorum, objectHealth, setFlags } from './ceph-pools'
import { mdArrays } from './disk-inventory'
import { healthyPods, healthyProm, hwMachine, setSample, HW_NODE } from './testdata/ceph-cluster'
import { g9Local, zfsLocal } from './testdata/local-storage'

function machine(local = zfsLocal()) {
  const m = hwMachine()
  m.storage!.divergences = []
  m.storage!.local = local
  const hdd3 = m.storage!.observed.find((d) => d.serialNumber === 'SER-HDD3')!
  hdd3.byIdPath = '/dev/disk/by-id/wwn-0x5000c500deadbe03'
  return m
}

const codes = (m = machine()) => localStackItems(m).map((i) => i.code)

describe('localStackItems', () => {
  it('ne dit rien du nœud matériel sain (md0 en miroir, ni ZFS ni LVM)', () => {
    expect(localStackItems(machine(g9Local()))).toEqual([])
  })

  it('signale un pool DEGRADED en critique', () => {
    const item = localStackItems(machine()).find((i) => i.code === 'zfs-pool-health')
    expect(item).toMatchObject({ severity: 'critical', text: 'srv-hw ZFS data: pool DEGRADED' })
  })

  it('signale un pool FAULTED, et rien pour un pool ONLINE', () => {
    const l = zfsLocal()
    l.zfs.pools![0].health = 'FAULTED'
    expect(localStackItems(machine(l)).filter((i) => i.code === 'zfs-pool-health').map((i) => i.text)).toEqual([
      'srv-hw ZFS data: pool FAULTED',
    ])
  })

  it('nomme la feuille en erreur avec son chemin dans l-arbre', () => {
    const item = localStackItems(machine()).find((i) => i.code === 'zfs-vdev')
    expect(item?.text).toContain('raidz1-0 / /dev/disk/by-id/wwn-0x5000c500deadbe03-part1 FAULTED — 12 read, 0 write, 1200 checksum')
  })

  it('signale un scrub qui a trouvé des erreurs', () => {
    expect(codes()).toContain('zfs-scrub-errors')
    const l = zfsLocal()
    l.zfs.pools![0].scrubErrors = 0
    expect(codes(machine(l))).not.toContain('zfs-scrub-errors')
  })

  it('signale un md dégradé avec ses membres en panne', () => {
    const l = g9Local()
    Object.assign(l.md.arrays![0], { degraded: true, slots: 'U_', activeDisks: 1, sync: 'recovery 8.5%' })
    l.md.arrays![0].members![1].failed = true
    const item = localStackItems(machine(l)).find((i) => i.code === 'md-degraded')
    expect(item?.text).toBe('srv-hw md0 (raid1): active [U_] 1/2 active, failed sdc2, recovery 8.5%')
  })

  it('signale une pile illisible, pas une pile absente', () => {
    const l = g9Local()
    l.lvm = { state: 'error', error: 'pvs timed out after 10s' }
    expect(localStackItems(machine(l)).map((i) => i.text)).toEqual(['srv-hw LVM: could not be read — pvs timed out after 10s (state unknown)'])
  })

  it('signale un VG partiel', () => {
    const l = zfsLocal()
    l.lvm.vgs![0].attr = 'wz-pn-'
    expect(codes(machine(l))).toContain('lvm-vg-partial')
  })

  it('ne dit rien d-une machine dont l-agent ne publie pas encore les piles', () => {
    const m = hwMachine()
    expect(localStackItems(m)).toEqual([])
    expect(stackSummary(null)).toBe("not published by this node's agent")
  })
})

describe('jointure vdev ↔ disque', () => {
  const observed = machine().storage!.observed
  it('joint une feuille by-id WWN au lien WWN publié par l-agent', () => {
    expect(vdevDisk('/dev/disk/by-id/wwn-0x5000c500deadbe03-part1', observed)?.serialNumber).toBe('SER-HDD3')
  })

  it('joint une feuille ata-MODELE_SERIE par la série', () => {
    expect(vdevDisk('/dev/disk/by-id/ata-HUC10121CLAR1200_SER-HDD1-part1', observed)?.serialNumber).toBe('SER-HDD1')
  })

  it('ne joint jamais une feuille /dev/sdX', () => {
    expect(vdevDisk('/dev/sdb1', observed)).toBeNull()
  })

  it('reconstruit le chemin d-une ligne de l-arbre', () => {
    const v = zfsLocal().zfs.pools![0].vdevs!
    expect(vdevPath(v, 5)).toBe('logs / mirror-1 / /dev/disk/by-id/ata-SSD-A_SER-SSD1-part1')
  })
})

describe('bandeau', () => {
  function inputs(machines: ReturnType<typeof machine>[], prom = healthyProm()) {
    const pools = buildPoolRows(prom)
    return {
      promAvailable: true,
      osds: buildOsdRows({ prom, pods: healthyPods(), machines, now: Date.parse('2026-09-27T12:20:00Z') }),
      pools,
      flags: setFlags(prom.flags),
      healthDetails: {},
      quorum: daemonQuorum(prom),
      objects: objectHealth(prom, pools),
      mdArrays: mdArrays(prom.mdDisks, prom.mdRequired, prom.mdState),
      machines,
      frameStorageCount: 1,
    }
  }

  it('porte les constats des piles locales', () => {
    expect(attentionItems(inputs([machine()])).map((i) => i.code)).toContain('zfs-pool-health')
  })

  // md0 degraded seen by node-exporter AND published by the agent: one line.
  it('ne dit pas deux fois un md que l-agent publie', () => {
    const prom = healthyProm()
    setSample(prom, 'mdDisks', { device: 'md0', state: 'active' }, 1)
    const l = g9Local()
    Object.assign(l.md.arrays![0], { degraded: true, activeDisks: 1, slots: 'U_' })
    const md = attentionItems(inputs([machine(l)], prom)).filter((i) => i.code === 'md-degraded')
    expect(md).toHaveLength(1)
    expect(md[0].text).toContain('srv-hw md0')
    expect(nodesWithPublishedMd([machine(l)])).toEqual(new Set([HW_NODE]))
  })
})

describe('réserves du réaudit', () => {
  it('remonte la ligne « Permanent errors » du pool', () => {
    const l = zfsLocal()
    l.zfs.pools![1].errors = 'Permanent errors have been detected in the following files:'
    const item = localStackItems(machine(l)).find((i) => i.code === 'zfs-data-errors')
    expect(item).toMatchObject({ severity: 'critical', text: 'srv-hw ZFS rpool: Permanent errors have been detected in the following files:' })
    expect(codes()).not.toContain('zfs-data-errors')
  })

  it('traite une feuille GUID comme une feuille : bandeau et jointure par « was … »', () => {
    const l = zfsLocal()
    l.zfs.pools![1].vdevs = [
      { name: 'mirror-0', depth: 0, state: 'DEGRADED' },
      { name: '12345678901234567890', depth: 1, state: 'UNAVAIL', note: 'was /dev/disk/by-id/ata-HUC10121CLAR1200_SER-HDD2-part1' },
    ]
    const items = localStackItems(machine(l)).filter((i) => i.code === 'zfs-vdev' && i.text.includes('rpool'))
    expect(items.map((i) => i.text)).toEqual([
      expect.stringContaining('srv-hw ZFS rpool: mirror-0 / 12345678901234567890 UNAVAIL'),
    ])
    const observed = machine().storage!.observed
    expect(vdevDisk('12345678901234567890', observed, 'was /dev/disk/by-id/ata-HUC10121CLAR1200_SER-HDD2-part1')?.serialNumber).toBe('SER-HDD2')
  })

  it('signale les tableaux md non listés', () => {
    const l = g9Local()
    l.md.arraysOmitted = 3
    expect(localStackItems(machine(l)).map((i) => i.text)).toContain(
      'srv-hw md: 3 array(s) not listed (status size limit) — their state is unknown',
    )
  })

  it('dit qu-une pile est occupée et de quand date ce qu-elle montre', () => {
    const l = zfsLocal()
    l.zfs.state = 'busy'
    l.zfs.readAt = '2026-09-27T11:59:00Z'
    const item = localStackItems(machine(l)).find((i) => i.code === 'local-stack-busy')
    expect(item?.text).toBe('srv-hw ZFS: previous read still running on the node — showing the read of 2026-09-27T11:59:00Z')
    expect(stackSummary(l)).toContain('ZFS: 2 pool(s) (previous read still running)')
  })
})
