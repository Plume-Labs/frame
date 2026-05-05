/**
 * Frame SDK — TypeScript client for the Frame operator API.
 *
 * Operators, CI pipelines, and workload launchers can use this SDK to
 * interact with a running Frame API server programmatically.
 *
 * @example
 * ```ts
 * import { FrameClient } from '@/lib/frame-sdk'
 *
 * const frame = new FrameClient('http://frame-api.cluster.local:4000')
 *
 * // Submit a GPU training job
 * const job = await frame.jobs.submit({
 *   name: 'llm-finetune-v4',
 *   pipeline: 'training',
 *   serviceClass: 'HIGH',
 *   priority: 'critical',
 *   namespace: 'neura-prod',
 *   gpuCount: 8,
 * })
 *
 * // Apply a scheduling policy
 * await frame.scheduler.applyPolicy({
 *   name: 'hpc-critical',
 *   scheduler: 'volcano',
 *   queue: 'hpc',
 *   priority: 100,
 *   preemption: true,
 *   maxGPUs: 64,
 *   maxCPUs: 512,
 * })
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

// ── HTTP helper ───────────────────────────────────────────────────────────────

async function request<T>(
  baseUrl: string,
  path: string,
  method: string,
  apiKey: string | undefined,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (apiKey) headers['Authorization'] = `Bearer ${apiKey}`

  const res = await fetch(`${baseUrl}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  const contentType = res.headers.get('content-type') ?? ''
  let data: T | { error: string }
  if (contentType.includes('application/json')) {
    try {
      data = await res.json() as T | { error: string }
    } catch {
      throw new FrameAPIError(res.status, `Non-JSON response from ${path}: ${res.statusText}`)
    }
  } else {
    const text = await res.text()
    throw new FrameAPIError(res.status, text || res.statusText)
  }
  if (!res.ok) throw new FrameAPIError(res.status, (data as { error: string }).error ?? res.statusText)
  return data as T
}

// ── Sub-clients ───────────────────────────────────────────────────────────────

class NodeClient {
  constructor(private readonly base: string, private readonly apiKey?: string) {}

  list(): Promise<{ items: FrameNode[]; total: number }> {
    return request(this.base, '/api/nodes', 'GET', this.apiKey)
  }

  get(id: string): Promise<FrameNode> {
    return request(this.base, `/api/nodes/${id}`, 'GET', this.apiKey)
  }
}

class JobClient {
  constructor(private readonly base: string, private readonly apiKey?: string) {}

  list(): Promise<{ items: Job[]; total: number }> {
    return request(this.base, '/api/jobs', 'GET', this.apiKey)
  }

  submit(spec: JobSpec): Promise<Job> {
    return request(this.base, '/api/jobs', 'POST', this.apiKey, spec)
  }

  cancel(id: string): Promise<{ cancelled: boolean; job: Job }> {
    return request(this.base, `/api/jobs/${id}`, 'DELETE', this.apiKey)
  }
}

class SchedulerClient {
  constructor(private readonly base: string, private readonly apiKey?: string) {}

  listPolicies(): Promise<{ items: SchedulingPolicy[]; total: number }> {
    return request(this.base, '/api/scheduler/policies', 'GET', this.apiKey)
  }

  applyPolicy(policy: SchedulingPolicy): Promise<SchedulingPolicy> {
    return request(this.base, '/api/scheduler/policies', 'POST', this.apiKey, policy)
  }

  deletePolicy(name: string): Promise<{ deleted: boolean; policy: SchedulingPolicy }> {
    return request(this.base, `/api/scheduler/policies/${name}`, 'DELETE', this.apiKey)
  }
}

class ResourceClient {
  constructor(private readonly base: string, private readonly apiKey?: string) {}

  listQuotas(): Promise<{ items: ResourceQuota[]; total: number }> {
    return request(this.base, '/api/resources/quotas', 'GET', this.apiKey)
  }

  setQuota(namespace: string, quota: Partial<ResourceQuota>): Promise<ResourceQuota> {
    return request(this.base, `/api/resources/quotas/${namespace}`, 'PUT', this.apiKey, quota)
  }

  listServiceClasses(): Promise<{ items: ServiceClassSummary[] }> {
    return request(this.base, '/api/resources/service-classes', 'GET', this.apiKey)
  }
}

// ── Main client ───────────────────────────────────────────────────────────────

export interface FrameClientOptions {
  /** Bearer token for authenticating against the Frame API server. */
  apiKey?: string
}

/**
 * Top-level Frame SDK client.
 *
 * @param baseUrl - Base URL of the Frame API server, e.g. `http://localhost:4000`
 * @param opts    - Optional configuration (API key, etc.)
 */
export class FrameClient {
  public readonly nodes: NodeClient
  public readonly jobs: JobClient
  public readonly scheduler: SchedulerClient
  public readonly resources: ResourceClient

  private readonly base: string
  private readonly apiKey?: string

  constructor(baseUrl: string, opts: FrameClientOptions = {}) {
    this.base      = baseUrl.replace(/\/$/, '')
    this.apiKey    = opts.apiKey
    this.nodes     = new NodeClient(this.base, this.apiKey)
    this.jobs      = new JobClient(this.base, this.apiKey)
    this.scheduler = new SchedulerClient(this.base, this.apiKey)
    this.resources = new ResourceClient(this.base, this.apiKey)
  }

  health(): Promise<HealthStatus> {
    return request(this.base, '/api/health', 'GET', this.apiKey)
  }
}

// ── Convenience factory ───────────────────────────────────────────────────────

/**
 * Create a Frame SDK client pointing at the default local API server.
 */
export function createFrameClient(
  baseUrl = 'http://localhost:4000',
  opts: FrameClientOptions = {},
): FrameClient {
  return new FrameClient(baseUrl, opts)
}
