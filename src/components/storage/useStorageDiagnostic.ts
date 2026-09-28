import { useMemo } from 'react'
import { CephDiagnosticRaw, StorageEntry, createFrameClient } from '@/lib/frame-sdk'
import { config } from '@/lib/frame-config'
import { crushWeights, placementOf, placementSourceLine, poolPlacement } from '@/lib/ceph-placement'
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
  cephDevices: string[]
  attention: AttentionItem[]
  /** Where min_size, CRUSH rules and weights come from, or why they are missing. */
  placementLine: string
  reload: () => void
}

/**
 * One read of everything both Storage tabs join: ceph-mgr metrics, OSD
 * pods and FrameMachines. Metrics drift continuously,
 * so this polls (30 s, the agent's own cadence) rather than watches.
 */
/**
 * `storageEntries`: the FrameStorage entries when the caller has read them
 * (null otherwise) — their count feeds the "no FrameStorage" finding, and
 * their status.cephPlacement the CRUSH weights, min_size and CRUSH rules.
 */
export function useStorageDiagnostic(storageEntries: StorageEntry[] | null): StorageDiagnostic {
  const { state, reload } = useLiveResource<CephDiagnosticRaw>(() => frame.cluster.cephDiagnostic(), [], [], 30_000)
  const { state: mState } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)

  const raw = state.phase === 'ready' ? state.data : null
  const machines = useMemo(() => (mState.phase === 'ready' ? mState.data : []), [mState])

  return useMemo(() => {
    const prom = raw?.prom ?? {}
    const now = Date.now()
    const placement = placementOf(storageEntries ?? [])
    const osds = buildOsdRows({
      prom,
      pods: raw?.pods ?? [],
      machines,
      now,
      stableAfterMs: config().storage.osdStableAfterMinutes * 60_000,
      crushWeights: crushWeights(placement),
    })
    const pools = buildPoolRows(prom, poolPlacement(placement))
    const flags = setFlags(prom.flags)
    const quorum = daemonQuorum(prom)
    const objects = objectHealth(prom, pools)
    const md = mdArrays(prom.mdDisks, prom.mdRequired, prom.mdState)
    const promAvailable = raw !== null && raw.prom !== null
    const io = ioIndex(prom.diskUtil, prom.diskAwait)
    const cephDevices = raw?.cephDevices ?? []
    const extra = [
      ...machines.flatMap((m) => undeclaredOsdDiskItems(m.name, buildDiskRows(m, osds, io, cephDevices))),
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
            frameStorageCount: storageEntries ? storageEntries.length : null,
            certificateNotAfter: placement?.certificateNotAfter ?? null,
            now,
            extra,
          })
        : state.phase === 'error'
          ? unreadableDiagnosticItems(state.message)
          : [],
      placementLine: placementSourceLine(placement),
      reload,
    }
  }, [raw, machines, storageEntries, state, reload])
}
