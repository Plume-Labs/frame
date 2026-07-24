import { useState, useMemo } from 'react'
import { Job, DAGNode, JobStatus } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ArrowClockwise, CheckCircle, XCircle, Clock, Spinner, Database } from '@phosphor-icons/react'
import { jobIdentity } from '@/lib/workflow-fixtures'

interface JobOrchestrationViewProps {
  jobs?: Job[]
}

const STATUS_COLORS: Record<JobStatus, string> = {
  queued:       'text-muted-foreground',
  running:      'text-primary',
  checkpointed: 'text-warning',
  completed:    'text-accent',
  failed:       'text-destructive',
}

const STATUS_BADGE: Record<JobStatus, string> = {
  queued:       'bg-secondary text-muted-foreground border-border',
  running:      'bg-primary/20 text-primary border-primary/30',
  checkpointed: 'bg-warning/10 text-warning border-warning/30',
  completed:    'bg-accent/20 text-accent border-accent/30',
  failed:       'bg-destructive/20 text-destructive border-destructive/30',
}

function StatusIcon({ status }: { status: JobStatus }) {
  switch (status) {
    case 'completed':    return <CheckCircle size={14} className="text-accent" />
    case 'failed':       return <XCircle size={14} className="text-destructive" />
    case 'running':      return <Spinner size={14} className="text-primary animate-spin" />
    case 'checkpointed': return <Database size={14} className="text-warning" />
    default:             return <Clock size={14} className="text-muted-foreground" />
  }
}

function formatDuration(ms: number): string {
  const s = Math.floor(ms / 1000)
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  if (h > 0) return `${h}h ${m % 60}m`
  if (m > 0) return `${m}m ${s % 60}s`
  return `${s}s`
}

// Simulated jobs
const DEMO_JOBS: Job[] = [
  {
    ...jobIdentity('trace-abc-001'),
    pipeline: 'neura-training-dag',
    status: 'running',
    priority: 'low',
    queue: 'neura-low',
    namespace: 'neura-training',
    retryCount: 1,
    maxRetries: 5,
    checkpoints: [
      { id: 'ckpt-001', timestamp: Date.now() - 3600000, size: 42, storageLocation: 's3://neura-checkpoints/wf-001/ckpt-001', step: 500 },
      { id: 'ckpt-002', timestamp: Date.now() - 1800000, size: 44, storageLocation: 's3://neura-checkpoints/wf-001/ckpt-002', step: 1000 },
    ],
    nodes: [
      { id: 'n1', name: 'data-validation', status: 'completed', template: 'data-validation', dependencies: [], startTime: Date.now() - 7100000, endTime: Date.now() - 6900000, retries: 0, gpuCount: 0 },
      { id: 'n2', name: 'data-preprocessing', status: 'completed', template: 'data-preprocessing', dependencies: ['n1'], startTime: Date.now() - 6900000, endTime: Date.now() - 5400000, retries: 0, gpuCount: 0 },
      { id: 'n3', name: 'restore-checkpoint', status: 'completed', template: 'restore-checkpoint', dependencies: ['n1'], startTime: Date.now() - 6900000, endTime: Date.now() - 6600000, retries: 0, gpuCount: 0 },
      { id: 'n4', name: 'distributed-training', status: 'running', template: 'training-worker', dependencies: ['n2', 'n3'], startTime: Date.now() - 5400000, retries: 1, gpuCount: 8 },
    ],
  },
  {
    ...jobIdentity('trace-def-002', 10000),
    pipeline: 'neura-inference-dag',
    status: 'completed',
    priority: 'critical',
    queue: 'neura-high',
    completedAt: Date.now() - 3540000,
    namespace: 'neura-inference',
    retryCount: 0,
    maxRetries: 3,
    checkpoints: [],
    nodes: [
      { id: 'a1', name: 'validate-input', status: 'completed', template: 'validate-input', dependencies: [], startTime: Date.now() - 3590000, endTime: Date.now() - 3580000, retries: 0, gpuCount: 0 },
      { id: 'a2', name: 'cache-lookup', status: 'completed', template: 'cache-lookup', dependencies: ['a1'], startTime: Date.now() - 3580000, endTime: Date.now() - 3575000, retries: 0, gpuCount: 0 },
      { id: 'a3', name: 'model-inference', status: 'completed', template: 'model-inference', dependencies: ['a2'], startTime: Date.now() - 3575000, endTime: Date.now() - 3550000, retries: 0, gpuCount: 1 },
      { id: 'a4', name: 'emit-result', status: 'completed', template: 'emit-result', dependencies: ['a3'], startTime: Date.now() - 3550000, endTime: Date.now() - 3540000, retries: 0, gpuCount: 0 },
    ],
  },
  {
    ...jobIdentity('trace-ghi-003', 400000),
    pipeline: 'neura-training-dag',
    status: 'failed',
    priority: 'low',
    queue: 'neura-low',
    completedAt: Date.now() - 10000000,
    namespace: 'neura-training',
    retryCount: 5,
    maxRetries: 5,
    checkpoints: [
      { id: 'ckpt-old-001', timestamp: Date.now() - 12000000, size: 38, storageLocation: 's3://neura-checkpoints/wf-003/ckpt-001', step: 300 },
    ],
    nodes: [
      { id: 'b1', name: 'data-validation', status: 'completed', template: 'data-validation', dependencies: [], startTime: Date.now() - 14000000, endTime: Date.now() - 13800000, retries: 0, gpuCount: 0 },
      { id: 'b2', name: 'distributed-training', status: 'failed', template: 'training-worker', dependencies: ['b1'], startTime: Date.now() - 13800000, endTime: Date.now() - 10000000, retries: 5, gpuCount: 8 },
    ],
  },
]

function DAGVisualization({ nodes: dagNodes }: { nodes: DAGNode[] }) {
  const maxDepth = Math.max(...dagNodes.map(n => {
    let depth = 0
    let current = n
    while (current.dependencies.length > 0) {
      depth++
      current = dagNodes.find(x => x.id === current.dependencies[0]) || current
      if (depth > 10) break
    }
    return depth
  }))
  
  const levels: DAGNode[][] = Array.from({ length: maxDepth + 1 }, () => [])
  const visited = new Set<string>()

  function assignLevel(node: DAGNode, level: number) {
    if (visited.has(node.id)) return
    visited.add(node.id)
    levels[level].push(node)
    const dependents = dagNodes.filter(n => n.dependencies.includes(node.id))
    dependents.forEach(d => assignLevel(d, level + 1))
  }

  dagNodes.filter(n => n.dependencies.length === 0).forEach(n => assignLevel(n, 0))

  return (
    <div className="overflow-x-auto">
      <div className="flex gap-4 min-w-max p-2">
        {levels.map((level, li) => (
          <div key={li} className="flex flex-col gap-2">
            {level.map(node => (
              <div
                key={node.id}
                className={`px-3 py-2 rounded border text-xs font-mono min-w-[140px] ${
                  node.status === 'completed' ? 'border-accent/40 bg-accent/5' :
                  node.status === 'running' ? 'border-primary/40 bg-primary/5' :
                  node.status === 'failed' ? 'border-destructive/40 bg-destructive/5' :
                  'border-border bg-secondary/30'
                }`}
              >
                <div className="flex items-center gap-1 mb-1">
                  <StatusIcon status={node.status} />
                  <span className="font-semibold truncate">{node.name}</span>
                </div>
                {node.gpuCount > 0 && (
                  <div className="text-primary text-[10px]">{node.gpuCount} GPU{node.gpuCount > 1 ? 's' : ''}</div>
                )}
                {node.retries > 0 && (
                  <div className="text-warning text-[10px]">{node.retries} retr{node.retries > 1 ? 'ies' : 'y'}</div>
                )}
                {node.startTime && node.endTime && (
                  <div className="text-muted-foreground text-[10px]">{formatDuration(node.endTime - node.startTime)}</div>
                )}
                {node.startTime && !node.endTime && (
                  <div className="text-primary text-[10px]">{formatDuration(Date.now() - node.startTime)} running</div>
                )}
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}

function JobCard({ job }: { job: Job }) {
  const [expanded, setExpanded] = useState(false)
  const duration = job.completedAt && job.startedAt
    ? formatDuration(job.completedAt - job.startedAt)
    : job.startedAt ? formatDuration(Date.now() - job.startedAt) : '—'

  return (
    <div className="p-4 rounded-lg bg-secondary/30 border border-border space-y-3">
      <div className="flex items-start justify-between gap-2">
        <div className="space-y-1">
          <div className="flex items-center gap-2">
            <StatusIcon status={job.status} />
            <span className="font-mono font-bold text-sm">{job.name}</span>
            <Badge className={`text-[10px] border ${STATUS_BADGE[job.status]}`}>{job.status}</Badge>
          </div>
          <div className="flex items-center gap-3 text-xs text-muted-foreground">
            <span>Pipeline: <span className="font-mono">{job.pipeline}</span></span>
            <span>Queue: <span className="font-mono">{job.queue}</span></span>
            <span>Duration: <span className="font-mono">{duration}</span></span>
            {job.retryCount > 0 && (
              <span className="text-warning">Retries: {job.retryCount}/{job.maxRetries}</span>
            )}
          </div>
        </div>
        <div className="flex gap-2 shrink-0">
          <Button variant="ghost" size="sm" className="text-xs h-7" onClick={() => setExpanded(e => !e)}>
            {expanded ? 'Collapse' : 'DAG'}
          </Button>
        </div>
      </div>

      {job.checkpoints.length > 0 && (
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-xs text-muted-foreground">Checkpoints:</span>
          {job.checkpoints.map(ckpt => (
            <span key={ckpt.id} className="text-[10px] font-mono px-2 py-0.5 rounded bg-warning/10 text-warning border border-warning/20">
              {ckpt.step ? `step ${ckpt.step}` : new Date(ckpt.timestamp).toLocaleTimeString()} · {ckpt.size} GB
            </span>
          ))}
        </div>
      )}

      {expanded && (
        <div className="mt-3 border-t border-border pt-3">
          <div className="text-xs text-muted-foreground uppercase tracking-wide mb-2">DAG</div>
          <DAGVisualization nodes={job.nodes} />
        </div>
      )}
    </div>
  )
}

export function JobOrchestrationView({ jobs = DEMO_JOBS }: JobOrchestrationViewProps) {
  const [filter, setFilter] = useState<JobStatus | 'all'>('all')
  
  const filtered = useMemo(
    () => filter === 'all' ? jobs : jobs.filter(j => j.status === filter),
    [jobs, filter]
  )

  const counts = useMemo(() => {
    const c: Record<JobStatus | 'all', number> = { all: jobs.length, queued: 0, running: 0, checkpointed: 0, completed: 0, failed: 0 }
    jobs.forEach(j => c[j.status]++)
    return c
  }, [jobs])

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowClockwise className="text-primary" />
            Job Orchestration
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Stats */}
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-3">
            {(['running', 'queued', 'checkpointed', 'completed', 'failed'] as JobStatus[]).map(s => (
              <div key={s} className="space-y-1">
                <div className="text-xs text-muted-foreground uppercase tracking-wide">{s}</div>
                <div className={`font-mono text-xl font-bold ${STATUS_COLORS[s]}`}>{counts[s]}</div>
              </div>
            ))}
          </div>

          {/* Filter */}
          <div className="flex gap-2 flex-wrap">
            {(['all', 'running', 'queued', 'checkpointed', 'completed', 'failed'] as const).map(f => (
              <button
                key={f}
                onClick={() => setFilter(f)}
                className={`text-xs font-mono px-3 py-1 rounded border transition-colors ${
                  filter === f
                    ? 'bg-primary text-primary-foreground border-primary'
                    : 'border-border text-muted-foreground hover:text-foreground'
                }`}
              >
                {f} {f !== 'all' && `(${counts[f]})`}
              </button>
            ))}
          </div>
        </CardContent>
      </Card>

      <div className="space-y-3">
        {filtered.map(job => <JobCard key={job.id} job={job} />)}
        {filtered.length === 0 && (
          <div className="text-center py-8 text-muted-foreground font-mono">No jobs matching filter</div>
        )}
      </div>
    </div>
  )
}
