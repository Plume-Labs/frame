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
  queueWeight: number
  priority: number
  preemption: boolean
  gangScheduling: boolean
  maxGPUs: number
  maxCPUs: number
}

export interface ResourceQuota {
  namespace: string
  serviceClass: ServiceClass
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

/** A real Kubernetes node, with live usage from metrics-server when available. */
export interface ClusterNodeInfo {
  name: string
  ready: boolean
  roles: string[]
  kubeletVersion: string
  os: string
  /** Allocatable/used, in whole units. */
  cpuCores: number
  cpuUsedCores?: number
  memGiB: number
  memUsedGiB?: number
  createdAt?: string
}

/** Where workloads actually run — pods grouped by the node scheduling them. */
export interface NodePlacement {
  node: string
  pods: Array<{ namespace: string; name: string; phase: string; app?: string }>
  running: number
  total: number
}

/** Live Ceph storage state, read from the Rook CephCluster CR + pods. */
export interface CephStatus {
  health: string
  version: string
  osds: number
  mons: number
  bytesTotal: number
  bytesUsed: number
  bytesAvailable: number
  pools: Array<{ name: string; replication: number }>
}

/** A real Kubernetes event. */
export interface ClusterEvent {
  reason: string
  message: string
  type: string
  involvedObject: string
  namespace?: string
  count: number
  lastTimestamp?: string
}

export type AppHealth = 'healthy' | 'degraded' | 'down'

/** One workload (Deployment or StatefulSet) making up an application. */
export interface AppComponent {
  name: string
  kind: 'Deployment' | 'StatefulSet'
  namespace: string
  /** `app.kubernetes.io/component` label, if set (e.g. api / client / worker). */
  role?: string
  readyReplicas: number
  desiredReplicas: number
  image: string
}

/**
 * A deployed application — the workloads sharing an
 * `app.kubernetes.io/instance` label (a Helm release), or failing that, a
 * namespace. This is how Neura (api + client + worker + postgres + redis)
 * shows up as a single entry.
 */
export interface Application {
  name: string
  namespace: string
  components: AppComponent[]
  readyReplicas: number
  desiredReplicas: number
  health: AppHealth
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
  spec: { scheduler?: string; queueName?: string; queueWeight?: number; priorityValue?: number; preemption?: boolean; gangScheduling?: boolean }
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
    queueWeight: cr.spec.queueWeight ?? 0,
    priority:   cr.spec.priorityValue ?? 0,
    preemption: cr.spec.preemption ?? false,
    gangScheduling: cr.spec.gangScheduling ?? false,
    maxGPUs:    0,
    maxCPUs:    0,
  }
}

function crToQuota(cr: FrameResourceQuotaCR): ResourceQuota {
  return {
    namespace:    cr.metadata.namespace ?? frameNs(),
    serviceClass: (cr.spec.serviceClass ?? 'MEDIUM') as ServiceClass,
    maxCPU:     cr.spec.maxCPU ?? '0',
    maxMemory:  cr.spec.maxMemory ?? '0Gi',
    maxGPUs:    cr.spec.maxGPUs ?? 0,
    usedCPU:    '0',
    usedMemory: '0Gi',
    usedGPUs:   0,
  }
}

// ── Workload (core apps/v1) shims + mappers ───────────────────────────────────

interface WorkloadCR {
  metadata: { name: string; namespace: string; labels?: Record<string, string> }
  spec?: {
    replicas?: number
    template?: { spec?: { containers?: Array<{ image?: string }> } }
  }
  status?: { readyReplicas?: number; replicas?: number }
}

const SYSTEM_NAMESPACES = new Set([
  'kube-system',
  'kube-public',
  'kube-node-lease',
])

function crToComponent(cr: WorkloadCR, kind: AppComponent['kind']): AppComponent {
  return {
    name:            cr.metadata.name,
    kind,
    namespace:       cr.metadata.namespace,
    role:            cr.metadata.labels?.['app.kubernetes.io/component'],
    readyReplicas:   cr.status?.readyReplicas ?? 0,
    desiredReplicas: cr.spec?.replicas ?? cr.status?.replicas ?? 0,
    image:           cr.spec?.template?.spec?.containers?.[0]?.image ?? 'unknown',
  }
}

/** Group key: the Helm release (instance label), else the namespace. */
function appKey(cr: WorkloadCR): string {
  return (
    cr.metadata.labels?.['app.kubernetes.io/instance'] ??
    cr.metadata.labels?.['app.kubernetes.io/part-of'] ??
    cr.metadata.namespace
  )
}

function healthOf(ready: number, desired: number): AppHealth {
  if (desired === 0) return 'down'
  if (ready >= desired) return 'healthy'
  if (ready === 0) return 'down'
  return 'degraded'
}

// ── Core K8s shims for the live cluster views ─────────────────────────────────

interface NodeItemCR {
  metadata: { name: string; labels?: Record<string, string>; creationTimestamp?: string }
  status?: {
    conditions?: Array<{ type: string; status: string }>
    capacity?: Record<string, string>
    allocatable?: Record<string, string>
    nodeInfo?: { kubeletVersion?: string; osImage?: string }
  }
}

interface NodeMetricsCR {
  metadata: { name: string }
  usage?: { cpu?: string; memory?: string }
}

interface EventCR {
  reason?: string
  message?: string
  type?: string
  count?: number
  lastTimestamp?: string
  eventTime?: string
  involvedObject?: { kind?: string; name?: string; namespace?: string }
}

/** Kubernetes CPU quantity → cores. `321055765n` → 0.32, `500m` → 0.5, `2` → 2. */
function cpuToCores(q?: string): number {
  if (!q) return 0
  if (q.endsWith('n')) return parseInt(q, 10) / 1e9
  if (q.endsWith('u')) return parseInt(q, 10) / 1e6
  if (q.endsWith('m')) return parseInt(q, 10) / 1e3
  return parseFloat(q)
}

/** Kubernetes memory quantity → GiB. Handles Ki/Mi/Gi and bare bytes. */
function memToGiB(q?: string): number {
  if (!q) return 0
  const units: Record<string, number> = { Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4 }
  const m = q.match(/^(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti)?$/)
  if (!m) return 0
  const bytes = parseFloat(m[1]) * (m[2] ? units[m[2]] : 1)
  return bytes / 1024 ** 3
}

// ── Sub-clients ───────────────────────────────────────────────────────────────

class ClusterClient {
  /** Real Kubernetes nodes, joined with metrics-server usage when present. */
  async nodes(): Promise<ClusterNodeInfo[]> {
    const nodes = await k8sFetch<ListResponse<NodeItemCR>>('/api/v1/nodes')

    let metricsByName = new Map<string, NodeMetricsCR>()
    try {
      const metrics = await k8sFetch<ListResponse<NodeMetricsCR>>(
        '/apis/metrics.k8s.io/v1beta1/nodes',
      )
      metricsByName = new Map((metrics.items ?? []).map((m) => [m.metadata.name, m]))
    } catch {
      // metrics-server absent — usage stays undefined, capacity still shows.
    }

    return (nodes.items ?? [])
      .map((n) => {
        const ready =
          n.status?.conditions?.find((c) => c.type === 'Ready')?.status === 'True'
        const roles = Object.keys(n.metadata.labels ?? {})
          .filter((k) => k.startsWith('node-role.kubernetes.io/'))
          .map((k) => k.split('/')[1])
          .filter(Boolean)
        const usage = metricsByName.get(n.metadata.name)?.usage
        return {
          name: n.metadata.name,
          ready,
          roles: roles.length ? roles : ['worker'],
          kubeletVersion: n.status?.nodeInfo?.kubeletVersion ?? 'unknown',
          os: n.status?.nodeInfo?.osImage ?? 'unknown',
          cpuCores: cpuToCores(n.status?.capacity?.cpu),
          cpuUsedCores: usage ? cpuToCores(usage.cpu) : undefined,
          memGiB: memToGiB(n.status?.capacity?.memory),
          memUsedGiB: usage ? memToGiB(usage.memory) : undefined,
          createdAt: n.metadata.creationTimestamp,
        }
      })
      .sort((a, b) => a.name.localeCompare(b.name))
  }

  /**
   * Live Ceph state from the Rook CephCluster CR, its CephBlockPools, and the
   * running OSD/mon pods. Throws if Rook is not installed (no CephCluster).
   */
  async ceph(): Promise<CephStatus> {
    const ns = 'rook-ceph'
    const [cluster, pools, osdPods, monPods] = await Promise.all([
      k8sFetch<{
        status?: {
          ceph?: {
            health?: string
            capacity?: { bytesTotal?: number; bytesUsed?: number; bytesAvailable?: number }
            versions?: { overall?: Record<string, number> }
          }
        }
      }>(`/apis/ceph.rook.io/v1/namespaces/${ns}/cephclusters/rook-ceph`),
      k8sFetch<ListResponse<{ metadata: { name: string }; spec?: { replicated?: { size?: number } } }>>(
        `/apis/ceph.rook.io/v1/namespaces/${ns}/cephblockpools`,
      ),
      k8sFetch<ListResponse<{ status?: { phase?: string } }>>(
        `/api/v1/namespaces/${ns}/pods?labelSelector=app%3Drook-ceph-osd`,
      ),
      k8sFetch<ListResponse<{ status?: { phase?: string } }>>(
        `/api/v1/namespaces/${ns}/pods?labelSelector=app%3Drook-ceph-mon`,
      ),
    ])

    const cap = cluster.status?.ceph?.capacity ?? {}
    const version = Object.keys(cluster.status?.ceph?.versions?.overall ?? {})[0] ?? ''
    const running = (list: ListResponse<{ status?: { phase?: string } }>) =>
      (list.items ?? []).filter((p) => p.status?.phase === 'Running').length

    return {
      health: cluster.status?.ceph?.health ?? 'UNKNOWN',
      version: version.replace(/^ceph version /, '').split(' ')[0] ?? '',
      osds: running(osdPods),
      mons: running(monPods),
      bytesTotal: cap.bytesTotal ?? 0,
      bytesUsed: cap.bytesUsed ?? 0,
      bytesAvailable: cap.bytesAvailable ?? 0,
      pools: (pools.items ?? []).map((p) => ({
        name: p.metadata.name,
        replication: p.spec?.replicated?.size ?? 0,
      })),
    }
  }

  /**
   * Workload placement: every non-system pod grouped by the node running it —
   * the real "data locality" of the cluster (where compute actually sits).
   */
  async placement(): Promise<NodePlacement[]> {
    const res = await k8sFetch<
      ListResponse<{
        metadata: { name: string; namespace: string; labels?: Record<string, string> }
        spec?: { nodeName?: string }
        status?: { phase?: string }
      }>
    >('/api/v1/pods')

    const byNode = new Map<string, NodePlacement>()
    for (const p of res.items ?? []) {
      if (SYSTEM_NAMESPACES.has(p.metadata.namespace)) continue
      const node = p.spec?.nodeName
      if (!node) continue
      const phase = p.status?.phase ?? 'Unknown'
      const entry = byNode.get(node) ?? { node, pods: [], running: 0, total: 0 }
      entry.pods.push({
        namespace: p.metadata.namespace,
        name: p.metadata.name,
        phase,
        app: p.metadata.labels?.['app.kubernetes.io/instance'],
      })
      entry.total += 1
      if (phase === 'Running') entry.running += 1
      byNode.set(node, entry)
    }
    return Array.from(byNode.values())
      .map((n) => ({ ...n, pods: n.pods.sort((a, b) => a.namespace.localeCompare(b.namespace)) }))
      .sort((a, b) => a.node.localeCompare(b.node))
  }

  /** Recent Kubernetes events across all namespaces, newest first. */
  async events(limit = 100): Promise<ClusterEvent[]> {
    const res = await k8sFetch<ListResponse<EventCR>>('/api/v1/events')
    return (res.items ?? [])
      .map((e) => ({
        reason: e.reason ?? '',
        message: e.message ?? '',
        type: e.type ?? 'Normal',
        involvedObject: [e.involvedObject?.kind, e.involvedObject?.name]
          .filter(Boolean)
          .join('/'),
        namespace: e.involvedObject?.namespace,
        count: e.count ?? 1,
        lastTimestamp: e.lastTimestamp ?? e.eventTime,
      }))
      .sort((a, b) => (b.lastTimestamp ?? '').localeCompare(a.lastTimestamp ?? ''))
      .slice(0, limit)
  }
}

class ApplicationClient {
  /**
   * List deployed applications across all non-system namespaces by reading
   * Deployments and StatefulSets and grouping them by Helm release.
   */
  async list(): Promise<Application[]> {
    const [deps, sts] = await Promise.all([
      k8sFetch<ListResponse<WorkloadCR>>('/apis/apps/v1/deployments'),
      k8sFetch<ListResponse<WorkloadCR>>('/apis/apps/v1/statefulsets'),
    ])

    const workloads: Array<{ cr: WorkloadCR; kind: AppComponent['kind'] }> = [
      ...(deps.items ?? []).map((cr) => ({ cr, kind: 'Deployment' as const })),
      ...(sts.items ?? []).map((cr) => ({ cr, kind: 'StatefulSet' as const })),
    ].filter(({ cr }) => !SYSTEM_NAMESPACES.has(cr.metadata.namespace))

    const groups = new Map<string, Application>()
    for (const { cr, kind } of workloads) {
      const key = appKey(cr)
      const component = crToComponent(cr, kind)
      const app = groups.get(key) ?? {
        name: key,
        namespace: cr.metadata.namespace,
        components: [],
        readyReplicas: 0,
        desiredReplicas: 0,
        health: 'down' as AppHealth,
      }
      app.components.push(component)
      app.readyReplicas += component.readyReplicas
      app.desiredReplicas += component.desiredReplicas
      groups.set(key, app)
    }

    return Array.from(groups.values())
      .map((app) => ({
        ...app,
        components: app.components.sort((a, b) => a.name.localeCompare(b.name)),
        health: healthOf(app.readyReplicas, app.desiredReplicas),
      }))
      .sort((a, b) => a.name.localeCompare(b.name))
  }
}

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
  public readonly apps: ApplicationClient
  public readonly cluster: ClusterClient

  constructor(opts: FrameClientOptions = {}) {
    this.nodes     = new NodeClient(opts.namespace)
    this.jobs      = new JobClient(opts.namespace)
    this.scheduler = new SchedulerClient(opts.namespace)
    this.resources = new ResourceClient(opts.namespace)
    this.apps      = new ApplicationClient()
    this.cluster   = new ClusterClient()
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
