// The Health tab's reading of a machine: the controller's four storage
// conditions, the mirrors they rest on, and the thresholds behind them. Pure.
//
// The verdict is the controller's, read from `status.conditions`, never
// recomputed here: a screen and an alert that disagree are worse than either
// being wrong. Only the per-mirror rows read `status.storage.local`, to say
// which array or pool the condition is about.

import { agentFreshness } from './disk-inventory'
import type { RegistryAlert } from './alert-registry'
import type { Machine } from './machines'
import type { Tone } from './thresholds'

export const STORAGE_HEALTH_POLICY_NAME = 'default'

export interface StorageThresholds {
  zfsCapacityPercent: number
  ssdWearPercent: number
}

/** Mirrors `DefaultZFSCapacityPercent` and `DefaultSSDWearPercent` in storagehealthpolicy_types.go. */
export const DEFAULT_STORAGE_THRESHOLDS: StorageThresholds = { zfsCapacityPercent: 80, ssdWearPercent: 80 }

export interface StorageHealthPolicyCR {
  spec?: Partial<StorageThresholds>
}

export function toThresholds(cr: StorageHealthPolicyCR): StorageThresholds {
  return { ...DEFAULT_STORAGE_THRESHOLDS, ...cr.spec }
}

/** The CRD's bounds; the apiserver refuses anything else with a less friendly message. */
export function validThreshold(value: number): boolean {
  return Number.isInteger(value) && value >= 1 && value <= 100
}

export const STORAGE_CHECKS = [
  { type: 'StorageDegraded', label: 'Mirrors and disks' },
  { type: 'ScrubErrors', label: 'Scrub' },
  { type: 'StorageCapacityHigh', label: 'ZFS capacity' },
  { type: 'DiskWearHigh', label: 'SSD wear' },
] as const

export const NETWORK_CHECKS = [
  { type: 'NetworkDegraded', label: 'Bonds and links' },
  { type: 'LinkSpeedLow', label: 'Link speed' },
] as const

export interface CheckVerdict {
  type: string
  label: string
  tone: Tone
  word: string
  detail: string
}

/**
 * One badge per check. A check the controller has not written yet — a manager
 * that predates it — reads as not assessed, never as healthy.
 */
export function checkVerdicts(
  machine: Machine,
  checks: readonly { type: string; label: string }[] = STORAGE_CHECKS,
): CheckVerdict[] {
  return checks.map(({ type, label }) => {
    const c = machine.storageConditions.find((x) => x.type === type)
    const detail = c?.message ?? ''
    if (c?.status === 'True') return { type, label, tone: 'destructive', word: c.reason === 'LastReadHeld' ? 'alert held' : 'alert', detail }
    if (c?.status === 'False') return { type, label, tone: 'accent', word: 'ok', detail }
    if (c?.status === 'Unknown') return { type, label, tone: 'warning', word: 'unknown', detail }
    return { type, label, tone: 'muted', word: 'not assessed', detail }
  })
}

/** The vdev states `internal/storage/health.go` reads as degraded. */
const BAD_VDEV = ['DEGRADED', 'FAULTED', 'OFFLINE', 'UNAVAIL', 'REMOVED']

export interface MirrorRow {
  key: string
  stack: 'md' | 'zfs'
  name: string
  layout: string
  tone: Tone
  state: string
  detail: string
}

/** Every md array and pool of the node, or one row saying the stack could not be read. */
export function mirrorRows(machine: Machine): MirrorRow[] {
  const local = machine.storage?.local
  if (!local) return []
  const rows: MirrorRow[] = []
  const unread = (stack: 'md' | 'zfs', state: string, error?: string) =>
    rows.push({
      key: `${stack}-unread`,
      stack,
      name: stack,
      layout: '',
      tone: 'warning',
      state,
      detail: state === 'busy' ? 'previous read still running, state not refreshed' : error || 'could not be read',
    })

  if (local.md.state === 'error' || local.md.state === 'busy') {
    unread('md', local.md.state, local.md.error)
  } else {
    for (const a of local.md.arrays ?? []) {
      const failed = (a.members ?? []).filter((m) => m.failed).map((m) => m.device)
      rows.push({
        key: `md-${a.name}`,
        stack: 'md',
        name: a.name,
        layout: a.level ?? '',
        tone: a.degraded ? 'destructive' : 'accent',
        state: a.degraded ? 'degraded' : a.state || 'active',
        detail: [
          a.raidDisks ? `${a.activeDisks ?? 0}/${a.raidDisks} [${a.slots ?? ''}]` : '',
          failed.length ? `failed: ${failed.join(', ')}` : '',
          a.sync ?? '',
        ].filter(Boolean).join(' · '),
      })
    }
  }

  if (local.zfs.state === 'error' || local.zfs.state === 'busy') {
    unread('zfs', local.zfs.state, local.zfs.error)
  } else {
    for (const p of local.zfs.pools ?? []) {
      const top = (p.vdevs ?? []).filter((v) => v.depth === 0).map((v) => v.name.replace(/-\d+$/, ''))
      const errors = (p.vdevs ?? []).reduce((n, v) => n + (v.readErrors ?? 0) + (v.writeErrors ?? 0) + (v.checksumErrors ?? 0), 0)
      const badVdevs = (p.vdevs ?? []).filter((v) => BAD_VDEV.includes(v.state ?? '')).map((v) => `${v.name} ${v.state}`)
      rows.push({
        key: `zfs-${p.name}`,
        stack: 'zfs',
        name: p.name,
        layout: [...new Set(top)].join(' + '),
        tone: p.health === 'ONLINE' && errors === 0 && badVdevs.length === 0 ? 'accent' : 'destructive',
        state: p.health || '?',
        detail: [
          p.capacityPercent !== undefined ? `${p.capacityPercent} % used` : '',
          ...badVdevs,
          errors ? `${errors} vdev errors` : '',
          p.scrubErrors ? `${p.scrubErrors} scrub errors` : '',
        ].filter(Boolean).join(' · '),
      })
    }
  }
  return rows
}

/** What the rows above cannot show: their age when the agent is silent, and what its lists left out. */
export function readingNotes(machine: Machine, now: number): string[] {
  const storage = machine.storage
  if (!storage?.local) return []
  const notes: string[] = []
  const { ageS, stale } = agentFreshness(storage.observedAt, now)
  if (stale) notes.push(ageS === null ? 'The agent has not dated this reading.' : `Reading ${Math.round(ageS / 60)} min old: the agent is silent.`)
  const cut = (storage.local.md.arraysOmitted ?? 0) + (storage.local.zfs.poolsOmitted ?? 0)
  if (cut > 0) notes.push(`${cut} array(s) or pool(s) past the agent's list limit are not shown.`)
  return notes
}

export interface HeldDevice {
  device: string
  checks: string[]
}

/**
 * Devices with an alert open while its check reads LastReadHeld: what a person
 * may declare removed. A device a read confirms may still appear when another
 * device holds the same check; the controller keeps its alert and says so.
 */
export function heldDevices(machine: Machine, alerts: RegistryAlert[]): HeldDevice[] {
  const held = new Set(machine.storageConditions.filter((c) => c.reason === 'LastReadHeld').map((c) => c.type))
  const byDevice = new Map<string, string[]>()
  for (const a of alerts) {
    const l = a.labels
    if (a.state !== 'Firing' || l.source !== 'frame-storage-health') continue
    if (l.machine !== machine.name || l.machine_namespace !== machine.namespace || !held.has(a.alertName)) continue
    byDevice.set(l.device, [...(byDevice.get(l.device) ?? []), a.alertName])
  }
  return [...byDevice].map(([device, checks]) => ({ device, checks })).sort((a, b) => a.device.localeCompare(b.device))
}

/** Stamped like a power request: the controller acts once per moment. */
export function alertReleaseBody(device: string): { spec: { alertRelease: { device: string; requestedAt: string } } } {
  return { spec: { alertRelease: { device, requestedAt: new Date().toISOString() } } }
}
