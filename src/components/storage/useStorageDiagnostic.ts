import { useMemo } from 'react'
import { CephDiagnosticRaw, createFrameClient } from '@/lib/frame-sdk'
import type { Machine } from '@/lib/machines'
import { buildOsdRows, type OsdRow } from '@/lib/ceph-diagnostic'
import {
  buildPoolRows,
  capacityByClass,
  daemonQuorum,
  objectHealth,
  setFlags,
  tallyPgs,
  type ClassCapacity,
  type DaemonQuorum,
  type ObjectHealth,
  type PgTally,
  type PoolRow,
} from '@/lib/ceph-pools'
import { buildDiskRows, ioIndex, mdArrays, type DiskIo, type MdArray } from '@/lib/disk-inventory'
import { attentionItems, undeclaredOsdDiskItems, unreadableDiagnosticItems, type AttentionItem } from '@/lib/storage-attention'
import { zfsSourceItems, type ZfsSourceResult } from '@/lib/zfs-source'
import { useLiveResource } from '@/hooks/useLiveResource'

const frame = createFrameClient()

export interface StorageDiagnostic {
  loading: boolean
  error: string | null
  promAvailable: boolean
  osds: OsdRow[]
  pools: PoolRow[]
  pgs: PgTally
  flags: string[]
  classes: ClassCapacity[]
  objects: ObjectHealth
  quorum: DaemonQuorum
  md: MdArray[]
  io: Map<string, DiskIo>
  machines: Machine[]
  zfs: ZfsSourceResult | null
  nodeUuids: Record<string, string>
  cephDevices: string[]
  attention: AttentionItem[]
  reload: () => void
}

/**
 * One read of everything both Storage tabs join: ceph-mgr metrics, OSD
 * pods, FrameMachines and the hypervisor's ZFS. Metrics drift continuously,
 * so this polls (30 s, the agent's own cadence) rather than watches.
 */
export function useStorageDiagnostic(frameStorageCount: number | null): StorageDiagnostic {
  const { state, reload } = useLiveResource<CephDiagnosticRaw>(() => frame.cluster.cephDiagnostic(), [], [], 30_000)
  const { state: mState } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  const { state: zState } = useLiveResource<ZfsSourceResult>(() => frame.cluster.zfsHosts(), [], [], 60_000)

  const raw = state.phase === 'ready' ? state.data : null
  const machines = useMemo(() => (mState.phase === 'ready' ? mState.data : []), [mState])
  const zfs = useMemo<ZfsSourceResult | null>(
    () => (zState.phase === 'ready' ? zState.data : zState.phase === 'error' ? { status: 'error', message: zState.message } : null),
    [zState],
  )

  return useMemo(() => {
    const prom = raw?.prom ?? {}
    const now = Date.now()
    const osds = buildOsdRows({ prom, pods: raw?.pods ?? [], machines, now })
    const pools = buildPoolRows(prom)
    const flags = setFlags(prom.flags)
    const quorum = daemonQuorum(prom)
    const objects = objectHealth(prom, pools)
    const md = mdArrays(prom.mdDisks, prom.mdRequired, prom.mdState)
    const promAvailable = raw !== null && raw.prom !== null
    const io = ioIndex(prom.diskUtil, prom.diskAwait)
    const cephDevices = raw?.cephDevices ?? []
    const extra = [
      ...machines.flatMap((m) => undeclaredOsdDiskItems(m.name, buildDiskRows(m, osds, io, cephDevices))),
      ...zfsSourceItems(zfs, now),
    ]
    return {
      loading: state.phase === 'loading',
      error: state.phase === 'error' ? state.message : null,
      promAvailable,
      osds,
      pools,
      pgs: tallyPgs(prom.pgStates).cluster,
      flags,
      classes: capacityByClass(prom, osds),
      objects,
      quorum,
      md,
      io,
      machines,
      zfs,
      nodeUuids: raw?.nodeUuids ?? {},
      cephDevices,
      attention: raw
        ? attentionItems({
            promAvailable,
            osds,
            pools,
            flags,
            healthDetails: raw.healthDetails,
            quorum,
            objects,
            mdArrays: md,
            machines,
            frameStorageCount,
            extra,
          })
        : state.phase === 'error'
          ? unreadableDiagnosticItems(state.message)
          : [],
      reload,
    }
  }, [raw, machines, zfs, frameStorageCount, state, reload])
}
