import {
  AlertsStatus,
  BackupStatus,
  CephStatus,
  ClusterNodeInfo,
} from './frame-sdk'
import { Tone, formatAge } from './thresholds'

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

  return findings
}
