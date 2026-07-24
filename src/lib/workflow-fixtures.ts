import { ServiceClass } from '@/lib/types'

/**
 * Identity of the three demo workflows, shared by the two views that render
 * them: `JobOrchestrationView` (as a DAG) and `DataLineageView` (as a Gantt).
 *
 * Both used to hardcode the same `traceId`/name/`serviceClass`/start time, so a
 * rename in one view silently disagreed with the other. Only the identity tuple
 * lives here — each view keeps its own DAG-node / span arrays, which genuinely
 * differ in shape.
 */
export interface WorkflowIdentity {
  jobId: string
  traceId: string
  /** Human name — `Job.name` and `PipelineTrace.pipelineName` are the same string. */
  name: string
  serviceClass: ServiceClass
  /** Milliseconds before page load that the workflow started running. */
  startedMsAgo: number
}

export const WORKFLOW_IDENTITIES = [
  { jobId: 'wf-001', traceId: 'trace-abc-001', name: 'neura-training-v3',        serviceClass: 'LOW',  startedMsAgo: 7100000 },
  { jobId: 'wf-002', traceId: 'trace-def-002', name: 'neura-inference-batch-001', serviceClass: 'HIGH', startedMsAgo: 3590000 },
  { jobId: 'wf-003', traceId: 'trace-ghi-003', name: 'neura-training-v2-retry',   serviceClass: 'LOW',  startedMsAgo: 14000000 },
] as const satisfies readonly WorkflowIdentity[]

const BY_TRACE: Record<string, WorkflowIdentity> = Object.fromEntries(
  WORKFLOW_IDENTITIES.map((w) => [w.traceId, w]),
)

export type WorkflowTraceId = (typeof WORKFLOW_IDENTITIES)[number]['traceId']

/**
 * Identity fields for a `DataLineageView` trace. `Date.now()` is read at call
 * time so start times stay relative to page load, as before.
 */
export function traceIdentity(traceId: WorkflowTraceId): {
  traceId: string
  pipelineName: string
  serviceClass: ServiceClass
  startTime: number
} {
  const w = BY_TRACE[traceId]
  return {
    traceId: w.traceId,
    pipelineName: w.name,
    serviceClass: w.serviceClass,
    startTime: Date.now() - w.startedMsAgo,
  }
}

/**
 * Identity fields for a `JobOrchestrationView` job. `createdMsBeforeStart`
 * offsets `createdAt` above `startedAt` (queue wait), defaulting to 100 s.
 */
export function jobIdentity(
  traceId: WorkflowTraceId,
  createdMsBeforeStart = 100000,
): {
  id: string
  name: string
  serviceClass: ServiceClass
  traceId: string
  createdAt: number
  startedAt: number
} {
  const w = BY_TRACE[traceId]
  const startedAt = Date.now() - w.startedMsAgo
  return {
    id: w.jobId,
    name: w.name,
    serviceClass: w.serviceClass,
    traceId: w.traceId,
    createdAt: startedAt - createdMsBeforeStart,
    startedAt,
  }
}
