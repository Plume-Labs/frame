import { describe, it, expect } from 'vitest'
import { erroredLeaves, parseProxmoxHost, parseScrub, zfsFindings, zvolForVirtualOsd } from './zfs-source'
import { pvePayload } from './testdata/proxmox-host'

const NOW = Date.parse('2026-09-27T12:00:00Z')

describe('parseProxmoxHost', () => {
  const r = parseProxmoxHost(pvePayload())

  it('lit les deux pools avec santé et capacité', () => {
    expect(r.pools.map((p) => [p.name, p.health])).toEqual([['data', 'ONLINE'], ['rpool', 'ONLINE']])
    expect(r.pools[0].sizeBytes).toBeGreaterThan(r.pools[0].allocBytes)
  })

  it('déplie l-arbre des vdevs sous la racine du pool, avec log et cache', () => {
    const data = r.pools[0]
    expect(data.vdevs.map((v) => v.name)).toEqual(['raidz1-0', 'logs', 'cache'])
    expect(data.vdevs[0].children).toHaveLength(4)
  })

  it('lit le dernier scrub', () => {
    expect(r.pools[0].scrub).toMatchObject({ kind: 'done', errors: 0, when: 'Sun Sep 14 00:24:01 2026' })
  })

  it('lit SMART et usure des disques de l-hôte, N/A en null', () => {
    const hdd = r.disks.find((d) => d.devpath === '/dev/sdf')!
    expect(hdd).toMatchObject({ smartHealth: 'PASSED', wearout: null, used: 'ZFS' })
    expect(r.disks.find((d) => d.devpath === '/dev/nvme0n1')?.wearout).toBe(97)
  })

  it('relie chaque disque de VM à son volume', () => {
    expect(r.guestDisks).toContainEqual(
      expect.objectContaining({ vmid: 201, slot: 'scsi2', volume: 'data:vm-201-disk-2', vmUuid: '00000000-0000-4000-8000-000000000201' }),
    )
    expect(r.guestDisks.some((g) => g.volume.includes('cdrom') || g.slot === 'ide2')).toBe(false)
  })

  it('liste les zvols des stockages zfspool', () => {
    expect(r.datasets.find((d) => d.volume === 'data:vm-201-disk-2')).toMatchObject({ vmid: 201, sizeBytes: 2 * 1024 ** 4 })
  })
})

describe('parseScrub', () => {
  it('distingue en cours, jamais, et terminé avec erreurs', () => {
    expect(parseScrub('scrub in progress since Sat Sep 27 01:00:00 2026\n1T scanned').kind).toBe('in-progress')
    expect(parseScrub('none requested').kind).toBe('none')
    expect(parseScrub('scrub repaired 4K in 01:00:00 with 2 errors on Sun Sep 14 00:24:01 2026')).toMatchObject({ kind: 'done', errors: 2 })
  })
})

describe('zfsFindings', () => {
  it('ne dit rien d-un hôte sain', () => {
    expect(zfsFindings(parseProxmoxHost(pvePayload()), NOW)).toEqual([])
  })

  it('signale un pool dégradé en critique', () => {
    const p = pvePayload()
    p.details[0].state = 'DEGRADED'
    const f = zfsFindings(parseProxmoxHost(p), NOW)
    expect(f).toContainEqual(expect.objectContaining({ severity: 'critical', code: 'zfs-pool-health', text: 'ZFS host-01/data: pool DEGRADED' }))
  })

  it('nomme le vdev qui a des erreurs de checksum', () => {
    const p = pvePayload()
    p.details[0].children![0].children![0].children![2].cksum = 7
    const r = parseProxmoxHost(p)
    expect(erroredLeaves(r.pools[0].vdevs).map((e) => e.path)).toEqual(['raidz1-0 / ata-HDD4T_SER-Z3'])
    expect(zfsFindings(r, NOW).map((x) => x.code)).toContain('zfs-vdev-errors')
  })

  it('signale un disque dont SMART ne passe pas, pas un UNKNOWN', () => {
    const p = pvePayload()
    p.disks[0].health = 'FAILED'
    p.disks[1].health = 'UNKNOWN'
    const f = zfsFindings(parseProxmoxHost(p), NOW).filter((x) => x.code === 'host-disk-smart')
    expect(f).toHaveLength(1)
  })

  it('signale un scrub trop ancien', () => {
    expect(zfsFindings(parseProxmoxHost(pvePayload()), Date.parse('2026-11-30T00:00:00Z')).map((x) => x.code)).toContain('zfs-scrub-old')
  })
})

describe('zvolForVirtualOsd', () => {
  const hosts = [parseProxmoxHost(pvePayload())]
  it('trouve le zvol sous le disque QEMU de l-OSD par UUID SMBIOS et slot', () => {
    expect(zvolForVirtualOsd('drive-scsi2', '00000000-0000-4000-8000-000000000202', hosts)?.vm.volume).toBe('data:vm-202-disk-2')
  })

  it('ne devine pas sans UUID de nœud', () => {
    expect(zvolForVirtualOsd('drive-scsi2', undefined, hosts)).toBeNull()
  })
})
