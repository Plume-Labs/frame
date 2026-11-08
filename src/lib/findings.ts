import {
  AlertsStatus,
  BackupStatus,
  CephStatus,
  ClusterNodeInfo,
} from './frame-sdk'
import type { Cache } from './caches'
import type { Database } from './databases'
import type { Machine } from './machines'
import { NETWORK_CHECKS, STORAGE_CHECKS } from './storage-health'
import { Tone, formatAge } from './thresholds'
import type { VolumeFillFinding } from './volume-load'

/**
 * What the Overview screen calls a problem, and where it sends you about it.
 *
 * Kept apart from the rendering because this is the judgement, not the markup:
 * deciding a backup 49 hours old is stale while a 47-hour one is fine is the
 * kind of rule that must not change by accident, and it can only be pinned down
 * by a test if it is reachable without a DOM.
 */
export interface Finding {
  key: string
  tone: Tone
  label: string
  detail: string
  screen: string
  tab?: string
}

/** A backup this old means the schedule has probably stopped running. */
export const BACKUP_STALE_MS = 48 * 3600_000

/** Beyond this the list stops being a summary; the Alerts tab has the rest. */
const MAX_ALERTS = 5

const plural = (n: number, word: string) => `${n} ${word}${n > 1 ? 's' : ''}`

export function collectFindings(
  input: {
    nodes?: ClusterNodeInfo[]
    alerts?: AlertsStatus | null
    ceph?: CephStatus | null
    backups?: BackupStatus | null
    machines?: Pick<Machine, 'name' | 'storageConditions'>[]
    caches?: Pick<Cache, 'name' | 'namespace' | 'ready' | 'reason' | 'message'>[]
    databases?: Pick<Database, 'name' | 'namespace' | 'ready' | 'reason' | 'message'>[]
    /** Volumes at or past the fill thresholds (volume-load.ts), worst first. */
    volumes?: VolumeFillFinding[]
  },
  now: number = Date.now(),
): Finding[] {
  const findings: Finding[] = []

  for (const a of input.alerts?.alerts.slice(0, MAX_ALERTS) ?? []) {
    findings.push({
      key: `alert-${a.name}-${a.startsAt}`,
      tone: a.severity === 'critical' ? 'destructive' : 'warning',
      label: a.name,
      detail: a.summary || `${a.namespace} · ${a.severity}`,
      screen: 'security',
      tab: 'alerts',
    })
  }

  if (input.nodes) {
    const down = input.nodes.filter((n) => !n.ready)
    if (down.length > 0) {
      findings.push({
        key: 'nodes-not-ready',
        tone: 'destructive',
        label: `${plural(down.length, 'node')} not ready`,
        detail: down.map((n) => n.name).join(', '),
        screen: 'nodes',
        tab: 'nodes',
      })
    }
    // Cordoned but ready is deliberate (a drain, an upgrade), so it is a warning
    // and reported separately rather than lumped in with nodes that are down.
    const cordoned = input.nodes.filter((n) => n.unschedulable && n.ready)
    if (cordoned.length > 0) {
      findings.push({
        key: 'nodes-cordoned',
        tone: 'warning',
        label: `${plural(cordoned.length, 'node')} cordoned`,
        detail: cordoned.map((n) => n.name).join(', '),
        screen: 'nodes',
        tab: 'nodes',
      })
    }
  }

  if (input.ceph && input.ceph.health !== 'HEALTH_OK') {
    findings.push({
      key: 'ceph-health',
      tone: input.ceph.health === 'HEALTH_WARN' ? 'warning' : 'destructive',
      label: `Ceph ${input.ceph.health.replace('HEALTH_', '')}`,
      detail: `${input.ceph.osds} OSDs · ${input.ceph.mons} mons`,
      screen: 'storage',
    })
  }

  for (const v of input.volumes ?? []) {
    findings.push({
      key: `volume-fill-${v.key}`,
      tone: v.severity === 'critical' ? 'destructive' : 'warning',
      label: `Volume ${v.usePct.toFixed(0)} % full`,
      detail: v.key,
      screen: 'storage',
      tab: 'storage-load',
    })
  }

  const bk = input.backups
  if (bk) {
    if (!bk.storageReady) {
      findings.push({
        key: 'backup-storage',
        tone: 'destructive',
        label: 'Backup storage unavailable',
        detail: 'Velero cannot reach its object store — backups are not being written',
        screen: 'capacity',
        tab: 'resilience',
      })
    } else if (!bk.lastSuccess) {
      // No successful backup at all is worse than an old one, and the "stale"
      // branch below would never fire for it.
      findings.push({
        key: 'backup-none',
        tone: 'warning',
        label: 'No successful backup recorded',
        detail: bk.schedule ? `schedule ${bk.schedule} has never completed` : 'no schedule configured',
        screen: 'capacity',
        tab: 'resilience',
      })
    } else {
      const last = new Date(bk.lastSuccess).getTime()
      if (now - last > BACKUP_STALE_MS) {
        findings.push({
          key: 'backup-stale',
          tone: 'warning',
          label: 'Last successful backup is stale',
          detail: `${formatAge(last, now)} ago`,
          screen: 'capacity',
          tab: 'resilience',
        })
      }
    }
  }

  for (const m of machineChecks(input.machines ?? [])) findings.push(m)
  for (const f of serviceFindings('Cache', 'caches', input.caches ?? [])) findings.push(f)
  for (const f of serviceFindings('Database', 'databases', input.databases ?? [])) findings.push(f)

  return findings
}

/**
 * Checks that mean data is at risk now. The others (capacity, wear, speed,
 * load, rising errors) say it will be if nothing changes, so they warn.
 */
const DESTRUCTIVE_CHECKS = new Set(['StorageDegraded', 'ScrubErrors', 'NetworkDegraded'])

/**
 * One finding per failing check, naming every machine it fails on — the same
 * shape as "2 nodes not ready", so a switch dying does not bury the list under
 * one row per host. Only `True` counts: `Unknown` and a check not yet written
 * are shown on the Health and Links tabs, and are not a problem to act on.
 */
function machineChecks(machines: Pick<Machine, 'name' | 'storageConditions'>[]): Finding[] {
  const out: Finding[] = []
  const groups = [
    { checks: STORAGE_CHECKS, screen: 'storage', tab: 'storage-health' },
    { checks: NETWORK_CHECKS, screen: 'network', tab: 'network-links' },
  ]
  for (const { checks, screen, tab } of groups) {
    for (const { type, label } of checks) {
      const hit = machines.filter((m) => m.storageConditions.some((c) => c.type === type && c.status === 'True'))
      if (hit.length === 0) continue
      const first = hit[0].storageConditions.find((c) => c.type === type)
      out.push({
        key: `check-${type}`,
        tone: DESTRUCTIVE_CHECKS.has(type) ? 'destructive' : 'warning',
        label: `${label}: ${plural(hit.length, 'machine')}`,
        detail: hit.length === 1 && first?.message ? `${hit[0].name} · ${first.message}` : hit.map((m) => m.name).join(', '),
        screen,
        tab,
      })
    }
  }
  return out
}

/**
 * A FrameService that is not Ready. `InstanceLost` is the queue whose volume
 * was replaced — jobs written before it are gone — so it is the one that is
 * destructive; anything else not Ready (still provisioning, a bad parameter)
 * is a warning.
 */
function serviceFindings(
  kind: string,
  screen: string,
  services: Pick<Cache, 'name' | 'namespace' | 'ready' | 'reason' | 'message'>[],
): Finding[] {
  return services
    .filter((s) => !s.ready)
    .map((s): Finding => ({
      key: `${screen}-${s.namespace}-${s.name}`,
      tone: s.reason === 'InstanceLost' ? 'destructive' : 'warning',
      label: `${kind} ${s.namespace}/${s.name} not ready`,
      detail: s.message ? `${s.reason} · ${s.message}` : s.reason,
      screen,
    }))
}
