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

/** One Alluxio tiered-storage layer (MEM/SSD/HDD). Bytes. */
export interface AlluxioTier {
  name: string
  totalBytes: number
  usedBytes: number
}

/** Live Alluxio tiered cache: stacked storage layers + cluster cache hit-rate. */
export interface AlluxioStats {
  tiers: AlluxioTier[]
  cacheHitRate: number
  totalBytes: number
  usedBytes: number
}

/** Live KSM (Kernel Same-page Merging) per node, from node-exporter's ksmd collector. */
export interface KsmNode {
  node: string
  run: boolean
  pagesShared: number
  pagesSharing: number
  savedMiB: number
  fullScans: number
}
export interface KsmStats {
  nodes: KsmNode[]
  enabledNodes: number
  totalSavedMiB: number
  totalPagesSharing: number
}

/** One network interface's live counters (node-exporter netdev). */
export interface NetIface {
  device: string
  rxBytes: number
  txBytes: number
  rxPackets: number
  txPackets: number
  rxErrs: number
  txErrs: number
  rxDrop: number
  txDrop: number
}

/** Per-node network detail across the k3s stack (physical NIC / VXLAN overlay / pod bridge). */
export interface NetNode {
  node: string
  ifaces: NetIface[]
}

/** Live cluster capacity for one resource: allocatable vs live-used vs reserved (requests). */
export interface CapacityResource {
  name: 'CPU' | 'Memory'
  unit: string
  allocatable: number
  used: number
  requested: number
}

/** A Volcano scheduling queue (an elastic resource pool). */
export interface VolcanoQueue {
  name: string
  state: string
  weight: number
  reclaimable: boolean
  cpuCapability: string
  memCapability: string
  running: number
}
/** A Volcano PodGroup (gang-scheduled unit). */
export interface VolcanoPodGroup {
  name: string
  namespace: string
  queue: string
  phase: string
  minMember: number
}
export interface VolcanoStats {
  queues: VolcanoQueue[]
  podGroups: VolcanoPodGroup[]
}

/** One step (Argo Workflow node) within a pipeline trace. */
export interface WorkflowSpan {
  name: string
  phase: string
  startedAt?: string
  finishedAt?: string
  durationMs: number
}
/** A pipeline run (Argo Workflow) as a lineage trace. */
export interface WorkflowTrace {
  name: string
  phase: string
  startedAt?: string
  totalDurationMs: number
  spans: WorkflowSpan[]
}

/** Live cluster reliability posture (no simulated MTBF — real signals only). */
export interface DisruptionBudget {
  name: string
  namespace: string
  currentHealthy: number
  desiredHealthy: number
  disruptionsAllowed: number
}
export interface RestartHotspot {
  pod: string
  namespace: string
  restarts: number
}
export interface Resilience {
  cephHealth: string
  cephOsds: number
  cephReplication: number
  pdbs: DisruptionBudget[]
  pdbAtRisk: number
  totalRestarts: number
  hotspots: RestartHotspot[]
}

/** Burst-buffer SSD tier per node (a fast local scratch mount). */
export interface BurstNode {
  node: string
  totalBytes: number
  usedBytes: number
}
/** Clock synchronisation per node (kernel adjtimex / ptp_kvm PHC). */
export interface PtpNode {
  node: string
  offsetSeconds: number
  synced: boolean
  freqPpm: number
  maxErrorSeconds: number
}

/** A node as seen within its rack. */
export interface RackNodeInfo {
  name: string
  ready: boolean
  role: string
  cpuCores: number
  memGiB: number
  pods: number
}
/**
 * A rack: nodes grouped by physical topology via the `topology.frame.io/rack`
 * node label (falls back to the FrameNode `spec.rack`). On bare metal — Frame's
 * primary target — this is the datacenter rack. On the virtualized test cluster
 * the label resolves to the hypervisor host and `physical` is additionally set
 * (host capacity + VM oversubscription); it is absent on metal.
 */
export interface Rack {
  name: string
  nodes: RackNodeInfo[]
  readyNodes: number
  totalCpu: number
  totalMem: number
  totalPods: number
  physical?: { hypervisor: string; pcpu: number; pmemGiB: number }
}

/** Live GPU telemetry per device, from DCGM-exporter. */
export interface GpuInfo {
  index: string
  model: string
  node: string
  utilPct: number
  memUsedMB: number
  memTotalMB: number
  tempC: number
  powerW: number
  encUtil: number
  decUtil: number
}

/** A Falco runtime-security detection, aggregated by rule + workload. */
export interface SecurityEvent {
  rule: string
  priority: string // warning | error | critical | notice | …
  priorityRank: number // Falco numeric (0 = emergency … 7 = debug); lower = worse
  node: string
  namespace: string
  pod: string
  source: string // syscall | k8s_audit | …
  count: number
}

export interface SecurityStatus {
  events: SecurityEvent[]
  total: number
  byPriority: Record<string, number>
}

/** trivy-operator security posture (image vulnerabilities + config misconfigs). */
export interface PostureSummary {
  critical: number
  high: number
  medium: number
  low: number
}
export interface VulnerableImage {
  image: string
  critical: number
  high: number
}
export interface MisconfigCheck {
  id: string
  title: string
  severity: string
  count: number
}
export interface PostureStatus {
  vulns: PostureSummary & { images: number }
  topImages: VulnerableImage[]
  misconfigs: PostureSummary & { resources: number }
  topChecks: MisconfigCheck[]
}

export interface InferenceStatus {
  model: string
  node: string
  nCtx: number
  slots: number
  kvTokens: number
  kvUsePct: number
  requestsProcessing: number
  requestsDeferred: number
  promptTokensPerSec: number
  predictedTokensPerSec: number
  promptTokensTotal: number
  tokensPredictedTotal: number
  busySlotsPerDecode: number
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

  /**
   * Live Alluxio tiered storage — MEM/SSD/HDD capacity + used and the cluster
   * cache hit-rate, read from the Alluxio master metrics over the pod-proxy.
   */
  async alluxio(namespace = 'alluxio'): Promise<AlluxioStats> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      `/api/v1/namespaces/${namespace}/pods?labelSelector=app%3Dalluxio`,
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) throw new FrameAPIError(404, 'Alluxio not deployed')

    const res = await fetch(
      `/api/v1/namespaces/${namespace}/pods/${name}:19999/proxy/metrics/json/`,
    )
    if (!res.ok) throw new FrameAPIError(res.status, 'cannot read Alluxio metrics')
    const g = ((await res.json()) as { gauges: Record<string, { value: number }> }).gauges
    const v = (k: string) => g[k]?.value ?? 0

    const tiers: AlluxioTier[] = ['MEM', 'SSD', 'HDD']
      .map((t) => ({
        name: t,
        totalBytes: v(`Cluster.CapacityTotalTier${t}`),
        usedBytes: v(`Cluster.CapacityUsedTier${t}`),
      }))
      .filter((t) => t.totalBytes > 0)

    const rate = v('Cluster.CacheHitRate')
    return {
      tiers,
      cacheHitRate: rate <= 1 ? rate * 100 : rate,
      totalBytes: v('Cluster.CapacityTotal'),
      usedBytes: v('Cluster.CapacityUsed'),
    }
  }

  /** Fetch node-exporter metrics text per node (pod-proxy), keyed by node name. */
  private async nodeExporterMetrics(): Promise<Array<{ node: string; text: string }>> {
    const pods = await k8sFetch<
      ListResponse<{ metadata: { name: string }; spec?: { nodeName?: string } }>
    >('/api/v1/namespaces/monitoring/pods?labelSelector=app%3Dnode-exporter')
    const results = await Promise.all(
      (pods.items ?? []).map(async (p) => {
        try {
          const res = await fetch(
            `/api/v1/namespaces/monitoring/pods/${p.metadata.name}:9100/proxy/metrics`,
          )
          return { node: p.spec?.nodeName ?? p.metadata.name, text: res.ok ? await res.text() : '' }
        } catch {
          return { node: p.spec?.nodeName ?? p.metadata.name, text: '' }
        }
      }),
    )
    return results.filter((r) => r.text)
  }

  /** Live KSM stats aggregated across nodes (node-exporter ksmd collector). */
  async ksm(): Promise<KsmStats> {
    const metrics = await this.nodeExporterMetrics()
    if (!metrics.length) throw new FrameAPIError(404, 'node-exporter not deployed')
    const g = (text: string, key: string) => {
      const m = text.match(new RegExp(`^${key}\\s+([0-9.e+-]+)`, 'm'))
      return m ? Number(m[1]) : 0
    }
    const nodes: KsmNode[] = metrics.map(({ node, text }) => {
      const sharing = g(text, 'node_ksmd_pages_sharing')
      return {
        node,
        run: g(text, 'node_ksmd_run') === 1,
        pagesShared: g(text, 'node_ksmd_pages_shared'),
        pagesSharing: sharing,
        savedMiB: (sharing * 4096) / 1024 ** 2,
        fullScans: g(text, 'node_ksmd_full_scans_total'),
      }
    })
    return {
      nodes: nodes.sort((a, b) => a.node.localeCompare(b.node)),
      enabledNodes: nodes.filter((n) => n.run).length,
      totalSavedMiB: nodes.reduce((s, n) => s + n.savedMiB, 0),
      totalPagesSharing: nodes.reduce((s, n) => s + n.pagesSharing, 0),
    }
  }

  /**
   * Per-node network detail across the k3s stack: the physical NIC (eth0),
   * the flannel VXLAN overlay (flannel.1) and the pod bridge (cni0), with
   * bytes/packets/errors/drops each.
   */
  async network(devices = ['eth0', 'flannel.1', 'cni0']): Promise<NetNode[]> {
    const metrics = await this.nodeExporterMetrics()
    if (!metrics.length) throw new FrameAPIError(404, 'node-exporter not deployed')

    const g = (text: string, key: string, dev: string) => {
      const m = text.match(
        new RegExp(`^${key}\\{device="${dev.replace('.', '\\.')}"\\}\\s+([0-9.e+-]+)`, 'm'),
      )
      return m ? Number(m[1]) : 0
    }

    return metrics
      .map(({ node, text }) => ({
        node,
        ifaces: devices
          .map((dev) => ({
            device: dev,
            rxBytes: g(text, 'node_network_receive_bytes_total', dev),
            txBytes: g(text, 'node_network_transmit_bytes_total', dev),
            rxPackets: g(text, 'node_network_receive_packets_total', dev),
            txPackets: g(text, 'node_network_transmit_packets_total', dev),
            rxErrs: g(text, 'node_network_receive_errs_total', dev),
            txErrs: g(text, 'node_network_transmit_errs_total', dev),
            rxDrop: g(text, 'node_network_receive_drop_total', dev),
            txDrop: g(text, 'node_network_transmit_drop_total', dev),
          }))
          .filter((i) => i.rxBytes > 0 || i.txBytes > 0),
      }))
      .sort((a, b) => a.node.localeCompare(b.node))
  }

  /**
   * Live cluster capacity: allocatable (sum node allocatable), used (metrics-server),
   * and requested (sum of pod container requests) for CPU and memory.
   */
  async capacity(): Promise<CapacityResource[]> {
    const [nodes, pods] = await Promise.all([
      k8sFetch<ListResponse<{ status?: { allocatable?: Record<string, string> } }>>(
        '/api/v1/nodes',
      ),
      k8sFetch<
        ListResponse<{
          status?: { phase?: string }
          spec?: { containers?: Array<{ resources?: { requests?: Record<string, string> } }> }
        }>
      >('/api/v1/pods'),
    ])

    let allocCpu = 0
    let allocMem = 0
    for (const n of nodes.items ?? []) {
      allocCpu += cpuToCores(n.status?.allocatable?.cpu)
      allocMem += memToGiB(n.status?.allocatable?.memory)
    }

    let reqCpu = 0
    let reqMem = 0
    for (const p of pods.items ?? []) {
      if (p.status?.phase === 'Succeeded' || p.status?.phase === 'Failed') continue
      for (const c of p.spec?.containers ?? []) {
        reqCpu += cpuToCores(c.resources?.requests?.cpu)
        reqMem += memToGiB(c.resources?.requests?.memory)
      }
    }

    let usedCpu = 0
    let usedMem = 0
    try {
      const m = await k8sFetch<ListResponse<NodeMetricsCR>>(
        '/apis/metrics.k8s.io/v1beta1/nodes',
      )
      for (const n of m.items ?? []) {
        usedCpu += cpuToCores(n.usage?.cpu)
        usedMem += memToGiB(n.usage?.memory)
      }
    } catch {
      // metrics-server absent — used stays 0
    }

    return [
      { name: 'CPU', unit: 'cores', allocatable: allocCpu, used: usedCpu, requested: reqCpu },
      { name: 'Memory', unit: 'GiB', allocatable: allocMem, used: usedMem, requested: reqMem },
    ]
  }

  /** Live Volcano queues (elastic pools) + gang-scheduled PodGroups. */
  async volcano(): Promise<VolcanoStats> {
    const [queues, pgs] = await Promise.all([
      k8sFetch<
        ListResponse<{
          metadata: { name: string }
          spec?: { weight?: number; reclaimable?: boolean; capability?: Record<string, string> }
          status?: { state?: string; running?: number }
        }>
      >('/apis/scheduling.volcano.sh/v1beta1/queues'),
      k8sFetch<
        ListResponse<{
          metadata: { name: string; namespace: string }
          spec?: { queue?: string; minMember?: number }
          status?: { phase?: string }
        }>
      >('/apis/scheduling.volcano.sh/v1beta1/podgroups'),
    ])

    return {
      queues: (queues.items ?? [])
        .filter((q) => !['root', 'default'].includes(q.metadata.name))
        .map((q) => ({
          name: q.metadata.name,
          state: q.status?.state ?? 'Unknown',
          weight: q.spec?.weight ?? 0,
          reclaimable: q.spec?.reclaimable ?? false,
          cpuCapability: q.spec?.capability?.cpu ?? '—',
          memCapability: q.spec?.capability?.memory ?? '—',
          running: q.status?.running ?? 0,
        }))
        .sort((a, b) => b.weight - a.weight),
      podGroups: (pgs.items ?? []).map((p) => ({
        name: p.metadata.name,
        namespace: p.metadata.namespace,
        queue: p.spec?.queue ?? '',
        phase: p.status?.phase ?? 'Unknown',
        minMember: p.spec?.minMember ?? 0,
      })),
    }
  }

  /** Live pipeline lineage from Argo Workflows: each run's DAG steps as timed spans. */
  async workflows(namespace = 'argo'): Promise<WorkflowTrace[]> {
    const res = await k8sFetch<
      ListResponse<{
        metadata: { name: string }
        status?: {
          phase?: string
          startedAt?: string
          finishedAt?: string
          nodes?: Record<
            string,
            { displayName?: string; type?: string; phase?: string; startedAt?: string; finishedAt?: string }
          >
        }
      }>
    >(`/apis/argoproj.io/v1alpha1/namespaces/${namespace}/workflows`)

    const ms = (a?: string, b?: string) =>
      a && b ? Math.max(0, new Date(b).getTime() - new Date(a).getTime()) : 0

    return (res.items ?? [])
      .map((w) => {
        const spans: WorkflowSpan[] = Object.values(w.status?.nodes ?? {})
          .filter((n) => n.type === 'Pod')
          .map((n) => ({
            name: n.displayName ?? '',
            phase: n.phase ?? 'Unknown',
            startedAt: n.startedAt,
            finishedAt: n.finishedAt,
            durationMs: ms(n.startedAt, n.finishedAt),
          }))
          .sort((a, b) => (a.startedAt ?? '').localeCompare(b.startedAt ?? ''))
        return {
          name: w.metadata.name,
          phase: w.status?.phase ?? 'Unknown',
          startedAt: w.status?.startedAt,
          totalDurationMs: ms(w.status?.startedAt, w.status?.finishedAt),
          spans,
        }
      })
      .sort((a, b) => (b.startedAt ?? '').localeCompare(a.startedAt ?? ''))
  }

  /**
   * Live reliability posture: data durability (Ceph), PodDisruptionBudgets and
   * pod restart hotspots — real signals, not a simulated MTBF/checkpoint feed.
   */
  async resilience(): Promise<Resilience> {
    const [pdbRes, podRes] = await Promise.all([
      k8sFetch<
        ListResponse<{
          metadata: { name: string; namespace: string }
          status?: { currentHealthy?: number; desiredHealthy?: number; disruptionsAllowed?: number }
        }>
      >('/apis/policy/v1/poddisruptionbudgets'),
      k8sFetch<
        ListResponse<{
          metadata: { name: string; namespace: string }
          status?: { containerStatuses?: Array<{ restartCount?: number }> }
        }>
      >('/api/v1/pods'),
    ])

    let ceph: CephStatus | null = null
    try {
      ceph = await this.ceph()
    } catch {
      ceph = null
    }

    const pdbs: DisruptionBudget[] = (pdbRes.items ?? [])
      .filter((p) => !SYSTEM_NAMESPACES.has(p.metadata.namespace))
      .map((p) => ({
        name: p.metadata.name,
        namespace: p.metadata.namespace,
        currentHealthy: p.status?.currentHealthy ?? 0,
        desiredHealthy: p.status?.desiredHealthy ?? 0,
        disruptionsAllowed: p.status?.disruptionsAllowed ?? 0,
      }))

    const hotspots: RestartHotspot[] = (podRes.items ?? [])
      .filter((p) => !SYSTEM_NAMESPACES.has(p.metadata.namespace))
      .map((p) => ({
        pod: p.metadata.name,
        namespace: p.metadata.namespace,
        restarts: (p.status?.containerStatuses ?? []).reduce((s, c) => s + (c.restartCount ?? 0), 0),
      }))
      .filter((h) => h.restarts > 0)
      .sort((a, b) => b.restarts - a.restarts)

    return {
      cephHealth: ceph?.health ?? 'N/A',
      cephOsds: ceph?.osds ?? 0,
      cephReplication: ceph?.pools?.[0]?.replication ?? 0,
      pdbs,
      pdbAtRisk: pdbs.filter((p) => p.disruptionsAllowed === 0).length,
      totalRestarts: hotspots.reduce((s, h) => s + h.restarts, 0),
      hotspots: hotspots.slice(0, 8),
    }
  }

  /** Burst-buffer SSD tier: capacity/used of the /burst-buffer mount per node. */
  async burstBuffer(mount = '/burst-buffer'): Promise<BurstNode[]> {
    const metrics = await this.nodeExporterMetrics()
    if (!metrics.length) throw new FrameAPIError(404, 'node-exporter not deployed')
    const g = (text: string, key: string) => {
      const m = text.match(new RegExp(`^${key}\\{[^}]*mountpoint="${mount}"[^}]*\\}\\s+([0-9.e+-]+)`, 'm'))
      return m ? Number(m[1]) : 0
    }
    return metrics
      .map(({ node, text }) => {
        const size = g(text, 'node_filesystem_size_bytes')
        const avail = g(text, 'node_filesystem_avail_bytes')
        return { node, totalBytes: size, usedBytes: Math.max(0, size - avail) }
      })
      .filter((b) => b.totalBytes > 0)
      .sort((a, b) => a.node.localeCompare(b.node))
  }

  /** Per-node clock sync (kernel timex; a ptp_kvm PHC disciplines it to the host). */
  async ptp(): Promise<PtpNode[]> {
    const metrics = await this.nodeExporterMetrics()
    if (!metrics.length) throw new FrameAPIError(404, 'node-exporter not deployed')
    const g = (text: string, key: string) => {
      const m = text.match(new RegExp(`^${key}\\s+([0-9.e+-]+)`, 'm'))
      return m ? Number(m[1]) : 0
    }
    return metrics
      .map(({ node, text }) => ({
        node,
        offsetSeconds: g(text, 'node_timex_offset_seconds'),
        synced: g(text, 'node_timex_sync_status') === 1,
        freqPpm: (g(text, 'node_timex_frequency_adjustment_ratio') - 1) * 1e6,
        maxErrorSeconds: g(text, 'node_timex_maxerror_seconds'),
      }))
      .sort((a, b) => a.node.localeCompare(b.node))
  }

  /** Real nodes grouped into racks by their FrameNode `spec.rack` label. */
  async racks(): Promise<Rack[]> {
    const [fnRes, k8sNodes, nodes, placement] = await Promise.all([
      k8sFetch<ListResponse<{ metadata: { name: string }; spec?: { rack?: string; role?: string } }>>(
        `/apis/${GROUP}/${VERSION}/namespaces/default/framenodes`,
      ),
      k8sFetch<ListResponse<{ metadata: { name: string; labels?: Record<string, string> } }>>(
        '/api/v1/nodes',
      ),
      this.nodes(),
      this.placement(),
    ])
    const fnRackOf = new Map((fnRes.items ?? []).map((f) => [f.metadata.name, f.spec?.rack]))
    // Real physical topology from node labels, falling back to the FrameNode tag.
    const labelsOf = new Map((k8sNodes.items ?? []).map((n) => [n.metadata.name, n.metadata.labels ?? {}]))
    const rackOf = (name: string) =>
      labelsOf.get(name)?.['topology.frame.io/rack'] ?? fnRackOf.get(name) ?? 'unracked'
    const physicalOf = (name: string): Rack['physical'] => {
      const l = labelsOf.get(name)
      if (!l?.['topology.frame.io/hypervisor']) return undefined
      return {
        hypervisor: l['topology.frame.io/hypervisor'],
        pcpu: Number(l['topology.frame.io/host-pcpu'] ?? 0),
        pmemGiB: Number(l['topology.frame.io/host-pmem-gib'] ?? 0),
      }
    }
    const podsOf = new Map(placement.map((p) => [p.node, p.total]))

    const byRack = new Map<string, Rack>()
    for (const n of nodes) {
      const rack = rackOf(n.name)
      const entry = byRack.get(rack) ?? {
        name: rack,
        nodes: [],
        readyNodes: 0,
        totalCpu: 0,
        totalMem: 0,
        totalPods: 0,
        physical: physicalOf(n.name),
      }
      entry.nodes.push({
        name: n.name,
        ready: n.ready,
        role: n.roles.join(','),
        cpuCores: n.cpuCores,
        memGiB: n.memGiB,
        pods: podsOf.get(n.name) ?? 0,
      })
      if (n.ready) entry.readyNodes += 1
      entry.totalCpu += n.cpuCores
      entry.totalMem += n.memGiB
      entry.totalPods += podsOf.get(n.name) ?? 0
      byRack.set(rack, entry)
    }
    return Array.from(byRack.values()).sort((a, b) => a.name.localeCompare(b.name))
  }

  /** Live GPU telemetry from DCGM-exporter (NVIDIA GPU operator). */
  async gpus(): Promise<GpuInfo[]> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      '/api/v1/namespaces/gpu-operator/pods?labelSelector=app%3Dnvidia-dcgm-exporter',
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) throw new FrameAPIError(404, 'DCGM exporter not deployed')
    const res = await fetch(
      `/api/v1/namespaces/gpu-operator/pods/${name}:9400/proxy/metrics`,
    )
    if (!res.ok) throw new FrameAPIError(res.status, 'cannot read DCGM metrics')
    const text = await res.text()

    // Group every DCGM_FI_DEV_* sample by its gpu="N" label.
    const gpus = new Map<string, Partial<GpuInfo> & { fbFree?: number }>()
    const re = /^(DCGM_FI_DEV_\w+)\{([^}]*)\}\s+([0-9.e+-]+)/gm
    let m: RegExpExecArray | null
    const lbl = (labels: string, k: string) => labels.match(new RegExp(`${k}="([^"]*)"`))?.[1] ?? ''
    while ((m = re.exec(text))) {
      const [, metric, labels, valStr] = m
      const idx = lbl(labels, 'gpu')
      const v = Number(valStr)
      const g = gpus.get(idx) ?? { index: idx }
      g.model = lbl(labels, 'modelName')
      g.node = lbl(labels, 'Hostname')
      if (metric === 'DCGM_FI_DEV_GPU_UTIL') g.utilPct = v
      else if (metric === 'DCGM_FI_DEV_FB_USED') g.memUsedMB = v
      else if (metric === 'DCGM_FI_DEV_FB_FREE') g.fbFree = v
      else if (metric === 'DCGM_FI_DEV_GPU_TEMP') g.tempC = v
      else if (metric === 'DCGM_FI_DEV_POWER_USAGE') g.powerW = v
      else if (metric === 'DCGM_FI_DEV_ENC_UTIL') g.encUtil = v
      else if (metric === 'DCGM_FI_DEV_DEC_UTIL') g.decUtil = v
      gpus.set(idx, g)
    }
    return Array.from(gpus.values())
      .map((g) => ({
        index: g.index ?? '0',
        model: g.model ?? 'GPU',
        node: g.node ?? '',
        utilPct: g.utilPct ?? 0,
        memUsedMB: g.memUsedMB ?? 0,
        memTotalMB: (g.memUsedMB ?? 0) + (g.fbFree ?? 0),
        tempC: g.tempC ?? 0,
        powerW: g.powerW ?? 0,
        encUtil: g.encUtil ?? 0,
        decUtil: g.decUtil ?? 0,
      }))
      .sort((a, b) => a.index.localeCompare(b.index))
  }

  /**
   * Live inference telemetry from the on-GPU llama.cpp server (Prometheus
   * /metrics + /props). Real KV-cache depth, throughput and request queue.
   */
  async inference(): Promise<InferenceStatus | null> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string }; spec: { nodeName?: string } }>>(
      '/api/v1/namespaces/inference/pods?labelSelector=app%3Dllamacpp',
    )
    const pod = pods.items?.find((p) => p.metadata.name)
    if (!pod) return null
    const name = pod.metadata.name
    const base = `/api/v1/namespaces/inference/pods/${name}:8080/proxy`

    const mRes = await fetch(`${base}/metrics`)
    if (!mRes.ok) throw new FrameAPIError(mRes.status, 'cannot read inference metrics')
    const text = await mRes.text()
    const num = (k: string) => Number(text.match(new RegExp(`^${k}\\s+([0-9.e+-]+)`, 'm'))?.[1] ?? 0)

    // /props → context window, slot count, model. Best-effort (never fatal).
    let nCtx = 0
    let slots = 0
    let model = ''
    try {
      const p = await (await fetch(`${base}/props`)).json()
      nCtx = p.default_generation_settings?.n_ctx ?? p.n_ctx ?? 0
      slots = p.total_slots ?? 0
      model = p.model_alias ?? ''
    } catch {
      /* props optional */
    }

    const kvTokens = num('llamacpp:n_tokens_max')
    return {
      model: model || 'llama.cpp',
      node: pod.spec.nodeName ?? '',
      nCtx,
      slots,
      kvTokens,
      kvUsePct: nCtx ? (kvTokens / nCtx) * 100 : 0,
      requestsProcessing: num('llamacpp:requests_processing'),
      requestsDeferred: num('llamacpp:requests_deferred'),
      promptTokensPerSec: num('llamacpp:prompt_tokens_seconds'),
      predictedTokensPerSec: num('llamacpp:predicted_tokens_seconds'),
      promptTokensTotal: num('llamacpp:prompt_tokens_total'),
      tokensPredictedTotal: num('llamacpp:tokens_predicted_total'),
      busySlotsPerDecode: num('llamacpp:n_busy_slots_per_decode'),
    }
  }

  /**
   * Runtime security detections from Falco (via Falcosidekick Prometheus
   * metrics). Each series is one (rule, priority, workload) with a firing count.
   */
  async security(): Promise<SecurityStatus | null> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      '/api/v1/namespaces/falco/pods?labelSelector=app.kubernetes.io%2Fname%3Dfalcosidekick',
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) return null
    const res = await fetch(
      `/api/v1/namespaces/falco/pods/${name}:2801/proxy/metrics`,
    )
    if (!res.ok) throw new FrameAPIError(res.status, 'cannot read Falco metrics')
    const text = await res.text()

    const lbl = (labels: string, k: string) =>
      labels.match(new RegExp(`${k}="([^"]*)"`))?.[1] ?? ''
    const re = /^falcosecurity_falcosidekick_falco_events_total\{([^}]*)\}\s+([0-9.e+-]+)/gm
    const events: SecurityEvent[] = []
    const byPriority: Record<string, number> = {}
    let m: RegExpExecArray | null
    let total = 0
    while ((m = re.exec(text))) {
      const [, labels, valStr] = m
      const count = Number(valStr)
      const priority = lbl(labels, 'priority_raw') || lbl(labels, 'priority')
      events.push({
        rule: lbl(labels, 'rule'),
        priority,
        priorityRank: Number(lbl(labels, 'priority') || 7),
        node: lbl(labels, 'hostname'),
        namespace: lbl(labels, 'k8s_ns_name'),
        pod: lbl(labels, 'k8s_pod_name'),
        source: lbl(labels, 'source'),
        count,
      })
      byPriority[priority] = (byPriority[priority] ?? 0) + count
      total += count
    }
    events.sort((a, b) => a.priorityRank - b.priorityRank || b.count - a.count)
    return { events, total, byPriority }
  }

  /**
   * Security posture from trivy-operator: image CVEs (VulnerabilityReport) and
   * workload misconfigs (ConfigAuditReport), aggregated cluster-wide.
   */
  async posture(): Promise<PostureStatus | null> {
    type Report = {
      report: {
        summary?: { criticalCount?: number; highCount?: number; mediumCount?: number; lowCount?: number }
        artifact?: { repository?: string; tag?: string }
        checks?: Array<{ checkID?: string; title?: string; severity?: string; success?: boolean }>
      }
    }
    const base = '/apis/aquasecurity.github.io/v1alpha1'
    const [vulnRes, confRes] = await Promise.all([
      k8sFetch<ListResponse<Report>>(`${base}/vulnerabilityreports`).catch(() => null),
      k8sFetch<ListResponse<Report>>(`${base}/configauditreports`).catch(() => null),
    ])
    if (!vulnRes && !confRes) return null

    const sum = (): PostureSummary => ({ critical: 0, high: 0, medium: 0, low: 0 })
    const add = (acc: PostureSummary, s?: Report['report']['summary']) => {
      acc.critical += s?.criticalCount ?? 0
      acc.high += s?.highCount ?? 0
      acc.medium += s?.mediumCount ?? 0
      acc.low += s?.lowCount ?? 0
    }

    // ── Vulnerabilities: cluster totals + worst images ──
    const vulns = sum()
    const perImage = new Map<string, VulnerableImage>()
    for (const r of vulnRes?.items ?? []) {
      add(vulns, r.report.summary)
      const a = r.report.artifact
      const image = a?.repository ? `${a.repository}${a.tag ? `:${a.tag}` : ''}` : ''
      if (!image) continue
      const e = perImage.get(image) ?? { image, critical: 0, high: 0 }
      e.critical += r.report.summary?.criticalCount ?? 0
      e.high += r.report.summary?.highCount ?? 0
      perImage.set(image, e)
    }
    const topImages = Array.from(perImage.values())
      .filter((i) => i.critical + i.high > 0)
      .sort((a, b) => b.critical - a.critical || b.high - a.high)
      .slice(0, 8)

    // ── Misconfigs: cluster totals + most common failed checks ──
    const misconfigs = sum()
    const perCheck = new Map<string, MisconfigCheck>()
    const rank: Record<string, number> = { CRITICAL: 0, HIGH: 1, MEDIUM: 2, LOW: 3 }
    for (const r of confRes?.items ?? []) {
      add(misconfigs, r.report.summary)
      for (const c of r.report.checks ?? []) {
        if (c.success || !c.checkID) continue
        const e = perCheck.get(c.checkID) ?? {
          id: c.checkID,
          title: c.title ?? c.checkID,
          severity: c.severity ?? 'UNKNOWN',
          count: 0,
        }
        e.count += 1
        perCheck.set(c.checkID, e)
      }
    }
    const topChecks = Array.from(perCheck.values())
      .sort((a, b) => (rank[a.severity] ?? 9) - (rank[b.severity] ?? 9) || b.count - a.count)
      .slice(0, 8)

    return {
      vulns: { ...vulns, images: vulnRes?.items?.length ?? 0 },
      topImages,
      misconfigs: { ...misconfigs, resources: confRes?.items?.length ?? 0 },
      topChecks,
    }
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
