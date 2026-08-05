import { describe, it, expect } from 'vitest'
import { collectFindings, BACKUP_STALE_MS } from './findings'
import type { BackupStatus, CephStatus, ClusterNodeInfo } from './frame-sdk'

const NOW = 1_700_000_000_000

function node(name: string, over: Partial<ClusterNodeInfo> = {}): ClusterNodeInfo {
  return {
    name,
    ready: true,
    roles: [],
    kubeletVersion: 'v1.36.2',
    os: 'linux',
    cpuCores: 4,
    memGiB: 8,
    unschedulable: false,
    ...over,
  }
}

const healthyCeph: CephStatus = {
  health: 'HEALTH_OK',
  version: '20.2.2',
  osds: 3,
  mons: 3,
  bytesTotal: 300,
  bytesUsed: 75,
  bytesAvailable: 225,
  pools: [],
}

function backups(over: Partial<BackupStatus> = {}): BackupStatus {
  return { storageReady: true, schedule: 'daily', lastSuccess: new Date(NOW).toISOString(), recent: [], ...over }
}

describe('collectFindings', () => {
  it('reports nothing when the cluster is healthy', () => {
    expect(
      collectFindings(
        { nodes: [node('a'), node('b')], alerts: { alerts: [], bySeverity: {} }, ceph: healthyCeph, backups: backups() },
        NOW,
      ),
    ).toEqual([])
  })

  it('stays quiet while data is still missing rather than inventing problems', () => {
    expect(collectFindings({}, NOW)).toEqual([])
  })

  it('separates nodes that are down from nodes that were cordoned on purpose', () => {
    const f = collectFindings(
      { nodes: [node('a', { ready: false }), node('b', { unschedulable: true }), node('c')] },
      NOW,
    )
    expect(f.map((x) => x.key)).toEqual(['nodes-not-ready', 'nodes-cordoned'])
    expect(f[0].tone).toBe('destructive')
    expect(f[1].tone).toBe('warning')
  })

  it('does not count a node that is both down and cordoned as cordoned', () => {
    const f = collectFindings({ nodes: [node('a', { ready: false, unschedulable: true })] }, NOW)
    expect(f.map((x) => x.key)).toEqual(['nodes-not-ready'])
  })

  it('grades Ceph HEALTH_WARN below HEALTH_ERR', () => {
    const warn = collectFindings({ ceph: { ...healthyCeph, health: 'HEALTH_WARN' } }, NOW)
    const err = collectFindings({ ceph: { ...healthyCeph, health: 'HEALTH_ERR' } }, NOW)
    expect(warn[0].tone).toBe('warning')
    expect(err[0].tone).toBe('destructive')
  })

  it('flags a backup past the staleness cutoff but not one just inside it', () => {
    const stale = new Date(NOW - BACKUP_STALE_MS - 1000).toISOString()
    const fresh = new Date(NOW - BACKUP_STALE_MS + 1000).toISOString()
    expect(collectFindings({ backups: backups({ lastSuccess: stale }) }, NOW).map((f) => f.key)).toEqual([
      'backup-stale',
    ])
    expect(collectFindings({ backups: backups({ lastSuccess: fresh }) }, NOW)).toEqual([])
  })

  it('treats a never-completed backup as its own finding, not as a fresh one', () => {
    // The staleness branch compares a date; with no date it would never fire,
    // so "never backed up" would otherwise pass as healthy.
    const f = collectFindings({ backups: backups({ lastSuccess: null }) }, NOW)
    expect(f.map((x) => x.key)).toEqual(['backup-none'])
  })

  it('reports unreachable backup storage instead of its age', () => {
    const f = collectFindings({ backups: backups({ storageReady: false, lastSuccess: null }) }, NOW)
    expect(f.map((x) => x.key)).toEqual(['backup-storage'])
    expect(f[0].tone).toBe('destructive')
  })

  it('caps the alert list so the card stays a summary', () => {
    const alerts = {
      alerts: Array.from({ length: 9 }, (_, i) => ({
        name: `Alert${i}`,
        severity: i === 0 ? 'critical' : 'warning',
        state: 'firing',
        summary: '',
        namespace: 'ns',
        startsAt: `${i}`,
        labels: {},
      })),
      bySeverity: {},
    }
    const f = collectFindings({ alerts }, NOW)
    expect(f).toHaveLength(5)
    expect(f[0].tone).toBe('destructive')
    expect(f[1].tone).toBe('warning')
  })

  it('sends each finding to the screen that can act on it', () => {
    const f = collectFindings(
      {
        nodes: [node('a', { ready: false })],
        ceph: { ...healthyCeph, health: 'HEALTH_WARN' },
        backups: backups({ storageReady: false }),
      },
      NOW,
    )
    expect(f.map((x) => [x.screen, x.tab])).toEqual([
      ['nodes', 'nodes'],
      ['storage', undefined],
      ['capacity', 'resilience'],
    ])
  })
})
