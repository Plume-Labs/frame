/**
 * Kubernetes client for Frame CRDs.
 *
 * Tries loadFromDefault() (kubeconfig or in-cluster SA).
 * If no config is found, k8sAvailable() returns false and
 * callers fall back to the in-memory simulation.
 */

import * as k8s from '@kubernetes/client-node'

const GROUP   = 'frame.plume-labs.io'
const VERSION = 'v1alpha1'

export const FRAME_NS = process.env.FRAME_NAMESPACE ?? 'default'

// ── Client init ──────────────────────────────────────────────────────────────

let api: k8s.CustomObjectsApi | null = null

try {
  const kc = new k8s.KubeConfig()
  kc.loadFromDefault()
  api = kc.makeApiClient(k8s.CustomObjectsApi)
  console.log('[k8s] Connected via', kc.getCurrentContext())
} catch {
  console.warn('[k8s] No cluster config found — falling back to simulation')
}

export function k8sAvailable(): boolean {
  return api !== null
}

// ── Type shims (raw CR shapes) ────────────────────────────────────────────────

interface FrameJobCR {
  metadata: { name: string; namespace?: string; creationTimestamp?: string; uid?: string }
  spec: {
    name?: string; pipeline: string; serviceClass?: string
    priority?: string; namespace?: string; gpuCount?: number
    suspended?: boolean; parameters?: Record<string, string>
  }
  status?: {
    phase?: string; startTime?: string; completionTime?: string; message?: string
  }
}

interface FrameNodeCR {
  metadata: { name: string; namespace?: string }
  spec: {
    ip: string; role: string; serviceClass?: string; zone?: string
    rack?: string; gpuCount?: number; rdmaInterface?: string; hostname?: string
    disk?: string
  }
  status?: {
    phase?: string; capacity?: Record<string, string>; allocatable?: Record<string, string>
    kubeletVersion?: string
  }
}

interface SchedulingPolicyCR {
  metadata: { name: string; namespace?: string }
  spec: {
    scheduler?: string; queueName?: string; priorityClass?: string
    priorityValue?: number; preemption?: boolean; gangScheduling?: boolean
  }
  status?: { conditions?: unknown[] }
}

interface FrameResourceQuotaCR {
  metadata: { name: string; namespace?: string }
  spec: { serviceClass: string; maxGPUs?: number; maxCPU?: string; maxMemory?: string; maxJobs?: number }
  status?: { conditions?: unknown[] }
}

interface ListResponse<T> { items: T[] }

// ── Helpers ───────────────────────────────────────────────────────────────────

async function list<T>(plural: string, namespace: string): Promise<T[]> {
  const res = await api!.listNamespacedCustomObject({ group: GROUP, version: VERSION, namespace, plural })
  return ((res as unknown as ListResponse<T>).items ?? [])
}

async function listAll<T>(plural: string): Promise<T[]> {
  const res = await api!.listClusterCustomObject({ group: GROUP, version: VERSION, plural })
  return ((res as unknown as ListResponse<T>).items ?? [])
}

// ── FrameJob ──────────────────────────────────────────────────────────────────

export type JobStatus = 'queued' | 'running' | 'completed' | 'failed'
export type ServiceClass = 'HIGH' | 'MEDIUM' | 'LOW'

export interface JobRecord {
  id: string; name: string; pipeline: string
  status: JobStatus; serviceClass: ServiceClass
  priority: 'critical' | 'high' | 'medium' | 'low'
  namespace: string; gpuCount: number
  createdAt: string; startedAt?: string; completedAt?: string
}

function mapJobPhase(phase?: string): JobStatus {
  switch (phase) {
    case 'Running': return 'running'
    case 'Completed': return 'completed'
    case 'Failed': return 'failed'
    default: return 'queued'
  }
}

function crToJob(cr: FrameJobCR): JobRecord {
  return {
    id:          cr.metadata.name,
    name:        cr.metadata.name,
    pipeline:    cr.spec.pipeline,
    status:      mapJobPhase(cr.status?.phase),
    serviceClass: (cr.spec.serviceClass ?? 'MEDIUM') as ServiceClass,
    priority:    (cr.spec.priority ?? 'medium') as JobRecord['priority'],
    namespace:   cr.spec.namespace ?? cr.metadata.namespace ?? FRAME_NS,
    gpuCount:    cr.spec.gpuCount ?? 0,
    createdAt:   cr.metadata.creationTimestamp ?? new Date().toISOString(),
    startedAt:   cr.status?.startTime,
    completedAt: cr.status?.completionTime,
  }
}

// CR name must be a valid k8s DNS subdomain — lowercase, alphanumeric + hyphens.
function toK8sName(name: string): string {
  return name.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^-+|-+$/g, '').slice(0, 63)
}

export async function listJobs(namespace = FRAME_NS): Promise<JobRecord[]> {
  const crs = await list<FrameJobCR>('framejobs', namespace)
  return crs.map(crToJob)
}

export async function createJob(body: {
  name: string; pipeline: string; serviceClass?: ServiceClass
  priority?: string; namespace?: string; gpuCount?: number
}): Promise<JobRecord> {
  const crName = toK8sName(body.name)
  const targetNS = body.namespace ?? FRAME_NS
  const cr = {
    apiVersion: `${GROUP}/${VERSION}`,
    kind: 'FrameJob',
    metadata: { name: crName, namespace: FRAME_NS },
    spec: {
      name: crName,
      pipeline: body.pipeline,
      serviceClass: body.serviceClass ?? 'MEDIUM',
      priority:     body.priority ?? 'medium',
      namespace:    targetNS,
      gpuCount:     body.gpuCount ?? 0,
    },
  }
  const res = await api!.createNamespacedCustomObject({
    group: GROUP, version: VERSION, namespace: FRAME_NS, plural: 'framejobs', body: cr,
  })
  return crToJob(res as unknown as FrameJobCR)
}

export async function deleteJob(name: string, namespace = FRAME_NS): Promise<void> {
  await api!.deleteNamespacedCustomObject({
    group: GROUP, version: VERSION, namespace, plural: 'framejobs', name,
  })
}

// ── FrameNode ─────────────────────────────────────────────────────────────────

export type NodeStatus = 'online' | 'degraded' | 'offline' | 'provisioning'

export interface NodeRecord {
  id: string; name: string; status: NodeStatus
  serviceClass: ServiceClass; zone: string; rackId: string
  cpu: number; memory: number; storage: number
  gpuCount: number; gpuModel: string
}

function mapNodePhase(phase?: string): NodeStatus {
  switch (phase) {
    case 'Online': return 'online'
    case 'Degraded': return 'degraded'
    case 'Provisioning': return 'provisioning'
    default: return 'offline'
  }
}

function quantityToNum(q?: string): number {
  if (!q) return 0
  const n = parseFloat(q)
  return Number.isFinite(n) ? Math.round(n) : 0
}

function crToNode(cr: FrameNodeCR): NodeRecord {
  const alloc = cr.status?.allocatable ?? {}
  return {
    id:           cr.metadata.name,
    name:         cr.spec.hostname ?? cr.metadata.name,
    status:       mapNodePhase(cr.status?.phase),
    serviceClass: (cr.spec.serviceClass ?? 'LOW') as ServiceClass,
    zone:         cr.spec.zone ?? '',
    rackId:       cr.spec.rack ?? '',
    cpu:          quantityToNum(alloc['cpu']),
    memory:       quantityToNum(alloc['memory']),
    storage:      0,
    gpuCount:     cr.spec.gpuCount ?? 0,
    gpuModel:     cr.spec.rdmaInterface ? 'RDMA' : 'Unknown',
  }
}

export async function listNodes(namespace = FRAME_NS): Promise<NodeRecord[]> {
  const crs = await list<FrameNodeCR>('framenodes', namespace)
  return crs.map(crToNode)
}

export async function getNodeByName(id: string, namespace = FRAME_NS): Promise<NodeRecord | null> {
  try {
    const cr = await api!.getNamespacedCustomObject({
      group: GROUP, version: VERSION, namespace, plural: 'framenodes', name: id,
    })
    return crToNode(cr as unknown as FrameNodeCR)
  } catch {
    return null
  }
}

// ── SchedulingPolicy ──────────────────────────────────────────────────────────

export interface PolicyRecord {
  name: string; scheduler: 'volcano' | 'yunikorn' | 'default'
  queue: string; priority: number; preemption: boolean
  maxGPUs: number; maxCPUs: number
}

function crToPolicy(cr: SchedulingPolicyCR): PolicyRecord {
  return {
    name:       cr.metadata.name,
    scheduler:  (cr.spec.scheduler ?? 'default') as PolicyRecord['scheduler'],
    queue:      cr.spec.queueName ?? '',
    priority:   cr.spec.priorityValue ?? 0,
    preemption: cr.spec.preemption ?? false,
    maxGPUs:    0,
    maxCPUs:    0,
  }
}

export async function listPolicies(namespace = FRAME_NS): Promise<PolicyRecord[]> {
  const crs = await list<SchedulingPolicyCR>('schedulingpolicies', namespace)
  return crs.map(crToPolicy)
}

export async function upsertPolicy(namespace: string, body: Partial<PolicyRecord> & { name: string }): Promise<PolicyRecord> {
  const crName = toK8sName(body.name)
  const cr = {
    apiVersion: `${GROUP}/${VERSION}`,
    kind: 'SchedulingPolicy',
    metadata: { name: crName, namespace },
    spec: {
      scheduler:     body.scheduler ?? 'default',
      queueName:     body.queue ?? '',
      priorityValue: body.priority ?? 0,
      preemption:    body.preemption ?? false,
    },
  }
  // Try create; if 409 Conflict, patch instead.
  try {
    const res = await api!.createNamespacedCustomObject({
      group: GROUP, version: VERSION, namespace, plural: 'schedulingpolicies', body: cr,
    })
    return crToPolicy(res as unknown as SchedulingPolicyCR)
  } catch (e: unknown) {
    const status = (e as { response?: { statusCode?: number } })?.response?.statusCode
    if (status !== 409) throw e
    const res = await api!.patchNamespacedCustomObject({
      group: GROUP, version: VERSION, namespace, plural: 'schedulingpolicies',
      name: crName, body: cr,
    })
    return crToPolicy(res as unknown as SchedulingPolicyCR)
  }
}

export async function deletePolicy(name: string, namespace = FRAME_NS): Promise<void> {
  await api!.deleteNamespacedCustomObject({
    group: GROUP, version: VERSION, namespace, plural: 'schedulingpolicies', name,
  })
}

// ── FrameResourceQuota ────────────────────────────────────────────────────────

export interface QuotaRecord {
  namespace: string
  maxCPU: string; maxMemory: string; maxGPUs: number
  usedCPU: string; usedMemory: string; usedGPUs: number
}

function crToQuota(cr: FrameResourceQuotaCR): QuotaRecord {
  return {
    namespace:  cr.metadata.namespace ?? FRAME_NS,
    maxCPU:     cr.spec.maxCPU ?? '0',
    maxMemory:  cr.spec.maxMemory ?? '0Gi',
    maxGPUs:    cr.spec.maxGPUs ?? 0,
    usedCPU:    '0',
    usedMemory: '0Gi',
    usedGPUs:   0,
  }
}

export async function listQuotas(namespace = FRAME_NS): Promise<QuotaRecord[]> {
  const crs = await list<FrameResourceQuotaCR>('frameresourcequotas', namespace)
  return crs.map(crToQuota)
}

export async function upsertQuota(namespace: string, serviceClass: ServiceClass, body: Partial<QuotaRecord>): Promise<QuotaRecord> {
  const crName = `frame-quota-${serviceClass.toLowerCase()}`
  const cr = {
    apiVersion: `${GROUP}/${VERSION}`,
    kind: 'FrameResourceQuota',
    metadata: { name: crName, namespace },
    spec: {
      serviceClass,
      maxCPU:    body.maxCPU,
      maxMemory: body.maxMemory,
      maxGPUs:   body.maxGPUs,
    },
  }
  try {
    const res = await api!.createNamespacedCustomObject({
      group: GROUP, version: VERSION, namespace, plural: 'frameresourcequotas', body: cr,
    })
    return crToQuota(res as unknown as FrameResourceQuotaCR)
  } catch (e: unknown) {
    const status = (e as { response?: { statusCode?: number } })?.response?.statusCode
    if (status !== 409) throw e
    const res = await api!.patchNamespacedCustomObject({
      group: GROUP, version: VERSION, namespace, plural: 'frameresourcequotas',
      name: crName, body: cr,
    })
    return crToQuota(res as unknown as FrameResourceQuotaCR)
  }
}
