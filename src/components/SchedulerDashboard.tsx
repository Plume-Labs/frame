import { ClusterNode, PodGroupStatus, SchedulerType, BackfillSlot, BackfillJob } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Cpu, Lightning, Queue, ArrowsClockwise, Shuffle } from '@phosphor-icons/react'
import { VOLCANO_QUEUE_LIST } from '@/lib/tuning-fixtures'
import { TONE_TEXT } from '@/lib/thresholds'

interface SchedulerDashboardProps {
  nodes: ClusterNode[]
}

const MOCK_POD_GROUPS: PodGroupStatus[] = [
  { name: 'neura-training-gang-1', queue: 'neura-low', minMember: 8, running: 8, pending: 0, phase: 'Running', gpusRequested: 8 },
  { name: 'neura-inference-batch', queue: 'neura-high', minMember: 4, running: 4, pending: 0, phase: 'Running', gpusRequested: 4 },
  { name: 'neura-training-gang-2', queue: 'neura-low', minMember: 16, running: 7, pending: 9, phase: 'Pending', gpusRequested: 16 },
  { name: 'neura-eval-job', queue: 'neura-medium', minMember: 2, running: 2, pending: 0, phase: 'Completed', gpusRequested: 2 },
]

const MOCK_BACKFILL_SLOTS: BackfillSlot[] = [
  { queue: 'neura-low',    startAt: Date.now() + 180000,  durationMin: 25, cpuAvailable: 64,  memAvailableGi: 128, gpuAvailable: 4  },
  { queue: 'neura-medium', startAt: Date.now() + 420000,  durationMin: 12, cpuAvailable: 32,  memAvailableGi: 64,  gpuAvailable: 2  },
  { queue: 'neura-low',    startAt: Date.now() + 900000,  durationMin: 60, cpuAvailable: 128, memAvailableGi: 256, gpuAvailable: 8  },
]

const MOCK_BACKFILL_JOBS: BackfillJob[] = [
  { name: 'eval-small-batch', queue: 'neura-low',    requestedCPU: 16,  requestedMemGi: 32,  requestedGPU: 2, estimatedDurationMin: 20, placed: true,  slotStartAt: Date.now() + 180000 },
  { name: 'hp-search-0',      queue: 'neura-medium', requestedCPU: 8,   requestedMemGi: 16,  requestedGPU: 1, estimatedDurationMin: 10, placed: true,  slotStartAt: Date.now() + 420000 },
  { name: 'finetune-tiny',    queue: 'neura-low',    requestedCPU: 32,  requestedMemGi: 64,  requestedGPU: 4, estimatedDurationMin: 55, placed: false              },
]

const ACTIVE_SCHEDULER: SchedulerType = 'volcano'

const phaseColors: Record<PodGroupStatus['phase'], string> = {
  Running: 'text-accent',
  Pending: 'text-warning',
  Completed: 'text-muted-foreground',
  Failed: 'text-destructive',
  Unknown: 'text-muted-foreground',
}

const phaseBadge: Record<PodGroupStatus['phase'], string> = {
  Running: 'bg-accent/20 text-accent border-accent/30',
  Pending: 'bg-warning/10 text-warning border-warning/30',
  Completed: 'bg-secondary text-muted-foreground border-border',
  Failed: 'bg-destructive/20 text-destructive border-destructive/30',
  Unknown: 'bg-secondary text-muted-foreground border-border',
}

function getServiceClassDistribution(nodes: ClusterNode[]) {
  const counts = { HIGH: 0, MEDIUM: 0, LOW: 0 }
  nodes.forEach(n => { if (n.serviceClass) counts[n.serviceClass]++ })
  return counts
}

export function SchedulerDashboard({ nodes }: SchedulerDashboardProps) {
  const activeNodes = nodes.filter(n => n.status !== 'offline')
  const dist = getServiceClassDistribution(nodes)
  const total = nodes.length || 1
  const runningGroups = MOCK_POD_GROUPS.filter(g => g.phase === 'Running').length
  const pendingGroups = MOCK_POD_GROUPS.filter(g => g.phase === 'Pending').length
  const totalGPUsScheduled = MOCK_POD_GROUPS.filter(g => g.phase === 'Running').reduce((s, g) => s + g.gpusRequested, 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowsClockwise className="text-primary" />
            Scheduler
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Active Scheduler</div>
              <div className="font-mono text-lg font-bold text-primary uppercase">{ACTIVE_SCHEDULER}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Active Nodes</div>
              <div className="font-mono text-lg font-bold text-accent">{activeNodes.length}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Running PodGroups</div>
              <div className="font-mono text-lg font-bold text-accent">{runningGroups}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Pending PodGroups</div>
              <div className={`font-mono text-lg font-bold ${pendingGroups > 0 ? 'text-warning' : 'text-foreground'}`}>{pendingGroups}</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">GPUs Gang-Scheduled</div>
            <div className="font-mono text-2xl font-bold text-primary">{totalGPUsScheduled}</div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <Cpu className="text-primary" />
            Service Class Distribution
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {(
            [
              { label: 'HIGH', value: dist.HIGH, color: 'bg-destructive', textColor: 'text-destructive', desc: 'Real-time inference' },
              { label: 'MEDIUM', value: dist.MEDIUM, color: 'bg-warning', textColor: 'text-warning', desc: 'Database queries' },
              { label: 'LOW', value: dist.LOW, color: 'bg-accent', textColor: 'text-accent', desc: 'Batch / Training' },
            ] as const
          ).map(({ label, value, color, textColor, desc }) => (
            <div key={label} className="space-y-1">
              <div className="flex items-center justify-between text-sm">
                <div className="flex items-center gap-2">
                  <span className={`font-mono font-bold ${textColor}`}>{label}</span>
                  <span className="text-muted-foreground text-xs">{desc}</span>
                </div>
                <span className="font-mono">{value} / {total} nodes</span>
              </div>
              <div className="w-full h-2 rounded-full bg-secondary">
                <div className={`h-2 rounded-full ${color}`} style={{ width: `${(value / total) * 100}%` }} />
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <Queue className="text-primary" />
            Gang Scheduling Groups
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {MOCK_POD_GROUPS.map(group => (
            <div key={group.name} className="p-3 rounded-lg bg-secondary/30 border border-border">
              <div className="flex items-center justify-between mb-2">
                <span className="font-mono text-sm font-semibold">{group.name}</span>
                <Badge className={`text-xs border ${phaseBadge[group.phase]}`}>{group.phase}</Badge>
              </div>
              <div className="grid grid-cols-4 gap-2 text-xs text-muted-foreground mb-2">
                <div><span className="text-foreground font-mono">{group.queue}</span><br />Queue</div>
                <div><span className={`font-mono font-bold ${phaseColors[group.phase]}`}>{group.running}/{group.minMember}</span><br />Running/Min</div>
                <div><span className="font-mono font-bold text-foreground">{group.pending}</span><br />Pending</div>
                <div><span className="font-mono font-bold text-primary">{group.gpusRequested}</span><br />GPUs</div>
              </div>
              <Progress value={(group.running / group.minMember) * 100} className="h-1" />
            </div>
          ))}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <Lightning className="text-primary" />
            Volcano Queues
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {VOLCANO_QUEUE_LIST.map(q => (
            <div key={q.name} className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
              <div className="flex items-center gap-3">
                <span className={`font-mono text-sm font-bold ${TONE_TEXT[q.tone]}`}>{q.name}</span>
                <span className="text-xs text-muted-foreground">weight {q.weight}</span>
              </div>
              <div className="text-right">
                <div className="font-mono text-xs">{q.cpuAlloc} CPU</div>
                <div className="font-mono text-xs text-muted-foreground">{q.memAlloc}</div>
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      {/* ── Backfill scheduling ────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <Shuffle className="text-primary" />
            Backfill Scheduling
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="text-xs text-muted-foreground">
            Small jobs fill idle reservation gaps between large GPU allocations. Volcano backfill admits jobs that fit within an available time window without delaying the primary reservation.
          </div>

          {/* Open slots */}
          <div>
            <div className="text-xs text-muted-foreground uppercase tracking-wide mb-2">Open Backfill Slots</div>
            <div className="space-y-2">
              {MOCK_BACKFILL_SLOTS.map((slot, i) => {
                const startsIn = Math.round((slot.startAt - Date.now()) / 60000)
                return (
                  <div key={i} className="p-2 rounded-lg bg-secondary/30 border border-border flex items-center justify-between text-xs">
                    <div className="flex items-center gap-3">
                      <span className="font-mono font-bold text-foreground">{slot.queue}</span>
                      <span className="text-muted-foreground">in {startsIn}m · {slot.durationMin}m window</span>
                    </div>
                    <div className="flex gap-3 text-muted-foreground font-mono">
                      <span>{slot.cpuAvailable} CPU</span>
                      <span>{slot.gpuAvailable} GPU</span>
                    </div>
                  </div>
                )
              })}
            </div>
          </div>

          {/* Backfill jobs */}
          <div>
            <div className="text-xs text-muted-foreground uppercase tracking-wide mb-2">Backfill Job Queue</div>
            <div className="space-y-2">
              {MOCK_BACKFILL_JOBS.map(job => (
                <div key={job.name} className="p-2 rounded-lg bg-secondary/30 border border-border flex items-center justify-between text-xs">
                  <div className="flex items-center gap-2">
                    {job.placed
                      ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">Placed</Badge>
                      : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">Waiting</Badge>
                    }
                    <span className="font-mono font-bold">{job.name}</span>
                    <span className="text-muted-foreground">{job.estimatedDurationMin}m</span>
                  </div>
                  <div className="flex gap-3 text-muted-foreground font-mono">
                    <span>{job.requestedCPU} CPU</span>
                    <span>{job.requestedGPU} GPU</span>
                    {job.placed && job.slotStartAt && (
                      <span className="text-accent">+{Math.round((job.slotStartAt - Date.now()) / 60000)}m</span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
