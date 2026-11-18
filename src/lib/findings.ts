import {
  AlertsStatus,
  BackupStatus,
  CephStatus,
  ClusterNodeInfo,
} from './frame-sdk'
import type { RegistryAlert } from './alert-registry'
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
    /**
     * The alert registry (FrameAlert). Only its Firing entries count here, and
     * only those Alertmanager does not already report: the registry outlives
     * an Alertmanager that is down or has lost an alert, which is exactly when
     * the live list would say "all quiet".
     */
    registry?: RegistryAlert[]
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

  const live = input.alerts?.alerts.slice(0, MAX_ALERTS) ?? []
  for (const a of live) {
    findings.push({
      key: `alert-${a.name}-${a.startsAt}`,
      tone: a.severity === 'critical' ? 'destructive' : 'warning',
      label: a.name,
      detail: a.summary || `${a.namespace} · ${a.severity}`,
      screen: 'security',
      tab: 'alerts',
    })
  }

  // Same alert, same namespace = same finding; the registry is a second view of
  // what Alertmanager already shows, not a second alert.
  const seen = new Set((input.alerts?.alerts ?? []).map((a) => `${a.name}/${a.namespace}`))
  // The registry is sorted by time; the few slots left should go to the worst first.
  const registryOnly = (input.registry ?? [])
    .filter((a) => a.state === 'Firing' && !seen.has(`${a.alertName}/${a.namespace}`))
    .sort((a, b) => Number(b.severity === 'critical') - Number(a.severity === 'critical'))
  for (const a of registryOnly.slice(0, Math.max(0, MAX_ALERTS - live.length))) {
    findings.push({
      key: `registry-${a.name || a.fingerprint}`,
      tone: a.severity === 'critical' ? 'destructive' : 'warning',
      label: a.alertName,
      detail: `${a.namespace ? `${a.namespace} · ` : ''}${a.severity} · registry only`,
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
      label: `Volume ${v.usePct.toFixed(1)} % full`,
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

/**
 * Which data sources the Overview cannot vouch for. A source that is loading,
 * failed or stale contributes no (or outdated) findings, so "no findings" only
 * means "all clear" when loading, failed and stale are all empty. `unavailable`
 * is a source that answered "nothing to read" (no Alertmanager, no Velero, a
 * metrics query that returned nothing): it cannot block the green check on
 * clusters that legitimately lack it, but the check must not claim it was
 * watched.
 */
export function unreadSources(
  sources: Record<string, { phase: string; data?: unknown; staleError?: string }>,
): { loading: string[]; failed: string[]; stale: string[]; unavailable: string[] } {
  const out = { loading: [] as string[], failed: [] as string[], stale: [] as string[], unavailable: [] as string[] }
  for (const [name, s] of Object.entries(sources)) {
    if (s.phase === 'loading') out.loading.push(name)
    else if (s.phase === 'error') out.failed.push(name)
    else if (s.phase === 'ready' && s.staleError) out.stale.push(name)
    else if (s.phase === 'ready' && s.data == null) out.unavailable.push(name)
  }
  return out
}
