import { ClusterNode, CheckpointStatus, SnapshotInfo } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { ShieldCheck, Clock, Archive, Warning } from '@phosphor-icons/react'

interface ResiliencePanelProps {
  nodes: ClusterNode[]
}

function formatAge(ms: number): string {
  const s = Math.floor((Date.now() - ms) / 1000)
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  if (h > 0) return `${h}h ${m % 60}m ago`
  if (m > 0) return `${m}m ago`
  return `${s}s ago`
}

function formatMs(ms: number): string {
  const h = Math.floor(ms / 3600000)
  const m = Math.floor((ms % 3600000) / 60000)
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

// Simulated checkpoint statuses
function generateCheckpointStatuses(nodes: ClusterNode[]): CheckpointStatus[] {
  return nodes
    .filter(n => n.status !== 'offline')
    .slice(0, 8)
    .map(n => ({
      nodeId: n.id,
      lastCheckpointAt: Date.now() - Math.floor(Math.random() * 3600000),
      nextCheckpointAt: Date.now() + Math.floor(Math.random() * 900000),
      checkpointCount: Math.floor(Math.random() * 20) + 1,
      storageUsedGB: Math.random() * 500,
      healthy: Math.random() > 0.1,
    }))
}

const SAMPLE_SNAPSHOTS: SnapshotInfo[] = [
  { id: 'snap-001', name: 'daily-stateful-backup-20240118', namespace: 'neura-inference', timestamp: Date.now() - 7200000, sizeGB: 148, type: 'full', status: 'completed', ttlHours: 720 },
  { id: 'snap-002', name: 'hourly-inference-backup-20240118-10', namespace: 'neura-inference', timestamp: Date.now() - 3600000, sizeGB: 12, type: 'incremental', status: 'completed', ttlHours: 48 },
  { id: 'snap-003', name: 'hourly-inference-backup-20240118-11', namespace: 'neura-inference', timestamp: Date.now() - 600000, sizeGB: 8, type: 'incremental', status: 'pending', ttlHours: 48 },
  { id: 'snap-004', name: 'daily-stateful-backup-20240117', namespace: 'neura-training', timestamp: Date.now() - 93600000, sizeGB: 220, type: 'full', status: 'completed', ttlHours: 720 },
]

const STATUS_BADGE: Record<SnapshotInfo['status'], string> = {
  pending:   'bg-primary/20 text-primary border-primary/30',
  completed: 'bg-accent/20 text-accent border-accent/30',
  failed:    'bg-destructive/20 text-destructive border-destructive/30',
}

export function ResiliencePanel({ nodes }: ResiliencePanelProps) {
  const checkpoints = generateCheckpointStatuses(nodes)
  const healthyCheckpoints = checkpoints.filter(c => c.healthy).length
  const totalStorageGB = checkpoints.reduce((s, c) => s + c.storageUsedGB, 0)
  const latestSnapshot = SAMPLE_SNAPSHOTS.reduce((latest, s) => s.timestamp > latest.timestamp ? s : latest, SAMPLE_SNAPSHOTS[0])
  const snapshotAgeMs = Date.now() - latestSnapshot.timestamp

  // MTBF / MTTR: simulate from node uptime data
  const onlineNodes = nodes.filter(n => n.status !== 'offline')
  const avgUptimeMs = onlineNodes.length > 0 ? onlineNodes.reduce((s, n) => s + n.uptime, 0) / onlineNodes.length : 0
  const offlineCount = nodes.filter(n => n.status === 'offline').length
  const mockMTTR = 1800000 // 30 minutes simulated

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldCheck className="text-primary" />
            Resilience &amp; Reliability
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">MTBF (avg uptime)</div>
              <div className="font-mono text-xl font-bold text-accent">{formatMs(avgUptimeMs)}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Est. MTTR</div>
              <div className="font-mono text-xl font-bold text-primary">{formatMs(mockMTTR)}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Offline Nodes</div>
              <div className={`font-mono text-xl font-bold ${offlineCount > 0 ? 'text-destructive' : 'text-foreground'}`}>{offlineCount}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Latest Snapshot</div>
              <div className={`font-mono text-xl font-bold ${snapshotAgeMs > 7200000 ? 'text-[oklch(0.75_0.18_75)]' : 'text-foreground'}`}>
                {formatAge(latestSnapshot.timestamp)}
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <Clock className="text-primary" />
              Checkpoint Timeline
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex items-center gap-4 text-sm mb-2">
              <span className="text-muted-foreground">Healthy: <span className="text-accent font-mono font-bold">{healthyCheckpoints}</span></span>
              <span className="text-muted-foreground">Storage: <span className="font-mono font-bold">{totalStorageGB.toFixed(1)} GB</span></span>
            </div>
            {checkpoints.map(ckpt => {
              const node = nodes.find(n => n.id === ckpt.nodeId)
              return (
                <div key={ckpt.nodeId} className={`flex items-center justify-between p-2 rounded border text-xs ${ckpt.healthy ? 'border-border bg-secondary/20' : 'border-[oklch(0.75_0.18_75)]/30 bg-[oklch(0.75_0.18_75)]/5'}`}>
                  <div className="flex items-center gap-2">
                    {ckpt.healthy
                      ? <ShieldCheck size={12} className="text-accent" />
                      : <Warning size={12} className="text-[oklch(0.75_0.18_75)]" />}
                    <span className="font-mono">{node?.name ?? ckpt.nodeId}</span>
                  </div>
                  <div className="text-right text-muted-foreground">
                    <div>Last: {formatAge(ckpt.lastCheckpointAt)}</div>
                    <div>#{ckpt.checkpointCount} · {ckpt.storageUsedGB.toFixed(1)} GB</div>
                  </div>
                </div>
              )
            })}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <Archive className="text-primary" />
              Velero Snapshots
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {SAMPLE_SNAPSHOTS.map(snap => (
              <div key={snap.id} className="p-3 rounded-lg bg-secondary/30 border border-border space-y-1">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-xs font-semibold truncate max-w-[200px]">{snap.name}</span>
                  <Badge className={`text-[10px] border ${STATUS_BADGE[snap.status]}`}>{snap.status}</Badge>
                </div>
                <div className="flex items-center gap-3 text-xs text-muted-foreground">
                  <span className="font-mono">{snap.namespace}</span>
                  <span>{snap.type}</span>
                  <span>{snap.sizeGB} GB</span>
                  <span>{formatAge(snap.timestamp)}</span>
                  <span>TTL: {snap.ttlHours}h</span>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
