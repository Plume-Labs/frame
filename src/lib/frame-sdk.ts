/**
 * Frame SDK — TypeScript client for the Frame operator CRDs.
 *
 * Talks directly to the Kubernetes API. In dev, run `kubectl proxy --port=8001`
 * so that Vite can proxy /apis/* to the cluster. In production, inject the
 * ServiceAccount token via `window.__FRAME_TOKEN__`.
 *
 * @example
 * ```ts
 * const frame = new FrameClient()
 *
 * // Submit a GPU training job
 * const job = await frame.jobs.submit({ name: 'llm-run-4', pipeline: 'training', gpuCount: 8 })
 *
 * // Provision a new node (creates a FrameNode CR, controller applies machineConfig)
 * const { crName } = await frame.nodes.discover('192.168.10.25')
 * // poll frame.nodes.getStatus(crName) until phase === 'Discovered', then:
 * await frame.nodes.patchSpec(crName, { ip: '192.168.10.25', role: 'worker', disk: '/dev/nvme0n1' })
 * ```
 */

// ── Domain types ─────────────────────────────────────────────────────────────

export type ServiceClass  = 'HIGH' | 'MEDIUM' | 'LOW'
export type JobStatus     = 'queued' | 'running' | 'completed' | 'failed'
export type NodeStatus    = 'online' | 'degraded' | 'offline' | 'provisioning'
export type SchedulerType = 'volcano' | 'yunikorn' | 'default'
export type Priority      = 'critical' | 'high' | 'medium' | 'low'

export interface FrameNode {
  id: string
  name: string
  status: NodeStatus
  serviceClass: ServiceClass
  zone: string
  rackId: string
  cpu: number
  memory: number
  storage: number
  gpuCount: number
  gpuModel: string
}

export interface FrameNodeStatus {
  phase: string
  discoveredHostname?: string
  discoveredTalosVersion?: string
  discoveredDisks?: Array<{ name: string; size: string; type: string }>
  discoveredNICs?: Array<{ name: string; mac: string; speed: string }>
}

export interface FrameNodeSpec {
  ip: string
  role?: string
  disk?: string
  hostname?: string
  rack?: string
  zone?: string
  serviceClass?: string
  rdmaInterface?: string
  network?: {
    address?: string
    gateway?: string
    dns?: string[]
    vlan?: number
    bond?: string
  }
}

export interface Job {
  id: string
  name: string
  pipeline: string
  status: JobStatus
  serviceClass: ServiceClass
  priority: Priority
  namespace: string
  gpuCount: number
  createdAt: string
  startedAt?: string
  completedAt?: string
}

export interface JobSpec {
  name: string
  pipeline: string
  serviceClass?: ServiceClass
  priority?: Priority
  namespace?: string
  gpuCount?: number
}

export interface SchedulingPolicy {
  name: string
  scheduler: SchedulerType
  queue: string
  priority: number
  preemption: boolean
  maxGPUs: number
  maxCPUs: number
}

export interface ResourceQuota {
  namespace: string
  maxCPU: string
  maxMemory: string
  maxGPUs: number
  usedCPU: string
  usedMemory: string
  usedGPUs: number
}

export interface ServiceClassSummary {
  serviceClass: ServiceClass
  nodeCount: number
  totalGPUs: number
}

export interface HealthStatus {
  status: 'ok' | 'degraded'
  version: string
  uptime: number
}

// ── Error type ───────────────────────────────────────────────────────────────

export class FrameAPIError extends Error {
  constructor(
    public readonly statusCode: number,
    message: string,
  ) {
    super(message)
    this.name = 'FrameAPIError'
  }
}

// ── K8s API config ────────────────────────────────────────────────────────────

const GROUP   = 'frame.plume-labs.io'
const VERSION = 'v1alpha1'

function frameNs(override?: string): string {
  return override
    ?? (window as unknown as Record<string, string>).__FRAME_NAMESPACE__
    ?? 'default'
}

function bearerToken(): string | undefined {
  return (window as unknown as Record<string, string>).__FRAME_TOKEN__
}

function apiBase(plural: string, ns?: string): string {
  return `/apis/${GROUP}/${VERSION}/namespaces/${frameNs(ns)}/${plural}`
}

function toK8sName(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^-+|-+$/g, '').slice(0, 63)
}

async function k8sFetch<T>(
  path: string,
  opts: { method?: string; body?: unknown; contentType?: string } = {},
): Promise<T> {
  const headers: Record<string, string> = {}
  const tok = bearerToken()
  if (tok) headers['Authorization'] = `Bearer ${tok}`
  if (opts.body !== undefined) {
    headers['Content-Type'] = opts.contentType ?? 'application/json'
  }
  const res = await fetch(path, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const data = await res.json() as T & { message?: string }
  if (!res.ok) throw new FrameAPIError(res.status, (data as { message?: string }).message ?? res.statusText)
  return data
}

// ── CR type shims ─────────────────────────────────────────────────────────────

interface FrameJobCR {
  metadata: { name: string; namespace?: string; creationTimestamp?: string }
  spec: {
    pipeline: string; serviceClass?: string; priority?: string
    namespace?: string; gpuCount?: number; suspended?: boolean
  }
  status?: { phase?: string; startTime?: string; completionTime?: string }
}

interface FrameNodeCR {
  metadata: { name: string; namespace?: string }
  spec: { ip: string; serviceClass?: string; zone?: string; rack?: string; gpuCount?: number; rdmaInterface?: string; hostname?: string }
  status?: {
    phase?: string; capacity?: Record<string, string>; allocatable?: Record<string, string>
    discoveredHostname?: string; discoveredTalosVersion?: string
    discoveredDisks?: Array<{ name: string; size: string; type: string }>
    discoveredNICs?: Array<{ name: string; mac: string; speed: string }>
  }
}

interface SchedulingPolicyCR {
  metadata: { name: string; namespace?: string }
  spec: { scheduler?: string; queueName?: string; priorityValue?: number; preemption?: boolean }
}

interface FrameResourceQuotaCR {
  metadata: { name: string; namespace?: string }
  spec: { serviceClass: string; maxGPUs?: number; maxCPU?: string; maxMemory?: string }
}

interface ListResponse<T> { items: T[] }

// ── CR → domain mappers ───────────────────────────────────────────────────────

function mapJobPhase(phase?: string): JobStatus {
  switch (phase) {
    case 'Running':   return 'running'
    case 'Completed': return 'completed'
    case 'Failed':    return 'failed'
    default:          return 'queued'
  }
}

function crToJob(cr: FrameJobCR): Job {
  return {
    id:           cr.metadata.name,
    name:         cr.metadata.name,
    pipeline:     cr.spec.pipeline,
    status:       mapJobPhase(cr.status?.phase),
    serviceClass: (cr.spec.serviceClass ?? 'MEDIUM') as ServiceClass,
    priority:     (cr.spec.priority ?? 'medium') as Priority,
    namespace:    cr.spec.namespace ?? cr.metadata.namespace ?? frameNs(),
    gpuCount:     cr.spec.gpuCount ?? 0,
    createdAt:    cr.metadata.creationTimestamp ?? new Date().toISOString(),
    startedAt:    cr.status?.startTime,
    completedAt:  cr.status?.completionTime,
  }
}

function mapNodePhase(phase?: string): NodeStatus {
  switch (phase) {
    case 'Online':       return 'online'
    case 'Degraded':     return 'degraded'
    case 'Provisioning':
    case 'Discovering':
    case 'Discovered':   return 'provisioning'
    default:             return 'offline'
  }
}

function quantityToNum(q?: string): number {
  if (!q) return 0
  const n = parseFloat(q)
  return Number.isFinite(n) ? Math.round(n) : 0
}

function crToNode(cr: FrameNodeCR): FrameNode {
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

function crToPolicy(cr: SchedulingPolicyCR): SchedulingPolicy {
  return {
    name:       cr.metadata.name,
    scheduler:  (cr.spec.scheduler ?? 'default') as SchedulerType,
    queue:      cr.spec.queueName ?? '',
    priority:   cr.spec.priorityValue ?? 0,
    preemption: cr.spec.preemption ?? false,
    maxGPUs:    0,
    maxCPUs:    0,
  }
}

function crToQuota(cr: FrameResourceQuotaCR): ResourceQuota {
  return {
    namespace:  cr.metadata.namespace ?? frameNs(),
    maxCPU:     cr.spec.maxCPU ?? '0',
    maxMemory:  cr.spec.maxMemory ?? '0Gi',
    maxGPUs:    cr.spec.maxGPUs ?? 0,
    usedCPU:    '0',
    usedMemory: '0Gi',
    usedGPUs:   0,
  }
}

// ── Sub-clients ───────────────────────────────────────────────────────────────

class NodeClient {
  constructor(private readonly ns?: string) {}

  async list(): Promise<{ items: FrameNode[]; total: number }> {
    const res = await k8sFetch<ListResponse<FrameNodeCR>>(apiBase('framenodes', this.ns))
    const items = (res.items ?? []).map(crToNode)
    return { items, total: items.length }
  }

  async get(id: string): Promise<FrameNode> {
    const cr = await k8sFetch<FrameNodeCR>(`${apiBase('framenodes', this.ns)}/${id}`)
    return crToNode(cr)
  }

  async discover(ip: string): Promise<{ crName: string }> {
    const crName = toK8sName('frame-node-' + ip.replace(/\./g, '-'))
    await k8sFetch<FrameNodeCR>(apiBase('framenodes', this.ns), {
      method: 'POST',
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'FrameNode',
        metadata: { name: crName, namespace: frameNs(this.ns) },
        spec: { ip },
      },
    })
    return { crName }
  }

  async getStatus(name: string): Promise<FrameNodeStatus> {
    const cr = await k8sFetch<FrameNodeCR>(`${apiBase('framenodes', this.ns)}/${name}`)
    return {
      phase:                  cr.status?.phase ?? '',
      discoveredHostname:     cr.status?.discoveredHostname,
      discoveredTalosVersion: cr.status?.discoveredTalosVersion,
      discoveredDisks:        cr.status?.discoveredDisks,
      discoveredNICs:         cr.status?.discoveredNICs,
    }
  }

  async patchSpec(name: string, spec: FrameNodeSpec): Promise<void> {
    await k8sFetch<FrameNodeCR>(`${apiBase('framenodes', this.ns)}/${name}`, {
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec },
    })
  }

  async delete(name: string): Promise<void> {
    await k8sFetch<undefined>(`${apiBase('framenodes', this.ns)}/${name}`, { method: 'DELETE' })
  }
}

class JobClient {
  constructor(private readonly ns?: string) {}

  async list(): Promise<{ items: Job[]; total: number }> {
    const res = await k8sFetch<ListResponse<FrameJobCR>>(apiBase('framejobs', this.ns))
    const items = (res.items ?? []).map(crToJob)
    return { items, total: items.length }
  }

  async submit(spec: JobSpec): Promise<Job> {
    const crName = toK8sName(spec.name)
    const cr = await k8sFetch<FrameJobCR>(apiBase('framejobs', this.ns), {
      method: 'POST',
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'FrameJob',
        metadata: { name: crName, namespace: frameNs(this.ns) },
        spec: {
          pipeline:     spec.pipeline,
          serviceClass: spec.serviceClass ?? 'MEDIUM',
          priority:     spec.priority ?? 'medium',
          namespace:    spec.namespace ?? frameNs(this.ns),
          gpuCount:     spec.gpuCount ?? 0,
        },
      },
    })
    return crToJob(cr)
  }

  async cancel(id: string): Promise<{ cancelled: boolean; job: Job }> {
    const cr = await k8sFetch<FrameJobCR>(`${apiBase('framejobs', this.ns)}/${id}`)
    const job = crToJob(cr)
    await k8sFetch<undefined>(`${apiBase('framejobs', this.ns)}/${id}`, { method: 'DELETE' })
    return { cancelled: true, job }
  }
}

class SchedulerClient {
  constructor(private readonly ns?: string) {}

  async listPolicies(): Promise<{ items: SchedulingPolicy[]; total: number }> {
    const res = await k8sFetch<ListResponse<SchedulingPolicyCR>>(apiBase('schedulingpolicies', this.ns))
    const items = (res.items ?? []).map(crToPolicy)
    return { items, total: items.length }
  }

  async applyPolicy(policy: SchedulingPolicy): Promise<SchedulingPolicy> {
    const crName = toK8sName(policy.name)
    const specBody = { scheduler: policy.scheduler, queueName: policy.queue, priorityValue: policy.priority, preemption: policy.preemption }
    try {
      const res = await k8sFetch<SchedulingPolicyCR>(apiBase('schedulingpolicies', this.ns), {
        method: 'POST',
        body: { apiVersion: `${GROUP}/${VERSION}`, kind: 'SchedulingPolicy', metadata: { name: crName, namespace: frameNs(this.ns) }, spec: specBody },
      })
      return crToPolicy(res)
    } catch (e) {
      if (e instanceof FrameAPIError && e.statusCode === 409) {
        const res = await k8sFetch<SchedulingPolicyCR>(`${apiBase('schedulingpolicies', this.ns)}/${crName}`, {
          method: 'PATCH', contentType: 'application/merge-patch+json', body: { spec: specBody },
        })
        return crToPolicy(res)
      }
      throw e
    }
  }

  async deletePolicy(name: string): Promise<{ deleted: boolean; policy: SchedulingPolicy }> {
    const cr = await k8sFetch<SchedulingPolicyCR>(`${apiBase('schedulingpolicies', this.ns)}/${name}`)
    const policy = crToPolicy(cr)
    await k8sFetch<undefined>(`${apiBase('schedulingpolicies', this.ns)}/${name}`, { method: 'DELETE' })
    return { deleted: true, policy }
  }
}

class ResourceClient {
  constructor(private readonly ns?: string) {}

  async listQuotas(): Promise<{ items: ResourceQuota[]; total: number }> {
    const res = await k8sFetch<ListResponse<FrameResourceQuotaCR>>(apiBase('frameresourcequotas', this.ns))
    const items = (res.items ?? []).map(crToQuota)
    return { items, total: items.length }
  }

  async setQuota(namespace: string, quota: Partial<ResourceQuota> & { serviceClass?: ServiceClass }): Promise<ResourceQuota> {
    const sc = quota.serviceClass ?? 'MEDIUM'
    const crName = toK8sName(`frame-quota-${sc.toLowerCase()}`)
    const specBody = { serviceClass: sc, maxCPU: quota.maxCPU, maxMemory: quota.maxMemory, maxGPUs: quota.maxGPUs }
    try {
      const res = await k8sFetch<FrameResourceQuotaCR>(apiBase('frameresourcequotas', namespace), {
        method: 'POST',
        body: { apiVersion: `${GROUP}/${VERSION}`, kind: 'FrameResourceQuota', metadata: { name: crName, namespace: frameNs(namespace) }, spec: specBody },
      })
      return crToQuota(res)
    } catch (e) {
      if (e instanceof FrameAPIError && e.statusCode === 409) {
        const res = await k8sFetch<FrameResourceQuotaCR>(`${apiBase('frameresourcequotas', namespace)}/${crName}`, {
          method: 'PATCH', contentType: 'application/merge-patch+json', body: { spec: specBody },
        })
        return crToQuota(res)
      }
      throw e
    }
  }

  async listServiceClasses(): Promise<{ items: ServiceClassSummary[] }> {
    const res = await k8sFetch<ListResponse<FrameNodeCR>>(apiBase('framenodes', this.ns))
    const nodes = res.items ?? []
    const items = (['HIGH', 'MEDIUM', 'LOW'] as ServiceClass[]).map((sc) => ({
      serviceClass: sc,
      nodeCount:   nodes.filter((n) => n.spec.serviceClass === sc).length,
      totalGPUs:   nodes.filter((n) => n.spec.serviceClass === sc).reduce((s, n) => s + (n.spec.gpuCount ?? 0), 0),
    }))
    return { items }
  }
}

// ── Main client ───────────────────────────────────────────────────────────────

export interface FrameClientOptions {
  namespace?: string
}

/**
 * Top-level Frame SDK client. Communicates directly with the Kubernetes API.
 *
 * Dev: run `kubectl proxy --port=8001` so Vite proxies /apis to the cluster.
 * Prod: set `window.__FRAME_TOKEN__` to a ServiceAccount Bearer token before mounting the app.
 */
export class FrameClient {
  public readonly nodes: NodeClient
  public readonly jobs: JobClient
  public readonly scheduler: SchedulerClient
  public readonly resources: ResourceClient

  constructor(opts: FrameClientOptions = {}) {
    this.nodes     = new NodeClient(opts.namespace)
    this.jobs      = new JobClient(opts.namespace)
    this.scheduler = new SchedulerClient(opts.namespace)
    this.resources = new ResourceClient(opts.namespace)
  }

  async health(): Promise<HealthStatus> {
    try {
      await k8sFetch<unknown>(`/apis/${GROUP}/${VERSION}/`)
      return { status: 'ok', version: VERSION, uptime: 0 }
    } catch {
      return { status: 'degraded', version: VERSION, uptime: 0 }
    }
  }
}

export function createFrameClient(opts: FrameClientOptions = {}): FrameClient {
  return new FrameClient(opts)
}
