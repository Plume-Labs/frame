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
 * // Install an OS on an inventoried machine (creates a FrameInstall CR)
 * await frame.installs.create('w3', installSpec)
 * ```
 */

import { config, CONFIG_NAMESPACE, type Integration } from './frame-config'
import { currentSession } from './auth'
import {
  buildWorkloadTree,
  type EditableKind,
  type NamespaceNode,
  type OwnerRef,
  type WorkloadController,
  type WorkloadKind,
  type WorkloadPod,
} from './workloads'
import { podLogPath, type PodLogQuery } from './pod-logs'
import { changedFieldPaths, editActionLabel, MAX_ACTION_LENGTH } from './manifest-diff'
import { toMachine, powerActionLabel, type Machine, type MachineCR } from './machines'
import {
  installObjectName, randomNameSuffix, toInstall,
  type Install, type InstallCR, type InstallCreateSpec,
} from './installs'
import type { ClaimCounts as StorageClaimCounts, StorageCapacity } from './storage'

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
  /**
   * Empty when the node has not been classified. FrameNode.spec.serviceClass
   * has no CRD default (unlike FrameJob's LOW and FrameService's MEDIUM), so
   * absence is a real, reachable state and not something to paper over.
   */
  serviceClass: ServiceClass | ''
  zone: string
  rackId: string
  cpu: number
  memory: number
  storage: number
  gpuCount: number
  gpuModel: string
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

/** The substrate a container-substrate FrameJob runs on. Ignored for a
 * pipeline job — Argo is always the substrate for a pipeline regardless of
 * `type`. Server-side default is `background` when omitted. */
export type WorkloadType = 'realtime' | 'batch' | 'background'

/** Mirrors k8s `corev1.EnvVar` — passed straight through to the API server,
 * which is why `valueFrom` is left loose rather than modelled field by field. */
export interface ContainerEnvVar {
  name: string
  value?: string
  valueFrom?: Record<string, unknown>
}

/** Mirrors k8s `corev1.EnvFromSource` — exactly one of `configMapRef`/`secretRef`,
 * same as upstream; Frame does not narrow it further. */
export interface ContainerEnvFromSource {
  configMapRef?: { name: string; optional?: boolean }
  secretRef?: { name: string; optional?: boolean }
  prefix?: string
}

/**
 * The container substrate for a FrameJob — the alternative to naming an Argo
 * pipeline. Deliberately narrow (image, command, args, env, resources,
 * envFrom) rather than a full pod spec; see `ContainerSpec` in
 * `api/frame/v1beta1/framejob_types.go` for why.
 */
export interface ContainerSpec {
  image: string
  command?: string[]
  args?: string[]
  env?: ContainerEnvVar[]
  /** Populates env vars from a Secret or ConfigMap already in the FrameJob's
   * own namespace — see the CRD field doc for the blast-radius note. */
  envFrom?: ContainerEnvFromSource[]
  resources?: {
    limits?: Record<string, string>
    requests?: Record<string, string>
  }
}

interface JobSpecCommon {
  name: string
  /** Omit to take the CRD default (LOW) rather than pinning a tier here. */
  serviceClass?: ServiceClass
  /** Omit to take the CRD default (medium). */
  priority?: Priority
  /**
   * Where the FrameJob — and therefore its Workflow or container Job — is
   * created. It used to name `spec.namespace`, a separate target the CR
   * itself did not live in; v1beta1 removed that field (F5), so this now
   * steers `metadata.namespace`.
   */
  namespace?: string
  gpuCount?: number
  /** Only meaningful alongside `container` — see `WorkloadType`. */
  type?: WorkloadType
}

/**
 * Exactly one of `pipeline`/`container`, mirroring the CEL rule the CRD
 * enforces server-side (`has(self.pipeline) != has(self.container)`). The
 * `?: never` on the field not chosen makes supplying both a compile error
 * instead of a runtime rejection.
 */
export type JobSpec =
  | (JobSpecCommon & { pipeline: string; container?: never })
  | (JobSpecCommon & { container: ContainerSpec; pipeline?: never })

export interface SchedulingPolicy {
  name: string
  scheduler: SchedulerType
  queue: string
  queueWeight: number
  priority: number
  preemption: boolean
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
  namespaces: number
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
  unschedulable: boolean
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

/** Velero disaster-recovery backup status (backups + schedule + storage). */
export interface BackupRun {
  name: string
  phase: string
  items: number
  completed: string
  errors: number
  namespaces: string[]
}
export interface BackupStatus {
  storageReady: boolean
  schedule: string | null
  lastSuccess: string | null
  recent: BackupRun[]
}

/** An active alert from Alertmanager. */
export interface ActiveAlert {
  name: string
  severity: string
  state: string
  summary: string
  namespace: string
  startsAt: string
  /** Raw label set, needed to build precise matchers for a silence. */
  labels: Record<string, string>
}
/** A live Alertmanager silence — what a "Silence" action produced, so it can be undone. */
export interface AlertSilence {
  id: string
  /** `active` (suppressing now) or `pending` (starts later). */
  state: string
  /** Matchers rendered for display, e.g. `alertname=Foo, namespace=bar`. */
  matchers: string
  startsAt: string
  endsAt: string
  createdBy: string
  comment: string
}
export interface AlertsStatus {
  alerts: ActiveAlert[]
  bySeverity: Record<string, number>
}

/** One metric's recent history, from a Prometheus range query. */
export interface MetricSeries {
  metric: string
  /** Last sample, or 0 when the query returned nothing. */
  current: number
  history: Array<{ t: number; v: number }>
  /** Passed through from the request, so a caller can label the axis. */
  unit?: string
}

/** A capacity metric's recent history + linear projection (from Prometheus). */
export interface CapacityTrend extends MetricSeries {
  projectedFullDays: number | null // null if flat/declining
}

/** One series to fetch: a display name and the PromQL behind it. */
export interface RangeQuery {
  metric: string
  q: string
  unit?: string
}

/**
 * Least-squares slope over a percent series → "days until it reaches 100".
 *
 * Slopes at or below 0.1 %/day give `null` rather than a huge number: at that
 * gradient the projection is noise, and "1200 days to full" reads as a fact
 * when it is really a flat line.
 */
export function projectToFull(series: MetricSeries): CapacityTrend {
  const { history } = series
  const n = history.length
  if (n < 2) return { ...series, projectedFullDays: null }

  const t0 = history[0].t / 1000
  const xs = history.map((p) => p.t / 1000 - t0)
  const ys = history.map((p) => p.v)
  const mx = xs.reduce((a, b) => a + b, 0) / n
  const my = ys.reduce((a, b) => a + b, 0) / n
  let num = 0
  let den = 0
  for (let i = 0; i < n; i++) {
    num += (xs[i] - mx) * (ys[i] - my)
    den += (xs[i] - mx) ** 2
  }
  const slopePerDay = (den > 0 ? num / den : 0) * 86400
  const current = ys[n - 1]
  return {
    ...series,
    current,
    projectedFullDays: slopePerDay > 0.1 ? (100 - current) / slopePerDay : null,
  }
}
export interface ForecastStatus {
  series: CapacityTrend[]
  windowHours: number
}

/** Tetragon eBPF process + network activity, aggregated from its metrics. */
export interface TetragonActivity {
  exec: number
  network: number // PROCESS_KPROBE (tcp_connect tracing policy)
  exit: number
  topNetwork: Array<{ workload: string; namespace: string; count: number }>
  topExec: Array<{ binary: string; workload: string; count: number }>
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

/** Live TEI (text-embeddings-inference) status — CPU-only in this deployment. */
export interface TeiStatus {
  model: string
  dtype: string
  node: string
  requestCount: number
  successCount: number
  queueSize: number
  avgInferenceMs: number
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
  /**
   * `status.ceph.details` off the CephCluster CR, carried through verbatim
   * — the same field `cmd/main.go`'s `readCephHealth` reads for
   * FrameStorage's `Healthy` condition. `cephWarningReasons` (`storage.ts`)
   * is written directly against this shape (R18): an earlier version
   * remapped it into a generic `checks[code].summary.message` shape here,
   * and shipped with no test covering that remap, so the pure function was
   * exercised only against a shape the cluster never actually sends.
   * Passing the real shape straight through is the fix.
   */
  details: Record<string, { message?: string; severity?: string }>
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

/**
 * One write the UI made through the proxy — a FrameTask, projected for the
 * Tasks screen. `target`/`ref` are rendered as `resource/name` (or
 * `namespace/resource/name`), matching how FrameTaskSpec.ObjectRef names
 * things: a plural resource off the request path, not a Kind.
 */
export interface TaskRecord {
  name: string
  user: string
  verb: string
  action: string
  target: string
  phase: 'Running' | 'Succeeded' | 'Failed'
  httpCode?: number
  startedAt?: string
  finishedAt?: string
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
/**
 * The served Frame API version every path below is built from.
 *
 * v1alpha1 still works — the conversion webhook is proven end to end — but it
 * is no longer the storage version, so every request through it pays a
 * conversion round trip and comes back with a `Warning:
 * frame.plume-labs.io/v1alpha1 ... is deprecated` header. The UI is the
 * heaviest client of the Frame API, so leaving it on the spoke would make it
 * the primary consumer of a code path nothing else exercises.
 */
const VERSION = 'v1beta1'

function frameNs(override?: string): string {
  return override
    ?? (window as unknown as Record<string, string>).__FRAME_NAMESPACE__
    ?? config().frameNamespace
}

/**
 * Pod-list URL for an integration's selector. An empty namespace means
 * cluster-wide, for components whose pods are not pinned to one namespace.
 */
function integrationPods(i: Integration): string {
  const scope = i.namespace ? `/namespaces/${i.namespace}` : ''
  return `/api/v1${scope}/pods?labelSelector=${encodeURIComponent(i.selector)}`
}

/** Proxy URL to `path` on one of an integration's pods. */
function integrationProxy(i: Integration, pod: string, path: string): string {
  return `/api/v1/namespaces/${i.namespace}/pods/${pod}:${i.port}/proxy${path}`
}

/**
 * Escape a value interpolated into a metrics-matching RegExp.
 *
 * Interface names and mount paths come from the config now, so they are
 * arbitrary user input: an unescaped `+` or `(` builds a regex that throws
 * instead of a match that fails, taking the whole screen down.
 */
function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function bearerToken(): string | undefined {
  return (window as unknown as Record<string, string>).__FRAME_TOKEN__
}

function apiBase(plural: string, ns?: string): string {
  return `/apis/${GROUP}/${VERSION}/namespaces/${frameNs(ns)}/${plural}`
}

/**
 * The list endpoint for a Frame CRD, for callers that want to watch it.
 *
 * Exported so a view can subscribe to the same collection the SDK reads
 * without hand-assembling the path and drifting from `frameNs`.
 */
export function frameListPath(plural: string, ns?: string): string {
  return apiBase(plural, ns)
}

/**
 * The list endpoint for a cluster-scoped Frame CRD — FrameStorage is the
 * only one this SDK reads (`+kubebuilder:resource:scope=Cluster` in
 * `api/frame/v1beta1/framestorage_types.go`), so unlike every other Frame
 * kind here there is no namespace segment to build and no `frameNs` to
 * resolve through.
 */
export function frameClusterListPath(plural: string): string {
  return `/apis/${GROUP}/${VERSION}/${plural}`
}

/**
 * Where the FrameTask trail is read from.
 *
 * Not `frameListPath('frametasks')`: that resolves through `frameNs()` to
 * `config().frameNamespace`, and the proxy writes to `config().taskNamespace`.
 * One exported function rather than the literal repeated in the SDK and in
 * `TasksView`, so the screen and its watch can never drift apart.
 */
export function taskListPath(): string {
  return frameListPath('frametasks', config().taskNamespace)
}

/** The list endpoint for a core Kubernetes collection, e.g. `nodes`, `events`. */
export function coreListPath(plural: string, ns?: string): string {
  return ns ? `/api/v1/namespaces/${ns}/${plural}` : `/api/v1/${plural}`
}

/**
 * The list endpoint for a CRD outside Frame's own group — Argo Workflows,
 * Volcano Queues, Rook-Ceph's CRs, Velero backups, trivy-operator reports —
 * for callers that want to watch it. `frameListPath` covers `frame.plume-labs.io`
 * itself; this is the same shape for everything else the SDK reads that isn't
 * a Frame CRD or a core collection.
 */
export function crdListPath(group: string, version: string, plural: string, ns?: string): string {
  return ns ? `/apis/${group}/${version}/namespaces/${ns}/${plural}` : `/apis/${group}/${version}/${plural}`
}

/** The list endpoint for FrameMachine CRs, for callers that want to watch it. */
export function machinesPath(ns?: string): string {
  return frameListPath('framemachines', ns)
}

/** The list endpoint for FrameInstall CRs, for callers that want to watch it. */
export function installsPath(ns?: string): string {
  return frameListPath('frameinstalls', ns)
}

/** The list endpoint for FrameStorage CRs — cluster-scoped, see `frameClusterListPath`. */
export function storageEntriesPath(): string {
  return frameClusterListPath('framestorages')
}

/** The list endpoint for FrameDiskClaim CRs, for callers that want to watch it. */
export function diskClaimsPath(ns?: string): string {
  return frameListPath('framediskclaims', ns)
}

/**
 * The power write is an action stamped with the moment it was requested, not a
 * desired state. A body carrying only the action would make a second press a
 * no-op and a restart inexpressible; the controller acts only when this
 * timestamp is newer than status.lastPowerActionAt.
 */
export function powerPatchBody(action: string): {
  spec: { powerRequest: { action: string; requestedAt: string } }
} {
  return { spec: { powerRequest: { action, requestedAt: new Date().toISOString() } } }
}

function toK8sName(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^-+|-+$/g, '').slice(0, 63)
}

/**
 * GET requests currently in flight, keyed by path.
 *
 * Every screen owns its own watch and its own fetcher, and nothing is shared
 * between them. So one Node event makes each mounted component that watches
 * nodes re-read the same list at the same moment: a measured burst reached
 * 321 reads of `/api/v1/nodes` and 308 of the node metrics inside twenty
 * seconds, plus 33 whole-cluster pod lists totalling 13.6 MB. The apiserver
 * answered all of them; the cost lands on the browser, which spends its
 * per-origin connection budget on duplicates and shows values late.
 *
 * Collapsing them is safe precisely because they are identical concurrent
 * GETs: every caller would have received the same body, so handing them one
 * response changes no result. Deliberately no expiry — the entry lives only
 * as long as the request, and the next call after it settles goes to the
 * network. A TTL would be a cache, with staleness to reason about after a
 * write; this is only de-duplication.
 */
const inFlightGets = new Map<string, Promise<unknown>>()

interface K8sFetchOptions {
  method?: string
  body?: unknown
  contentType?: string
  /**
   * A human-readable label for what this write is, sent as `X-Frame-Action`
   * and stored on the FrameTask the proxy records ("cordon node w2").
   *
   * Without it the Tasks screen falls back to `${verb} ${target}` — "patch
   * nodes/w2" — which is the request, not the action, and is the same string
   * for a cordon and an uncordon. The header existed and was read by
   * `internal/uiproxy/recorder.go` from the first commit; nothing ever sent
   * it (whole-branch review, I2).
   *
   * Only meaningful on a mutating verb: reads are not recorded.
   */
  action?: string
  /**
   * A body sent verbatim, instead of `JSON.stringify(body)`.
   *
   * The manifest editor's body is the user's own YAML text, and there is no
   * YAML library in this repository to turn it into an object first — the
   * apiserver parses `application/yaml` itself. Re-serialising here would mean
   * writing something other than what the person read and edited.
   */
  rawBody?: string
  /**
   * An `Accept` header. Only the manifest editor sets it
   * (`application/yaml`); everything else wants the default JSON.
   */
  accept?: string
}

async function k8sFetch<T>(
  path: string,
  opts: K8sFetchOptions = {},
): Promise<T> {
  const method = opts.method ?? 'GET'
  // Only plain GETs dedupe. A write must always reach the apiserver, and two
  // writes to one path are not interchangeable the way two reads are.
  if (method === 'GET' && opts.body === undefined && opts.rawBody === undefined) {
    const pending = inFlightGets.get(path)
    if (pending) return pending as Promise<T>
    const p = k8sFetchUncached<T>(path, opts).finally(() => {
      inFlightGets.delete(path)
    })
    inFlightGets.set(path, p)
    return p
  }
  return k8sFetchUncached<T>(path, opts)
}

async function k8sFetchUncached<T>(
  path: string,
  opts: K8sFetchOptions = {},
): Promise<T> {
  const res = await sendToApiserver(path, opts)
  if (res.status !== 401) return parseApiserverResponse<T>(res)

  // 401 here means the proxy in front of the apiserver rejected the bearer
  // token itself — missing or expired — not a Kubernetes RBAC decision
  // (that comes back as 403). The request never reached the apiserver, so
  // replaying it is safe even for a write.
  //
  // The retry forces a brand-new token via `currentSession()` rather than
  // `ensureToken()`'s "still has time left, keep it" fast path. A 401 can
  // happen for a reason that has nothing to do with local expiry — a
  // revoked account, a role change, authd reissuing a shorter-lived token,
  // clock skew — and in every one of those cases the cached token is
  // exactly the one that was just rejected; replaying it via `ensureToken()`
  // would only earn a second 401 for free.
  //
  // If the refresh itself fails — the browser's own session cookie is also
  // gone, or authd is unreachable — that must not surface as this call's
  // error: the caller asked about their apiserver request, not about the
  // token, so `refreshTokenForRetry` swallows it and this falls through to
  // the original 401. Refresh once, retry once, never loop: there is no
  // path back to the top of this function.
  const token = await refreshTokenForRetry()
  if (!token) return parseApiserverResponse<T>(res)
  return parseApiserverResponse<T>(await sendToApiserver(path, opts))
}

/** Force a fresh token before retrying a 401 — see the comment above. */
async function refreshTokenForRetry(): Promise<string | undefined> {
  try {
    return (await currentSession())?.token
  } catch {
    return undefined
  }
}

async function sendToApiserver(
  path: string,
  opts: K8sFetchOptions,
): Promise<Response> {
  const headers: Record<string, string> = {}
  const tok = bearerToken()
  if (tok) headers['Authorization'] = `Bearer ${tok}`
  if (opts.accept) headers['Accept'] = opts.accept
  if (opts.body !== undefined || opts.rawBody !== undefined) {
    headers['Content-Type'] = opts.contentType ?? 'application/json'
  }
  // Read by internal/uiproxy/recorder.go and stored on the FrameTask. Header
  // values must be latin-1, and an action label is assembled from names the
  // user may have chosen, so anything outside that range is stripped rather
  // than allowed to throw inside the request.
  if (opts.action) headers['X-Frame-Action'] = opts.action.replace(/[^\x20-\x7e]/g, '')
  return globalThis.fetch(path, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.rawBody ?? (opts.body !== undefined ? JSON.stringify(opts.body) : undefined),
  })
}

/**
 * `fetch` for the integration proxies, carrying the caller's bearer token.
 *
 * Every URL these call sites build is an apiserver path —
 * `/api/v1/namespaces/{ns}/pods/{pod}:{port}/proxy/...`, from
 * `integrationProxy()` — so it travels through nginx to the uiproxy, which
 * rejects anything without a token before RBAC is consulted. They were bare
 * `proxyFetch()` calls, so Prometheus, Alluxio, node-exporter, DCGM, llama.cpp,
 * TEI, Falco, Tetragon and Alertmanager all answered 401 for everyone. The
 * reason nothing caught it is that a bare `fetch` is what these looked like
 * before there was any authentication in the path at all.
 *
 * `k8sFetch` is the one for typed Kubernetes objects; this is for the
 * arbitrary bodies (Prometheus JSON, exporter text) behind the proxy.
 */
function proxyFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const tok = bearerToken()
  const headers: Record<string, string> = { ...(init.headers as Record<string, string> | undefined) }
  if (tok) headers['Authorization'] = `Bearer ${tok}`
  return globalThis.fetch(url, { ...init, headers })
}

/**
 * `fetch` for an apiserver path whose response is not JSON — a YAML manifest,
 * or a log stream that must stay a stream.
 *
 * `k8sFetch` parses; these two callers must not be parsed. It still carries
 * the bearer token and still retries once on a 401 with a fresh one, which is
 * the whole reason it is not a bare `fetch`: a tab left open past a token's
 * life would otherwise show an empty log pane rather than reconnecting.
 */
async function rawFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const res = await proxyFetch(path, init)
  if (res.status !== 401) return res
  const token = await refreshTokenForRetry()
  if (!token) return res
  return proxyFetch(path, init)
}

/**
 * Turn an apiserver response into its object, or into a {@link FrameAPIError}.
 *
 * The body is not always JSON, and assuming it is cost the console its ability
 * to notice a lapsed session. The uiproxy used to answer 401 with a
 * `text/plain` "unauthorized", so `res.json()` threw
 * `SyntaxError: Unexpected token 'u'` and the caller never saw a 401 at all —
 * a tab left open past the 12h cookie filled with parse errors instead of
 * returning to the login screen (whole-branch review, I1). The proxy now
 * emits a `metav1.Status`, but it is not the only hop that can answer: an
 * ingress 502 and an nginx 504 are both HTML, and neither is a bug in this
 * function's caller.
 *
 * So: parse when it parses, fall back to the text when it does not, and
 * always throw a status the caller can act on.
 */
async function parseApiserverResponse<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: (T & { message?: string }) | undefined
  try {
    data = JSON.parse(text) as T & { message?: string }
  } catch {
    // Not JSON. On a success that is a real surprise and worth reporting as
    // one; on a failure the text *is* the message.
    if (res.ok) {
      throw new FrameAPIError(res.status, `expected JSON from ${res.url || 'the apiserver'}, got: ${text.slice(0, 200)}`)
    }
  }
  if (!res.ok) {
    throw new FrameAPIError(res.status, data?.message ?? text.trim() ?? res.statusText)
  }
  return data as T
}

// ── CR type shims ─────────────────────────────────────────────────────────────

interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  reason?: string
  message?: string
  observedGeneration?: number
  lastTransitionTime?: string
}

/**
 * The Ready condition, which is where every Frame kind reports health.
 *
 * v1beta1 removed `status.phase` from every kind: a single enum forces the API
 * to pick one dimension of health out of several and cannot express
 * "provisioned but degraded". For FrameJob and FrameNode the Ready condition's
 * `reason` carries exactly the string the old phase field held, so the mappers
 * below read it from there.
 */
function readyCondition(conditions?: Condition[]): Condition | undefined {
  return conditions?.find((c) => c.type === 'Ready')
}

interface FrameJobCR {
  metadata: { name: string; namespace?: string; creationTimestamp?: string }
  spec: {
    pipeline: string; serviceClass?: string; priority?: string
    gpuCount?: number; suspended?: boolean
  }
  status?: {
    conditions?: Condition[]
    observedGeneration?: number
    startTime?: string; completionTime?: string
    argoWorkflowName?: string; message?: string
  }
}

interface FrameNodeCR {
  metadata: { name: string; namespace?: string }
  spec: { ip: string; serviceClass?: string; zone?: string; rack?: string; rdmaInterface?: string; hostname?: string }
  status?: {
    conditions?: Condition[]
    observedGeneration?: number
    capacity?: Record<string, string>; allocatable?: Record<string, string>
    discoveredHostname?: string; discoveredTalosVersion?: string
    discoveredDisks?: Array<{ name: string; size: string; type: string }>
    discoveredNICs?: Array<{ name: string; mac: string; speed: string }>
  }
}

interface SchedulingPolicyCR {
  metadata: { name: string; namespace?: string }
  spec: { scheduler?: string; queueName?: string; queueWeight?: number; priorityValue?: number; preemption?: boolean }
}

interface FrameResourceQuotaCR {
  metadata: { name: string; namespace?: string }
  spec: { serviceClass: string; maxGPUs?: number; maxCPU?: string; maxMemory?: string }
  status?: { used?: Record<string, string>; namespaces?: number }
}

interface ListResponse<T> { items: T[] }

// ── CR → domain mappers ───────────────────────────────────────────────────────

/**
 * A FrameJob's lifecycle, read off the Ready condition's reason.
 *
 * The controller writes exactly one condition and its reason is always one of
 * Submitted, Running, Suspended, Completed, Failed, so this is a rename of the
 * removed `status.phase`, not an inference.
 *
 * The interesting case is the one that is *not* a rename. Both FrameJobs
 * stored on the cluster predate that invariant: they sit at generation 1 with
 * a write-once `Submitted/WorkflowCreated` condition and no Ready condition at
 * all. Reading `Ready.reason` on them yields undefined, so they land here on
 * `queued` — the same place the server-side v1alpha1 projection puts them,
 * which answers `Submitted` for a legacy Submitted condition and `Pending` for
 * a conditionless object, both of which this narrower domain type spells
 * `queued`.
 *
 * `status.completionTime` is deliberately not consulted, even though those two
 * objects carry one. The controller sets it on Failed as well as on Completed,
 * so it cannot tell the two apart, and guessing `completed` would make an old
 * failure render as healthy — the exact bug the condition invariant exists to
 * prevent. A re-reconcile is what restores their real outcome.
 */
function mapJobPhase(cr: FrameJobCR): JobStatus {
  switch (readyCondition(cr.status?.conditions)?.reason) {
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
    status:       mapJobPhase(cr),
    // LOW, not MEDIUM: the CRD defaults serviceClass to LOW, so a served
    // object always carries one and this fallback is only reachable for a
    // hand-built object. Answering MEDIUM here was half of the disagreement
    // between what kubectl showed and what the UI showed (F4).
    serviceClass: (cr.spec.serviceClass ?? 'LOW') as ServiceClass,
    priority:     (cr.spec.priority ?? 'medium') as Priority,
    // The FrameJob's own namespace is where its Workflow runs. spec.namespace
    // is gone at v1beta1 (F5) — it let a caller direct workflow creation into
    // any namespace the operator could reach.
    namespace:    cr.metadata.namespace ?? frameNs(),
    gpuCount:     cr.spec.gpuCount ?? 0,
    createdAt:    cr.metadata.creationTimestamp ?? new Date().toISOString(),
    startedAt:    cr.status?.startTime,
    completedAt:  cr.status?.completionTime,
  }
}

/**
 * A FrameNode's health, read off the Ready condition's reason.
 *
 * `Discovering` is gone from the switch. It was in v1alpha1's enum and no
 * controller ever wrote it; keeping a case for a value that cannot occur
 * invites someone to "fix" the controller into producing it.
 */
function mapNodePhase(cr: FrameNodeCR): NodeStatus {
  switch (readyCondition(cr.status?.conditions)?.reason) {
    case 'Online':       return 'online'
    case 'Degraded':     return 'degraded'
    case 'Provisioning':
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
    status:       mapNodePhase(cr),
    // No fallback, unlike FrameJob and FrameService. FrameNode.spec
    // .serviceClass is deliberately undefaulted at v1beta1 — a node is
    // discovered before anyone classifies its hardware — so inventing LOW
    // here would show a tier kubectl does not, which is the disagreement the
    // freeze removed, pointed the other way.
    serviceClass: (cr.spec.serviceClass ?? '') as ServiceClass | '',
    zone:         cr.spec.zone ?? '',
    rackId:       cr.spec.rack ?? '',
    cpu:          quantityToNum(alloc['cpu']),
    memory:       quantityToNum(alloc['memory']),
    storage:      0,
    // FrameNodeSpec has no gpuCount and never did: the number shown here was
    // structurally always 0. status.allocatable is what the controller
    // actually syncs from the corev1.Node, so read the GPU count from there.
    gpuCount:     quantityToNum(alloc['nvidia.com/gpu']),
    gpuModel:     cr.spec.rdmaInterface ? 'RDMA' : 'Unknown',
  }
}

function crToPolicy(cr: SchedulingPolicyCR): SchedulingPolicy {
  return {
    name:        cr.metadata.name,
    scheduler:   (cr.spec.scheduler ?? 'default') as SchedulerType,
    queue:       cr.spec.queueName ?? '',
    queueWeight: cr.spec.queueWeight ?? 0,
    priority:    cr.spec.priorityValue ?? 0,
    preemption:  cr.spec.preemption ?? false,
    // maxGPUs/maxCPUs were hardcoded to 0 here. SchedulingPolicySpec has no
    // such fields and never did; resource ceilings are FrameResourceQuota's.
  }
}

function crToQuota(cr: FrameResourceQuotaCR): ResourceQuota {
  // status.used is the sum the controller aggregates across every projected
  // corev1.ResourceQuota, keyed exactly as buildResourceList writes them. A
  // key no namespace reported is absent, not zero — hence the ?? fallbacks
  // rather than a required shape.
  const used = cr.status?.used ?? {}
  return {
    namespace:    cr.metadata.namespace ?? frameNs(),
    serviceClass: (cr.spec.serviceClass ?? 'MEDIUM') as ServiceClass,
    maxCPU:       cr.spec.maxCPU ?? '0',
    maxMemory:    cr.spec.maxMemory ?? '0Gi',
    maxGPUs:      cr.spec.maxGPUs ?? 0,
    usedCPU:      used['limits.cpu'] ?? '0',
    usedMemory:   used['limits.memory'] ?? '0Gi',
    usedGPUs:     quantityToNum(used['requests.nvidia.com/gpu']),
    namespaces:   cr.status?.namespaces ?? 0,
  }
}

interface FrameTaskCR {
  metadata: { name: string; creationTimestamp?: string }
  spec: {
    user: string; verb: string; action?: string
    target: { group?: string; resource: string; namespace?: string; name: string; subresource?: string }
  }
  status?: { phase?: string; httpCode?: number; startedAt?: string; finishedAt?: string }
}

/**
 * `namespace/resource/name[/subresource]`, matching how FrameTaskSpec.ObjectRef
 * names things: plural resources off the request path, not Kinds. The
 * subresource is the difference between "create pods/api-0" — a pod being
 * created — and "create pods/api-0/exec", a shell being opened in one.
 */
function refLabel(r: {
  resource: string
  namespace?: string
  name: string
  subresource?: string
}): string {
  const base = r.namespace ? `${r.namespace}/${r.resource}/${r.name}` : `${r.resource}/${r.name}`
  return r.subresource ? `${base}/${r.subresource}` : base
}

/**
 * An `X-Frame-Action` label capped at `MAX_ACTION_LENGTH`, for writes that
 * have no field-path body to shed the way `editActionLabel` does — a rolling
 * restart, a scale, a pod delete are one short sentence, not "identity:
 * change list", so there is nothing to trade off between and a plain
 * truncation is the right shape.
 *
 * Kubernetes allows a 63-character namespace and, for most kinds, a
 * 253-character object name, so an unbounded label built from `${verb}
 * ${namespace}/${name}` can run past the CRD's cap. Past it the apiserver
 * rejects the FrameTask create outright; `TaskRecorder.Start` logs the error
 * and the user's write still succeeds — so the cost of skipping this is not a
 * truncated record, it is no record at all, precisely for the longest-named
 * resources.
 */
function boundedActionLabel(label: string): string {
  return label.length > MAX_ACTION_LENGTH ? label.slice(0, MAX_ACTION_LENGTH) : label
}

/**
 * A FrameTask's phase, normalized the same way `mapJobPhase`/`mapNodePhase`
 * normalize theirs: a switch over the raw string with a safe default, never
 * an unvalidated cast. `TasksView` does an unconditional `PHASE[t.phase]`
 * lookup, and only a switch-with-default can guarantee that's always a hit —
 * a cast would let any string the controller ever emits (a future phase
 * value, a typo, a hand-built object in a test) reach that lookup unchanged
 * and throw when it comes back undefined.
 *
 * A task with no status, or an explicit `Running`, is one the proxy created
 * and has not closed: in flight, or the proxy died mid-request. Both read as
 * Running, which is also where anything outside the three known phases
 * lands — the same "unrecognized reads as not-yet-done" call `mapJobPhase`
 * makes for a legacy FrameJob with no Ready condition.
 */
function mapTaskPhase(cr: FrameTaskCR): TaskRecord['phase'] {
  switch (cr.status?.phase) {
    case 'Succeeded': return 'Succeeded'
    case 'Failed':    return 'Failed'
    default:          return 'Running'
  }
}

function crToTask(cr: FrameTaskCR): TaskRecord {
  return {
    name: cr.metadata.name,
    user: cr.spec.user,
    verb: cr.spec.verb,
    action: cr.spec.action ?? `${cr.spec.verb} ${refLabel(cr.spec.target)}`,
    target: refLabel(cr.spec.target),
    phase: mapTaskPhase(cr),
    httpCode: cr.status?.httpCode,
    startedAt: cr.status?.startedAt ?? cr.metadata.creationTimestamp,
    finishedAt: cr.status?.finishedAt,
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

/**
 * Every pod in the cluster, served from the apiserver's watch cache.
 *
 * `resourceVersion=0` tells the apiserver "any reasonably recent version will
 * do", which lets it answer from memory instead of a quorum read against etcd.
 * The three callers below — capacity, placement, resilience — all aggregate
 * over the whole list and then re-read it on a watch or a poll, so a response
 * that may lag by a beat costs them nothing and saves roughly a third of the
 * latency on a list this size.
 *
 * Do not reuse this for a read whose result is about to be written back: a
 * stale resourceVersion is exactly the wrong basis for a read-modify-write.
 */
const ALL_PODS_CACHED = '/api/v1/pods?resourceVersion=0'

/**
 * Sum of container resource requests across the cluster, as [cores, GiB],
 * read from kube-state-metrics through Prometheus.
 *
 * The phase join reproduces what summing the pod list does: kube-state-metrics
 * keeps emitting requests for Succeeded and Failed pods, whose objects still
 * exist, and counting those would overstate what is actually asked of the
 * scheduler. Verified equal to the pod-list computation on this cluster —
 * 4.965 cores and 15.244 GiB from both.
 *
 * Returns null, never a partial answer, if Prometheus is absent or either
 * query fails: the caller then does the full read rather than render a number
 * that is quietly too low.
 */
async function requestedFromPrometheus(): Promise<[number, number] | null> {
  const phaseJoin =
    '* on(namespace,pod) group_left() (max by(namespace,pod) (kube_pod_status_phase{phase=~"Running|Pending|Unknown"}) == 1)'
  try {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(config().integrations.prometheus),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) return null
    const base = integrationProxy(config().integrations.prometheus, name, '/api/v1/query')

    const values = await Promise.all(
      [
        `sum(kube_pod_container_resource_requests{resource="cpu"} ${phaseJoin})`,
        `sum(kube_pod_container_resource_requests{resource="memory"} ${phaseJoin}) / 1073741824`,
      ].map(async (q) => {
        const res = await proxyFetch(`${base}?query=${encodeURIComponent(q)}`)
        if (!res.ok) return NaN
        const json = (await res.json()) as {
          data?: { result?: Array<{ value?: [number, string] }> }
        }
        return Number(json?.data?.result?.[0]?.value?.[1])
      }),
    )
    if (!values.every((v) => Number.isFinite(v))) return null
    return [values[0], values[1]]
  } catch {
    return null
  }
}

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

/**
 * Group key: `part-of` first, since that's the label the Kubernetes common-
 * labels convention defines for "this resource belongs to a larger logical
 * application" (e.g. rook-ceph's mon-a/mon-b/osd-0/osd-1 all set part-of:
 * rook-ceph but a per-daemon instance: a/b/0/1 — grouping on instance first
 * would fragment them into one card each). Falls back to instance (a
 * single-release chart like Neura sets only instance, uniformly, across all
 * its components), then namespace.
 */
function appKey(cr: WorkloadCR): string {
  return (
    cr.metadata.labels?.['app.kubernetes.io/part-of'] ??
    cr.metadata.labels?.['app.kubernetes.io/instance'] ??
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
  spec?: { unschedulable?: boolean }
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
          unschedulable: n.spec?.unschedulable ?? false,
        }
      })
      .sort((a, b) => a.name.localeCompare(b.name))
  }

  /** Cordon (true) or uncordon (false) a real Kubernetes node. */
  async cordon(name: string, unschedulable: boolean): Promise<void> {
    await k8sFetch<undefined>(`/api/v1/nodes/${name}`, {
      action: `${unschedulable ? 'cordon' : 'uncordon'} node ${name}`,
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec: { unschedulable } },
    })
  }

  /**
   * Drain a node: cordon it, then evict every evictable pod on it.
   *
   * Skips the same pods `kubectl drain` skips by default — DaemonSet-owned and
   * mirror (static) pods, whose controllers would just recreate them on the
   * same node — plus already-terminal pods, which have nothing to evict.
   *
   * A pod whose PodDisruptionBudget refuses the eviction comes back 429; that
   * is a normal outcome (the budget is doing its job), so it counts as
   * `blocked` and the drain continues instead of aborting. Any other error —
   * an RBAC denial in particular — still throws, so a misconfigured token
   * fails loudly rather than looking like a partially-successful drain.
   */
  async drain(name: string): Promise<{ evicted: number; skipped: number; blocked: number }> {
    await this.cordon(name, true)
    const pods = await k8sFetch<
      ListResponse<{
        metadata: {
          name: string
          namespace: string
          annotations?: Record<string, string>
          ownerReferences?: Array<{ kind: string }>
        }
        status?: { phase?: string }
      }>
    >(`/api/v1/pods?fieldSelector=spec.nodeName%3D${encodeURIComponent(name)}`)

    let evicted = 0
    let skipped = 0
    let blocked = 0
    for (const p of pods.items ?? []) {
      const ownedByDaemonSet = p.metadata.ownerReferences?.some((o) => o.kind === 'DaemonSet')
      const mirror = p.metadata.annotations?.['kubernetes.io/config.mirror'] !== undefined
      const terminal = p.status?.phase === 'Succeeded' || p.status?.phase === 'Failed'
      if (ownedByDaemonSet || mirror || terminal) {
        skipped++
        continue
      }
      try {
        await k8sFetch<undefined>(
          `/api/v1/namespaces/${p.metadata.namespace}/pods/${p.metadata.name}/eviction`,
          {
            action: `drain node ${name}: evict ${p.metadata.namespace}/${p.metadata.name}`,
            method: 'POST',
            body: {
              apiVersion: 'policy/v1',
              kind: 'Eviction',
              metadata: { name: p.metadata.name, namespace: p.metadata.namespace },
            },
          },
        )
        evicted++
      } catch (e) {
        if (e instanceof FrameAPIError && e.statusCode === 429) blocked++
        else throw e
      }
    }
    return { evicted, skipped, blocked }
  }

  /**
   * Live Ceph state from the Rook CephCluster CR, its CephBlockPools, and the
   * running OSD/mon pods. Throws if Rook is not installed (no CephCluster).
   */
  async ceph(): Promise<CephStatus> {
    const cfg = config()
    const ns = cfg.namespaces.ceph
    const [clusters, pools, osdPods, monPods] = await Promise.all([
      // Listed rather than fetched by name: Rook names the CR after its
      // namespace by convention, but nothing enforces that, and a wrong guess
      // would 404 a screen that could just read whichever cluster is there.
      k8sFetch<
        ListResponse<{
          status?: {
            ceph?: {
              health?: string
              capacity?: { bytesTotal?: number; bytesUsed?: number; bytesAvailable?: number }
              versions?: { overall?: Record<string, number> }
              // Same field cmd/main.go's readCephHealth reads for
              // FrameStorage's Healthy condition: one entry per failed
              // check, keyed by check code, message-only (no summary
              // nesting — that shape is ceph's own `status --format json`,
              // not what Rook publishes on the CR).
              details?: Record<string, { message?: string; severity?: string }>
            }
          }
        }>
      >(`/apis/ceph.rook.io/v1/namespaces/${ns}/cephclusters`),
      k8sFetch<ListResponse<{ metadata: { name: string }; spec?: { replicated?: { size?: number } } }>>(
        `/apis/ceph.rook.io/v1/namespaces/${ns}/cephblockpools`,
      ),
      k8sFetch<ListResponse<{ status?: { phase?: string } }>>(
        integrationPods(cfg.integrations.cephOsd),
      ),
      k8sFetch<ListResponse<{ status?: { phase?: string } }>>(
        integrationPods(cfg.integrations.cephMon),
      ),
    ])

    const cluster = clusters.items?.[0] ?? {}
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
      // Carried through verbatim — R18. No remap here: see CephStatus.details.
      details: cluster.status?.ceph?.details ?? {},
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
    >(ALL_PODS_CACHED)

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
  async alluxio(): Promise<AlluxioStats> {
    const alluxio = config().integrations.alluxio
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(alluxio),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) throw new FrameAPIError(404, 'Alluxio not deployed')

    const res = await proxyFetch(integrationProxy(alluxio, name, '/metrics/json/'))
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
    const nodeExporter = config().integrations.nodeExporter
    const pods = await k8sFetch<
      ListResponse<{ metadata: { name: string }; spec?: { nodeName?: string } }>
    >(integrationPods(nodeExporter))
    const results = await Promise.all(
      (pods.items ?? []).map(async (p) => {
        try {
          const res = await proxyFetch(integrationProxy(nodeExporter, p.metadata.name, '/metrics'))
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
  async network(): Promise<NetNode[]> {
    const devices = config().network.devices
    const metrics = await this.nodeExporterMetrics()
    if (!metrics.length) throw new FrameAPIError(404, 'node-exporter not deployed')

    const g = (text: string, key: string, dev: string) => {
      const m = text.match(
        new RegExp(`^${key}\\{device="${escapeRegExp(dev)}"\\}\\s+([0-9.e+-]+)`, 'm'),
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
   *
   * The requested half asks Prometheus for two scalars and only falls back to
   * summing the pod list itself when that is unavailable. HeaderStats calls
   * this on every screen with a 30 s poll, and the pod list is 432 KB on this
   * cluster: it was 105 reads and 45 MB in one measured session, sixty percent
   * of all API traffic the console generated, to compute two numbers.
   */
  async capacity(): Promise<CapacityResource[]> {
    const nodes = await k8sFetch<
      ListResponse<{ status?: { allocatable?: Record<string, string> } }>
    >('/api/v1/nodes')

    let allocCpu = 0
    let allocMem = 0
    for (const n of nodes.items ?? []) {
      allocCpu += cpuToCores(n.status?.allocatable?.cpu)
      allocMem += memToGiB(n.status?.allocatable?.memory)
    }

    let reqCpu = 0
    let reqMem = 0
    const fromProm = await requestedFromPrometheus()
    if (fromProm) {
      ;[reqCpu, reqMem] = fromProm
    } else {
      const pods = await k8sFetch<
        ListResponse<{
          status?: { phase?: string }
          spec?: { containers?: Array<{ resources?: { requests?: Record<string, string> } }> }
        }>
      >(ALL_PODS_CACHED)
      for (const p of pods.items ?? []) {
        if (p.status?.phase === 'Succeeded' || p.status?.phase === 'Failed') continue
        for (const c of p.spec?.containers ?? []) {
          reqCpu += cpuToCores(c.resources?.requests?.cpu)
          reqMem += memToGiB(c.resources?.requests?.memory)
        }
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

  /** Set a Volcano queue's share weight — its slice of capacity when queues compete. */
  async setQueueWeight(name: string, weight: number): Promise<void> {
    await k8sFetch<undefined>(`/apis/scheduling.volcano.sh/v1beta1/queues/${name}`, {
      action: `set queue ${name} weight to ${weight}`,
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec: { weight } },
    })
  }

  /**
   * Open or close a Volcano queue. Closing stops new PodGroups from being
   * admitted; the ones already running keep their resources.
   *
   * Deliberately not a spec patch. The Queue CRD has no `spec.state` — setting
   * one returns 200 and is then silently pruned by the structural schema
   * (verified against a live cluster). State is status-only and driven by a
   * `bus.volcano.sh` Command that the queue controller consumes and deletes,
   * which is what `vcctl queue --action` issues. The Command needs the target's
   * UID, hence the read first.
   *
   * The controller is asynchronous, so a created Command is a request, not a
   * result — we poll until the queue reports the requested state so a success
   * here means the queue actually flipped.
   */
  async setQueueState(name: string, state: 'Open' | 'Closed'): Promise<void> {
    const url = `/apis/scheduling.volcano.sh/v1beta1/queues/${name}`
    const queue = await k8sFetch<{ metadata: { uid: string } }>(url)

    const commandNs = config().namespaces.volcanoCommands
    await k8sFetch<unknown>(`/apis/bus.volcano.sh/v1alpha1/namespaces/${commandNs}/commands`, {
      action: `${state === 'Open' ? 'open' : 'close'} queue ${name}`,
      method: 'POST',
      body: {
        apiVersion: 'bus.volcano.sh/v1alpha1',
        kind: 'Command',
        metadata: {
          generateName: `${state.toLowerCase()}queue-`,
          namespace: commandNs,
        },
        action: `${state}Queue`,
        target: {
          apiVersion: 'scheduling.volcano.sh/v1beta1',
          kind: 'Queue',
          name,
          uid: queue.metadata.uid,
        },
        reason: 'FrameUI',
        message: `${state === 'Open' ? 'Opened' : 'Closed'} from the Frame UI`,
      },
    })

    for (let i = 0; i < 20; i++) {
      await new Promise((resolve) => setTimeout(resolve, 500))
      const current = await k8sFetch<{ status?: { state?: string } }>(url)
      if (current.status?.state === state) return
    }
    throw new FrameAPIError(504, `queue ${name} did not reach ${state} within 10s`)
  }

  /** Live pipeline lineage from Argo Workflows: each run's DAG steps as timed spans. */
  async workflows(): Promise<WorkflowTrace[]> {
    const namespace = config().namespaces.argo
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
      >(ALL_PODS_CACHED),
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

  /** Burst-buffer SSD tier: capacity/used of the configured scratch mount per node. */
  async burstBuffer(): Promise<BurstNode[]> {
    const mount = config().burstBuffer.mount
    const metrics = await this.nodeExporterMetrics()
    if (!metrics.length) throw new FrameAPIError(404, 'node-exporter not deployed')
    const g = (text: string, key: string) => {
      const m = text.match(
        new RegExp(`^${key}\\{[^}]*mountpoint="${escapeRegExp(mount)}"[^}]*\\}\\s+([0-9.e+-]+)`, 'm'),
      )
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
        apiBase('framenodes'),
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
    const dcgm = config().integrations.dcgm
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(dcgm),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) throw new FrameAPIError(404, 'DCGM exporter not deployed')
    const res = await proxyFetch(integrationProxy(dcgm, name, '/metrics'))
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
    const llamacpp = config().integrations.llamacpp
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string }; spec: { nodeName?: string } }>>(
      integrationPods(llamacpp),
    )
    const pod = pods.items?.find((p) => p.metadata.name)
    if (!pod) return null
    const name = pod.metadata.name
    const base = integrationProxy(llamacpp, name, '')

    const mRes = await proxyFetch(`${base}/metrics`)
    if (!mRes.ok) throw new FrameAPIError(mRes.status, 'cannot read inference metrics')
    const text = await mRes.text()
    const num = (k: string) => Number(text.match(new RegExp(`^${k}\\s+([0-9.e+-]+)`, 'm'))?.[1] ?? 0)

    // /props → context window, slot count, model. Best-effort (never fatal).
    let nCtx = 0
    let slots = 0
    let model = ''
    try {
      const p = await (await proxyFetch(`${base}/props`)).json()
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
   * Live TEI (text-embeddings-inference) status from its Prometheus /metrics
   * + /info. Runs CPU-only in this deployment — no GPU telemetry to report.
   */
  async teiStatus(): Promise<TeiStatus | null> {
    const tei = config().integrations.tei
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string }; spec: { nodeName?: string } }>>(
      integrationPods(tei),
    )
    const pod = pods.items?.find((p) => p.metadata.name)
    if (!pod) return null
    const name = pod.metadata.name
    const base = integrationProxy(tei, name, '')

    const mRes = await proxyFetch(`${base}/metrics`)
    if (!mRes.ok) throw new FrameAPIError(mRes.status, 'cannot read TEI metrics')
    const text = await mRes.text()
    const num = (k: string) => Number(text.match(new RegExp(`^${k}(?:\\{[^}]*\\})?\\s+([0-9.e+-]+)`, 'm'))?.[1] ?? 0)

    const durationSum = num('te_request_inference_duration_sum')
    const durationCount = num('te_request_inference_duration_count')

    let model = ''
    let dtype = ''
    try {
      const info = await (await proxyFetch(`${base}/info`)).json()
      model = info.model_id ?? ''
      dtype = info.model_dtype ?? ''
    } catch {
      /* info optional */
    }

    return {
      model: model || 'TEI',
      dtype,
      node: pod.spec.nodeName ?? '',
      requestCount: num('te_request_count'),
      successCount: num('te_request_success'),
      queueSize: num('te_queue_size'),
      avgInferenceMs: durationCount ? (durationSum / durationCount) * 1000 : 0,
    }
  }

  /**
   * Runtime security detections from Falco (via Falcosidekick Prometheus
   * metrics). Each series is one (rule, priority, workload) with a firing count.
   */
  async security(): Promise<SecurityStatus | null> {
    const falco = config().integrations.falcosidekick
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(falco),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) return null
    const res = await proxyFetch(integrationProxy(falco, name, '/metrics'))
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

  /**
   * Tetragon (eBPF) process + network activity, aggregated from its Prometheus
   * metrics. PROCESS_KPROBE counts come from the tcp_connect TracingPolicy =
   * outbound network connections per workload.
   */
  async tetragon(): Promise<TetragonActivity | null> {
    const tetragon = config().integrations.tetragon
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(tetragon),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) return null
    const res = await proxyFetch(integrationProxy(tetragon, name, '/metrics'))
    if (!res.ok) throw new FrameAPIError(res.status, 'cannot read Tetragon metrics')
    const text = await res.text()

    const lbl = (labels: string, k: string) => labels.match(new RegExp(`${k}="([^"]*)"`))?.[1] ?? ''
    const re = /^tetragon_events_total\{([^}]*)\}\s+([0-9.e+-]+)/gm
    let exec = 0
    let network = 0
    let exit = 0
    const net = new Map<string, { workload: string; namespace: string; count: number }>()
    const ex = new Map<string, { binary: string; workload: string; count: number }>()
    let m: RegExpExecArray | null
    while ((m = re.exec(text))) {
      const [, labels, valStr] = m
      const v = Number(valStr)
      const type = lbl(labels, 'type')
      const ns = lbl(labels, 'namespace')
      const workload = lbl(labels, 'workload')
      if (type === 'PROCESS_EXEC') {
        exec += v
        const binary = lbl(labels, 'binary')
        const key = `${binary}\u0000${workload}`
        const e = ex.get(key) ?? { binary, workload, count: 0 }
        e.count += v
        ex.set(key, e)
      } else if (type === 'PROCESS_KPROBE') {
        network += v
        const key = `${workload}\u0000${ns}`
        const e = net.get(key) ?? { workload, namespace: ns, count: 0 }
        e.count += v
        net.set(key, e)
      } else if (type === 'PROCESS_EXIT') {
        exit += v
      }
    }
    return {
      exec,
      network,
      exit,
      topNetwork: Array.from(net.values()).sort((a, b) => b.count - a.count).slice(0, 8),
      topExec: Array.from(ex.values()).sort((a, b) => b.count - a.count).slice(0, 8),
    }
  }

  /**
   * Neura cluster jobs = ephemeral sandbox pods (coding-agent / CAD). Ops
   * summary: counts by phase + recent pods. Detail lives in Neura itself.
   */
  async neuraSandboxJobs(): Promise<{ byPhase: Record<string, number>; recent: Array<{ name: string; phase: string; node: string; age: string }> }> {
    const res = await k8sFetch<ListResponse<{ metadata: { name: string; creationTimestamp?: string }; spec?: { nodeName?: string }; status?: { phase?: string } }>>(
      integrationPods(config().integrations.neuraSandbox),
    )
    const byPhase: Record<string, number> = {}
    const recent = (res.items ?? [])
      .map((p) => {
        const phase = p.status?.phase ?? 'Unknown'
        byPhase[phase] = (byPhase[phase] ?? 0) + 1
        return {
          name: p.metadata.name,
          phase,
          node: p.spec?.nodeName ?? '',
          age: p.metadata.creationTimestamp ?? '',
        }
      })
      .sort((a, b) => b.age.localeCompare(a.age))
      .slice(0, 8)
    return { byPhase, recent }
  }

  /** Velero backup status (DR): storage location, schedule, recent runs. */
  async backups(): Promise<BackupStatus | null> {
    type Backup = {
      metadata: { name: string; creationTimestamp?: string }
      spec?: { includedNamespaces?: string[] }
      status?: {
        phase?: string
        completionTimestamp?: string
        errors?: number
        progress?: { itemsBackedUp?: number }
      }
    }
    const base = `/apis/velero.io/v1/namespaces/${config().namespaces.velero}`
    const [bk, bsl, sch] = await Promise.all([
      k8sFetch<ListResponse<Backup>>(`${base}/backups`).catch(() => null),
      k8sFetch<ListResponse<{ status?: { phase?: string } }>>(`${base}/backupstoragelocations`).catch(() => null),
      k8sFetch<ListResponse<{ spec?: { schedule?: string }; status?: { phase?: string } }>>(`${base}/schedules`).catch(() => null),
    ])
    if (!bk && !bsl && !sch) return null

    const recent: BackupRun[] = (bk?.items ?? [])
      .map((b) => ({
        name: b.metadata.name,
        phase: b.status?.phase ?? 'Unknown',
        items: b.status?.progress?.itemsBackedUp ?? 0,
        completed: b.status?.completionTimestamp ?? '',
        errors: b.status?.errors ?? 0,
        namespaces: b.spec?.includedNamespaces ?? [],
        _ts: b.status?.completionTimestamp ?? b.metadata.creationTimestamp ?? '',
      }))
      .sort((a, b) => b._ts.localeCompare(a._ts))
      .slice(0, 8)
      .map(({ _ts, ...r }) => r)

    const lastSuccess =
      (bk?.items ?? [])
        .filter((b) => b.status?.phase === 'Completed' && b.status?.completionTimestamp)
        .map((b) => b.status!.completionTimestamp!)
        .sort()
        .pop() ?? null

    return {
      storageReady: (bsl?.items ?? []).some((l) => l.status?.phase === 'Available'),
      schedule: (sch?.items ?? []).find((s) => s.status?.phase === 'Enabled')?.spec?.schedule ?? null,
      lastSuccess,
      recent,
    }
  }

  /** Trigger an on-demand Velero backup of the whole cluster. */
  async triggerBackup(): Promise<{ name: string }> {
    const name = toK8sName(`on-demand-${new Date().toISOString()}`)
    const veleroNs = config().namespaces.velero
    await k8sFetch<undefined>(`/apis/velero.io/v1/namespaces/${veleroNs}/backups`, {
      action: `trigger backup ${name}`,
      method: 'POST',
      body: {
        apiVersion: 'velero.io/v1',
        kind: 'Backup',
        metadata: { name, namespace: veleroNs },
        spec: {},
      },
    })
    return { name }
  }

  /**
   * Recent history for arbitrary PromQL, via Prometheus range queries.
   *
   * This is the generic half of what `forecast()` used to do inline. Screens
   * that only want a sparkline call this directly; `forecast()` layers its
   * projection on top. Returns null when Prometheus isn't deployed, so a caller
   * can tell "no monitoring" apart from "monitored, but flat".
   *
   * A query that fails or returns nothing yields an empty `history` rather than
   * dropping the series, so a caller rendering one sparkline per query keeps a
   * stable set of slots.
   */
  async range(queries: RangeQuery[], windowHours = 3, step = 300): Promise<MetricSeries[] | null> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(config().integrations.prometheus),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) return null
    const base = integrationProxy(config().integrations.prometheus, name, '/api/v1/query_range')
    const end = Math.floor(Date.now() / 1000)
    const start = end - windowHours * 3600

    return Promise.all(
      queries.map(async ({ metric, q, unit }): Promise<MetricSeries> => {
        const empty: MetricSeries = { metric, current: 0, history: [], unit }
        const url = `${base}?query=${encodeURIComponent(q)}&start=${start}&end=${end}&step=${step}`
        let json: unknown
        try {
          const res = await proxyFetch(url)
          if (!res.ok) return empty
          json = await res.json()
        } catch {
          return empty
        }
        const values: Array<[number, string]> =
          (json as { data?: { result?: Array<{ values?: Array<[number, string]> }> } })?.data?.result?.[0]?.values ?? []
        const history = values
          .map(([t, v]) => ({ t: t * 1000, v: Number(v) }))
          .filter((p) => Number.isFinite(p.v))
        return { metric, current: history[history.length - 1]?.v ?? 0, history, unit }
      }),
    )
  }

  /**
   * Capacity trend + forecast from Prometheus range queries (cluster CPU/mem
   * usage over the last few hours), with a linear projection to "days to full".
   * Returns null if Prometheus isn't deployed.
   */
  async forecast(windowHours = 3): Promise<ForecastStatus | null> {
    const series = await this.range(
      [
        { metric: 'Memory', q: '100 * (1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes))', unit: '%' },
        { metric: 'CPU', q: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])))', unit: '%' },
      ],
      windowHours,
    )
    if (!series) return null
    return { series: series.map(projectToFull), windowHours }
  }

  /**
   * Active alerts from Alertmanager (Prometheus rules + routed Falco security
   * detections). Meta alerts (Watchdog/InfoInhibitor) are dropped.
   */
  async alerts(): Promise<AlertsStatus | null> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(config().integrations.alertmanager),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) return null
    const res = await proxyFetch(
      integrationProxy(config().integrations.alertmanager, name, '/api/v2/alerts'),
    )
    if (!res.ok) throw new FrameAPIError(res.status, 'cannot read Alertmanager alerts')
    const raw: Array<{
      labels?: Record<string, string>
      annotations?: Record<string, string>
      status?: { state?: string }
      startsAt?: string
    }> = await res.json()

    const rank: Record<string, number> = { critical: 0, warning: 1, info: 2, none: 3 }
    const bySeverity: Record<string, number> = {}
    const alerts: ActiveAlert[] = raw
      .filter((a) => !['Watchdog', 'InfoInhibitor'].includes(a.labels?.alertname ?? ''))
      .map((a) => ({
        name: a.labels?.alertname ?? a.labels?.rule ?? 'Unknown',
        severity: a.labels?.severity ?? 'none',
        state: a.status?.state ?? '',
        summary: a.annotations?.summary ?? a.annotations?.description ?? '',
        namespace: a.labels?.namespace ?? a.labels?.k8s_ns_name ?? '',
        startsAt: a.startsAt ?? '',
        labels: a.labels ?? {},
      }))
    for (const a of alerts) bySeverity[a.severity] = (bySeverity[a.severity] ?? 0) + 1
    alerts.sort(
      (a, b) => (rank[a.severity] ?? 9) - (rank[b.severity] ?? 9) || b.startsAt.localeCompare(a.startsAt),
    )
    return { alerts, bySeverity }
  }

  /**
   * Proxy base for the Alertmanager pod's v2 API. Throws when Alertmanager is
   * absent — unlike `alerts()`, which returns null, because every caller here
   * is a user-initiated write that should surface the failure rather than
   * silently do nothing.
   */
  private async alertmanagerBase(): Promise<string> {
    const pods = await k8sFetch<ListResponse<{ metadata: { name: string } }>>(
      integrationPods(config().integrations.alertmanager),
    )
    const name = pods.items?.[0]?.metadata.name
    if (!name) throw new FrameAPIError(404, 'Alertmanager pod not found')
    return integrationProxy(config().integrations.alertmanager, name, '/api/v2')
  }

  /** Silences that are currently active or pending — expired ones are dropped. */
  async silences(): Promise<AlertSilence[]> {
    const res = await proxyFetch(`${await this.alertmanagerBase()}/silences`)
    if (!res.ok) throw new FrameAPIError(res.status, 'cannot read Alertmanager silences')
    const raw: Array<{
      id: string
      status?: { state?: string }
      matchers?: Array<{ name: string; value: string; isEqual?: boolean; isRegex?: boolean }>
      startsAt?: string
      endsAt?: string
      createdBy?: string
      comment?: string
    }> = await res.json()

    return raw
      .filter((s) => s.status?.state !== 'expired')
      .map((s) => ({
        id: s.id,
        state: s.status?.state ?? '',
        matchers: (s.matchers ?? [])
          .map((m) => `${m.name}${m.isEqual === false ? '!' : ''}${m.isRegex ? '~' : '='}${m.value}`)
          .join(', '),
        startsAt: s.startsAt ?? '',
        endsAt: s.endsAt ?? '',
        createdBy: s.createdBy ?? '',
        comment: s.comment ?? '',
      }))
      .sort((a, b) => a.endsAt.localeCompare(b.endsAt))
  }

  /**
   * End a silence now. Alertmanager has no hard delete — DELETE moves `endsAt`
   * to the present, so the silence stays in the log as expired rather than
   * vanishing. That's why `silences()` filters on state instead of assuming
   * the list only holds live entries.
   */
  async expireSilence(id: string): Promise<void> {
    const res = await proxyFetch(`${await this.alertmanagerBase()}/silence/${id}`, { method: 'DELETE' })
    if (!res.ok) throw new FrameAPIError(res.status, `cannot expire silence: ${await res.text()}`)
  }

  /** Silence an active alert in Alertmanager for `durationMinutes`, matching on all its labels. */
  async silenceAlert(alert: ActiveAlert, durationMinutes: number, createdBy: string): Promise<void> {
    const startsAt = new Date()
    const endsAt = new Date(startsAt.getTime() + durationMinutes * 60_000)
    const res = await proxyFetch(`${await this.alertmanagerBase()}/silences`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        matchers: Object.entries(alert.labels).map(([labelName, value]) => ({
          name: labelName,
          value,
          isRegex: false,
          isEqual: true,
        })),
        startsAt: startsAt.toISOString(),
        endsAt: endsAt.toISOString(),
        createdBy,
        comment: `Silenced from Frame UI (${durationMinutes}m)`,
      }),
    })
    if (!res.ok) throw new FrameAPIError(res.status, `cannot create silence: ${await res.text()}`)
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

/**
 * The collections an application is assembled from, and therefore exactly the
 * collections a screen showing applications must watch.
 *
 * `ApplicationClient.list()` builds its own reads from this array rather than
 * naming the paths again, and `ApplicationsView` watches it. That is what
 * keeps the two from drifting: a screen that watched Deployments but not
 * StatefulSets would sit frozen through a database scaling while tracking its
 * API perfectly, and nothing about it would look wrong.
 *
 * Order is load-bearing — it pairs with APPLICATION_KINDS below.
 */
export const APPLICATION_WATCH_PATHS = [
  '/apis/apps/v1/deployments',
  '/apis/apps/v1/statefulsets',
] as const

const APPLICATION_KINDS: readonly AppComponent['kind'][] = ['Deployment', 'StatefulSet']

class ApplicationClient {
  /**
   * List deployed applications across all non-system namespaces by reading
   * every collection in APPLICATION_WATCH_PATHS and grouping them by Helm
   * release.
   */
  async list(): Promise<Application[]> {
    const [deps, sts] = await Promise.all(
      APPLICATION_WATCH_PATHS.map((path) => k8sFetch<ListResponse<WorkloadCR>>(path)),
    )

    const workloads: Array<{ cr: WorkloadCR; kind: AppComponent['kind'] }> = [
      ...(deps.items ?? []).map((cr) => ({ cr, kind: APPLICATION_KINDS[0] })),
      ...(sts.items ?? []).map((cr) => ({ cr, kind: APPLICATION_KINDS[1] })),
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

  /**
   * The pods currently backing one component, so an operator looking at an
   * unhealthy application can read its output without leaving the screen.
   *
   * Resolved through the workload's own `spec.selector.matchLabels` rather
   * than by walking ownerReferences. For a Deployment that walk is two hops --
   * Deployment to ReplicaSet to Pod -- and during a rollout it spans two
   * ReplicaSets, so the label selector is both shorter and the only form that
   * answers the same way for a StatefulSet. It is also what the controller
   * itself uses to decide which pods are its own.
   *
   * Two requests, and only when the operator asks: the applications list does
   * not carry pods, and pulling every pod in the cluster to fill a panel
   * nobody opened is what Important 7 of a previous review was about.
   */
  async pods(component: Pick<AppComponent, 'kind' | 'namespace' | 'name'>): Promise<WorkloadPod[]> {
    const plural = component.kind === 'Deployment' ? 'deployments' : 'statefulsets'
    const workload = await k8sFetch<{ spec?: { selector?: { matchLabels?: Record<string, string> } } }>(
      `/apis/apps/v1/namespaces/${component.namespace}/${plural}/${component.name}`,
    )
    const labels = workload.spec?.selector?.matchLabels ?? {}
    const selector = Object.entries(labels)
      .map(([k, v]) => `${k}=${v}`)
      .join(',')
    // A workload with no selector would otherwise list every pod in the
    // namespace and present another application's output as this one's.
    if (!selector) return []

    const pods = await k8sFetch<ListResponse<PodItemCR>>(
      `/api/v1/namespaces/${component.namespace}/pods?labelSelector=${encodeURIComponent(selector)}`,
    )
    return (pods.items ?? []).map(toPod)
  }

  /** Rolling-restart a Deployment/StatefulSet by bumping its pod template restart annotation. */
  async restart(component: Pick<AppComponent, 'kind' | 'namespace' | 'name'>): Promise<void> {
    const plural = component.kind === 'Deployment' ? 'deployments' : 'statefulsets'
    await k8sFetch<undefined>(
      `/apis/apps/v1/namespaces/${component.namespace}/${plural}/${component.name}`,
      {
        action: boundedActionLabel(
          `restart ${component.kind.toLowerCase()} ${component.namespace}/${component.name}`,
        ),
        method: 'PATCH',
        contentType: 'application/strategic-merge-patch+json',
        body: {
          spec: {
            template: {
              metadata: {
                annotations: { 'kubectl.kubernetes.io/restartedAt': new Date().toISOString() },
              },
            },
          },
        },
      },
    )
  }

  /** Scale a Deployment/StatefulSet to an explicit replica count. */
  async scale(component: Pick<AppComponent, 'kind' | 'namespace' | 'name'>, replicas: number): Promise<void> {
    const plural = component.kind === 'Deployment' ? 'deployments' : 'statefulsets'
    await k8sFetch<undefined>(
      `/apis/apps/v1/namespaces/${component.namespace}/${plural}/${component.name}/scale`,
      {
        action: boundedActionLabel(
          `scale ${component.kind.toLowerCase()} ${component.namespace}/${component.name} to ${replicas}`,
        ),
        method: 'PATCH', contentType: 'application/merge-patch+json', body: { spec: { replicas } },
      },
    )
  }
}

/** The collection endpoint for each kind the Workloads screen shows. */
const WORKLOAD_COLLECTIONS: Record<EditableKind, string> = {
  Pod: '/api/v1/pods',
  Deployment: '/apis/apps/v1/deployments',
  StatefulSet: '/apis/apps/v1/statefulsets',
  DaemonSet: '/apis/apps/v1/daemonsets',
  Job: '/apis/batch/v1/jobs',
}

/**
 * The namespaced path for one object of a kind the Workloads screen shows.
 *
 * Built from the cluster-wide collection above by splicing the namespace in,
 * so the two can never name different resources — the failure that made the
 * Accounts screen read `frameusers` out of the wrong namespace was exactly a
 * second place where a path was assembled.
 */
export function workloadPath(kind: EditableKind, namespace: string, name?: string): string {
  const collection = WORKLOAD_COLLECTIONS[kind]
  const cut = collection.lastIndexOf('/')
  const base = `${collection.slice(0, cut)}/namespaces/${namespace}${collection.slice(cut)}`
  return name ? `${base}/${name}` : base
}

/**
 * The list paths the Workloads screen watches for live updates.
 *
 * `Pod` deliberately absent (whole-branch review Important 7): cluster-wide
 * pod churn — Argo workflows, Jobs, evictions — is exactly the "something
 * that churns per second" k8s-watch.ts's own header says needs an incremental
 * cache instead of a watch-triggers-refetch screen, and every event here was
 * coalesced at 250ms into a fresh WorkloadClient.tree() call that issues six
 * unpaginated cluster-wide GETs, including every pod and every ReplicaSet
 * (~10 per Deployment) — the same "33 whole-cluster pod lists totalling
 * 13.6 MB" burst `inFlightGets`'s own comment above measured, from one
 * screen re-firing on pod churn this time rather than several screens
 * duplicating each other. Dropping it does not stop the
 * tree from noticing pod changes: it still refreshes on the next controller
 * event (a Deployment's status reflects its pods) or when the screen is
 * reopened. Before adding this back, read k8s-watch.ts's header first — that
 * boundary is why it was removed, not an oversight.
 */
export function workloadWatchPaths(): string[] {
  return [
    WORKLOAD_COLLECTIONS.Deployment,
    WORKLOAD_COLLECTIONS.StatefulSet,
    WORKLOAD_COLLECTIONS.DaemonSet,
    WORKLOAD_COLLECTIONS.Job,
  ]
}

interface MetaCR {
  name: string
  namespace: string
  creationTimestamp?: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  ownerReferences?: Array<{ kind: string; name: string; controller?: boolean }>
}

interface WorkloadItemCR {
  metadata: MetaCR
  spec?: { replicas?: number; parallelism?: number; completions?: number }
  status?: {
    readyReplicas?: number
    replicas?: number
    numberReady?: number
    desiredNumberScheduled?: number
    succeeded?: number
    active?: number
  }
}

interface PodItemCR {
  metadata: MetaCR
  spec?: { nodeName?: string; containers?: Array<{ name: string }> }
  status?: { phase?: string; containerStatuses?: Array<{ restartCount?: number }> }
}

/** The controlling ownerReference, which is the only one that means "belongs to". */
function controllerOwner(meta: MetaCR): OwnerRef | undefined {
  const refs = meta.ownerReferences ?? []
  const owner = refs.find((r) => r.controller) ?? refs[0]
  return owner ? { kind: owner.kind, name: owner.name } : undefined
}

function toController(kind: WorkloadKind, cr: WorkloadItemCR): WorkloadController {
  // Each kind counts its readiness in its own fields: a DaemonSet's desired
  // count is the number of matching nodes, not a replica setting, and a Job's
  // is its completions. Reading `spec.replicas` for all four would report 0/0
  // for half the tree.
  const desired =
    kind === 'DaemonSet'
      ? (cr.status?.desiredNumberScheduled ?? 0)
      : kind === 'Job'
        ? (cr.spec?.completions ?? cr.spec?.parallelism ?? 1)
        : (cr.spec?.replicas ?? 0)
  const ready =
    kind === 'DaemonSet'
      ? (cr.status?.numberReady ?? 0)
      : kind === 'Job'
        ? (cr.status?.succeeded ?? 0)
        : (cr.status?.readyReplicas ?? 0)
  return {
    kind,
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    desiredReplicas: desired,
    readyReplicas: ready,
    scalable: kind === 'Deployment' || kind === 'StatefulSet',
  }
}

function toPod(cr: PodItemCR): WorkloadPod {
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    phase: cr.status?.phase ?? 'Unknown',
    nodeName: cr.spec?.nodeName ?? '',
    restarts: (cr.status?.containerStatuses ?? []).reduce((n, s) => n + (s.restartCount ?? 0), 0),
    containers: (cr.spec?.containers ?? []).map((c) => c.name),
    createdAt: cr.metadata.creationTimestamp,
    owner: controllerOwner(cr.metadata),
  }
}

/**
 * Operating the cluster's workloads: the tree, the logs, the manifest, and the
 * four writes.
 *
 * Every write goes through `k8sFetch` with an `action`, which is what makes it
 * appear on the Tasks screen as something a person did rather than as the
 * request it was. A bare `fetch` here would skip the 401 retry and leave the
 * record unlabelled, which is why the structural guard at the bottom of
 * `frame-sdk.test.ts` exists.
 */
class WorkloadClient {
  async tree(): Promise<NamespaceNode[]> {
    const [deployments, statefulsets, daemonsets, jobs, replicasets, pods] = await Promise.all([
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.Deployment),
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.StatefulSet),
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.DaemonSet),
      k8sFetch<ListResponse<WorkloadItemCR>>(WORKLOAD_COLLECTIONS.Job),
      k8sFetch<ListResponse<{ metadata: MetaCR }>>('/apis/apps/v1/replicasets'),
      k8sFetch<ListResponse<PodItemCR>>(WORKLOAD_COLLECTIONS.Pod),
    ])

    const controllers: WorkloadController[] = [
      ...(deployments.items ?? []).map((c) => toController('Deployment', c)),
      ...(statefulsets.items ?? []).map((c) => toController('StatefulSet', c)),
      ...(daemonsets.items ?? []).map((c) => toController('DaemonSet', c)),
      ...(jobs.items ?? []).map((c) => toController('Job', c)),
    ]

    // A Deployment's pods name a ReplicaSet as their owner, never the
    // Deployment, so the tree needs this one extra hop to place them.
    const replicaSetOwners = new Map<string, OwnerRef>()
    for (const rs of replicasets.items ?? []) {
      const owner = controllerOwner(rs.metadata)
      if (owner) replicaSetOwners.set(`${rs.metadata.namespace}/${rs.metadata.name}`, owner)
    }

    return buildWorkloadTree({
      controllers,
      pods: (pods.items ?? []).map(toPod),
      replicaSetOwners,
    })
  }

  /**
   * The raw log response, left as a stream so the caller can read it line by
   * line with `pumpLogLines` while the container keeps writing.
   *
   * This is a read, so it carries no `action` and leaves no FrameTask — see
   * the note at the top of `pod-logs.ts`.
   *
   * `signal` is optional and forwarded to `fetch` as-is. Without it, a caller
   * that abandons the pod mid-request (switches pods, closes the panel) has
   * no way to actually close the connection — cancelling the reader it gets
   * back does nothing for a request whose headers haven't arrived yet, and a
   * `follow=true` request leaves its apiserver log-watch open indefinitely.
   */
  logs(q: PodLogQuery, signal?: AbortSignal): Promise<Response> {
    return rawFetch(podLogPath(q), { signal })
  }

  /** The object as JSON — the diff's baseline, and where the ownership labels are read. */
  object(kind: EditableKind, namespace: string, name: string): Promise<Record<string, unknown>> {
    return k8sFetch<Record<string, unknown>>(workloadPath(kind, namespace, name))
  }

  /**
   * The object as YAML, serialised by the apiserver.
   *
   * `Accept: application/yaml` is a supported representation for every
   * resource, so the console ships no YAML serialiser and no parser — which is
   * both two fewer dependencies and one fewer place for the editor's text to
   * be silently rewritten.
   */
  async manifest(kind: EditableKind, namespace: string, name: string): Promise<string> {
    const res = await rawFetch(workloadPath(kind, namespace, name), {
      headers: { Accept: 'application/yaml' },
    })
    const text = await res.text()
    if (!res.ok) throw new FrameAPIError(res.status, text)
    return text
  }

  /**
   * Write the edited manifest, labelled with what actually changed.
   *
   * Two requests, and both are necessary:
   *
   *  1. `PUT ?dryRun=All` — the apiserver parses the YAML and answers with the
   *     object it *would* store. That is how the changed field paths are
   *     computed with no YAML parser on this side, and it validates the edit
   *     before anything is written. It leaves no FrameTask
   *     (`TaskRecorder.Start` skips a dry run), so the trail shows one row per
   *     edit.
   *  2. the real `PUT`, carrying the label.
   *
   * `yaml` is the user's text, which still contains the `resourceVersion` that
   * was read — so a concurrent change produces a 409 rather than silently
   * overwriting someone else's work. Do not strip it.
   */
  async applyManifest(o: {
    kind: EditableKind
    namespace: string
    name: string
    before: unknown
    yaml: string
  }): Promise<void> {
    const path = workloadPath(o.kind, o.namespace, o.name)
    const after = await k8sFetch<unknown>(`${path}?dryRun=All`, {
      method: 'PUT',
      contentType: 'application/yaml',
      rawBody: o.yaml,
    })
    const action = editActionLabel(o.kind, o.namespace, o.name, changedFieldPaths(o.before, after))
    await k8sFetch<unknown>(path, {
      method: 'PUT',
      contentType: 'application/yaml',
      rawBody: o.yaml,
      action,
    })
  }

  /**
   * Rolling-restart by bumping the pod template's `restartedAt` annotation
   * rather than deleting pods, so the controller's own update strategy —
   * surge, maxUnavailable, ordinal order for a StatefulSet — is respected.
   *
   * No `DaemonSet` here, deliberately (whole-branch review Important 2):
   * `cluster-control-workload-operator` grants `patch` on
   * `[deployments, statefulsets]` only (deploy/kubernetes/base/rbac-workload-operator.yaml),
   * so a DaemonSet restart 403s for every operator — the same asymmetry
   * WorkloadActions.tsx's `restartable` now matches instead of offering a
   * button that fails for one tier and not the other.
   */
  async restart(
    kind: 'Deployment' | 'StatefulSet',
    namespace: string,
    name: string,
  ): Promise<void> {
    await k8sFetch<undefined>(workloadPath(kind, namespace, name), {
      action: boundedActionLabel(`restart ${kind.toLowerCase()} ${namespace}/${name}`),
      method: 'PATCH',
      contentType: 'application/strategic-merge-patch+json',
      body: {
        spec: {
          template: {
            metadata: {
              annotations: { 'kubectl.kubernetes.io/restartedAt': new Date().toISOString() },
            },
          },
        },
      },
    })
  }

  /**
   * Scale through the `scale` subresource, which can only change the replica
   * count — unlike a full patch, it cannot touch the pod template. That is why
   * the two are separate grants in `deploy/kubernetes/base/rbac.yaml`.
   */
  async scale(
    kind: 'Deployment' | 'StatefulSet',
    namespace: string,
    name: string,
    replicas: number,
  ): Promise<void> {
    await k8sFetch<undefined>(`${workloadPath(kind, namespace, name)}/scale`, {
      action: boundedActionLabel(`scale ${kind.toLowerCase()} ${namespace}/${name} to ${replicas}`),
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec: { replicas } },
    })
  }

  async deletePod(namespace: string, name: string): Promise<void> {
    await k8sFetch<undefined>(workloadPath('Pod', namespace, name), {
      action: boundedActionLabel(`delete pod ${namespace}/${name}`),
      method: 'DELETE',
    })
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
      action: `discover node ${ip}`,
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

  async patchSpec(name: string, spec: FrameNodeSpec): Promise<void> {
    await k8sFetch<FrameNodeCR>(`${apiBase('framenodes', this.ns)}/${name}`, {
      action: `provision node ${name}`,
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec },
    })
  }

  async delete(name: string): Promise<void> {
    await k8sFetch<undefined>(`${apiBase('framenodes', this.ns)}/${name}`, {
      action: `delete node ${name}`, method: 'DELETE',
    })
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
    // spec.namespace is gone at v1beta1 (F5): the Workflow now runs in the
    // FrameJob's own namespace, so JobSpec.namespace has to steer where the
    // CR is created rather than what it points at. Directing a workflow into
    // a namespace you cannot create a FrameJob in is precisely the privilege
    // F5 withdrew. Keeping the field wired keeps the Jobs view's Retry
    // resubmitting into the namespace the original ran in.
    const ns = spec.namespace ?? this.ns
    const cr = await k8sFetch<FrameJobCR>(apiBase('framejobs', ns), {
      action: `submit job ${spec.name}`,
      method: 'POST',
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'FrameJob',
        metadata: { name: crName, namespace: frameNs(ns) },
        spec: {
          // Exactly one of the two is set — JobSpec's type makes the other
          // combination a compile error, so this is just forwarding it.
          ...(spec.pipeline ? { pipeline: spec.pipeline } : {}),
          ...(spec.container ? { container: spec.container } : {}),
          // No fallback for type either: the CRD defaults it to `background`,
          // and it is ignored server-side for a pipeline job anyway.
          ...(spec.type ? { type: spec.type } : {}),
          // No fallback: the CRD defaults serviceClass to LOW and priority to
          // medium now, so sending a value here is what made kubectl and the
          // UI disagree about what "unspecified" means (F4). Send only what
          // the user chose.
          ...(spec.serviceClass ? { serviceClass: spec.serviceClass } : {}),
          ...(spec.priority ? { priority: spec.priority } : {}),
          gpuCount:     spec.gpuCount ?? 0,
        },
      },
    })
    return crToJob(cr)
  }

  async cancel(id: string): Promise<{ cancelled: boolean; job: Job }> {
    const cr = await k8sFetch<FrameJobCR>(`${apiBase('framejobs', this.ns)}/${id}`)
    const job = crToJob(cr)
    await k8sFetch<undefined>(`${apiBase('framejobs', this.ns)}/${id}`, {
      action: `cancel job ${id}`, method: 'DELETE',
    })
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
        action: `create scheduling policy ${policy.name}`,
        method: 'POST',
        body: { apiVersion: `${GROUP}/${VERSION}`, kind: 'SchedulingPolicy', metadata: { name: crName, namespace: frameNs(this.ns) }, spec: specBody },
      })
      return crToPolicy(res)
    } catch (e) {
      if (e instanceof FrameAPIError && e.statusCode === 409) {
        const res = await k8sFetch<SchedulingPolicyCR>(`${apiBase('schedulingpolicies', this.ns)}/${crName}`, {
          action: `update scheduling policy ${policy.name}`,
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
    await k8sFetch<undefined>(`${apiBase('schedulingpolicies', this.ns)}/${name}`, {
      action: `delete scheduling policy ${name}`, method: 'DELETE',
    })
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
        action: `set quota for ${namespace}`,
        method: 'POST',
        body: { apiVersion: `${GROUP}/${VERSION}`, kind: 'FrameResourceQuota', metadata: { name: crName, namespace: frameNs(namespace) }, spec: specBody },
      })
      return crToQuota(res)
    } catch (e) {
      if (e instanceof FrameAPIError && e.statusCode === 409) {
        const res = await k8sFetch<FrameResourceQuotaCR>(`${apiBase('frameresourcequotas', namespace)}/${crName}`, {
          action: `update quota for ${namespace}`,
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
      // FrameNodeSpec has no gpuCount (see crToNode); the GPU count the
      // controller actually maintains lives in status.allocatable.
      totalGPUs:   nodes
        .filter((n) => n.spec.serviceClass === sc)
        .reduce((s, n) => s + quantityToNum(n.status?.allocatable?.['nvidia.com/gpu']), 0),
    }))
    return { items }
  }
}

// ── Talos ─────────────────────────────────────────────────────────────────────

/** A machine-config patch or an OS upgrade, as the control plane sees it. */
export interface TalosOperation {
  kind: 'TalosMachineConfig' | 'TalosUpgrade'
  name: string
  nodeName: string
  endpoint: string
  /** The Talos image, for an upgrade. Absent on a config patch. */
  image?: string
  /** True, False, or Unknown while the controller has not reported yet. */
  ready: 'True' | 'False' | 'Unknown'
  /** What the controller did, or why it could not: Applied, ClientBuildFailed, … */
  reason: string
  message: string
}

interface TalosCR {
  metadata: { name: string; namespace?: string }
  spec: { nodeName?: string; talosEndpoint?: string; image?: string }
  status?: {
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string }>
  }
}

function crToTalosOperation(kind: TalosOperation['kind'], cr: TalosCR): TalosOperation {
  const ready = cr.status?.conditions?.find((c) => c.type === 'Ready')
  return {
    kind,
    name: cr.metadata.name,
    nodeName: cr.spec.nodeName ?? '',
    endpoint: cr.spec.talosEndpoint ?? '',
    image: cr.spec.image,
    ready: (ready?.status as TalosOperation['ready']) ?? 'Unknown',
    reason: ready?.reason ?? 'Pending',
    message: ready?.message ?? '',
  }
}

/**
 * TalosMachineConfig and TalosUpgrade — the two CRDs that reconfigure and
 * upgrade a node's operating system.
 *
 * Both are write-once-and-watch: the controller talks Talos gRPC and reports
 * the outcome in the Ready condition, so there is nothing to edit afterwards.
 * Re-running an operation means a new object, which is also what makes the
 * history readable.
 */
class TalosClient {
  constructor(private readonly ns?: string) {}

  async listMachineConfigs(): Promise<TalosOperation[]> {
    const res = await k8sFetch<ListResponse<TalosCR>>(apiBase('talosmachineconfigs', this.ns))
    return (res.items ?? []).map((cr) => crToTalosOperation('TalosMachineConfig', cr))
  }

  async listUpgrades(): Promise<TalosOperation[]> {
    const res = await k8sFetch<ListResponse<TalosCR>>(apiBase('talosupgrades', this.ns))
    return (res.items ?? []).map((cr) => crToTalosOperation('TalosUpgrade', cr))
  }

  /** Both kinds, newest-looking first by node then name, for a single table. */
  async list(): Promise<TalosOperation[]> {
    const [configs, upgrades] = await Promise.all([
      this.listMachineConfigs(),
      this.listUpgrades(),
    ])
    return [...configs, ...upgrades].sort(
      (a, b) => a.nodeName.localeCompare(b.nodeName) || a.name.localeCompare(b.name),
    )
  }

  async applyMachineConfig(input: {
    name: string
    nodeName: string
    talosEndpoint: string
    configPatch: string
    secretName: string
  }): Promise<void> {
    await k8sFetch<TalosCR>(apiBase('talosmachineconfigs', this.ns), {
      action: `apply Talos machine config to ${input.nodeName}`,
      method: 'POST',
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'TalosMachineConfig',
        metadata: { name: toK8sName(input.name), namespace: frameNs(this.ns) },
        spec: {
          nodeName: input.nodeName,
          talosEndpoint: input.talosEndpoint,
          // No namespace (F6): TalosSecretReference is name-only at v1beta1,
          // and the Secret is always resolved in the CR's own namespace. The
          // value sent here was already that same namespace, so this is a
          // field the server would now prune, not a behaviour change.
          talosSecretRef: { name: input.secretName },
          configPatch: input.configPatch,
        },
      },
    })
  }

  async requestUpgrade(input: {
    name: string
    nodeName: string
    talosEndpoint: string
    image: string
    secretName: string
  }): Promise<void> {
    await k8sFetch<TalosCR>(apiBase('talosupgrades', this.ns), {
      action: `request Talos upgrade of ${input.nodeName}`,
      method: 'POST',
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'TalosUpgrade',
        metadata: { name: toK8sName(input.name), namespace: frameNs(this.ns) },
        spec: {
          nodeName: input.nodeName,
          talosEndpoint: input.talosEndpoint,
          // Name-only at v1beta1, as above (F6).
          talosSecretRef: { name: input.secretName },
          image: input.image,
        },
      },
    })
  }

  async remove(op: Pick<TalosOperation, 'kind' | 'name'>): Promise<void> {
    const plural = op.kind === 'TalosUpgrade' ? 'talosupgrades' : 'talosmachineconfigs'
    await k8sFetch<undefined>(`${apiBase(plural, this.ns)}/${op.name}`, {
      action: `delete ${op.kind} ${op.name}`, method: 'DELETE',
    })
  }
}

/**
 * Reads FrameMachine inventory and writes power requests, mapping through
 * `toMachine` (Task 7) rather than reshaping the CR here — every decision
 * about what a reading means (staleness, sensor availability, severity)
 * lives in `machines.ts`, which is the only place vitest can reach for it.
 */
class MachineClient {
  constructor(private readonly ns?: string) {}

  async list(): Promise<Machine[]> {
    const res = await k8sFetch<ListResponse<MachineCR>>(machinesPath(this.ns))
    return (res.items ?? []).map(toMachine).sort((a, b) => a.name.localeCompare(b.name))
  }

  /** For `useLiveResource`, so the console watches the same collection it reads. */
  watchPath(): string {
    return machinesPath(this.ns)
  }

  async power(machine: Machine, action: string): Promise<void> {
    await k8sFetch<undefined>(`${machinesPath(machine.namespace)}/${machine.name}`, {
      action: powerActionLabel(action, machine.name),
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: powerPatchBody(action),
    })
  }
}

/**
 * Reads FrameInstall status and creates new ones, mapping through
 * `toInstall` (Task 11) rather than reshaping the CR here — every decision
 * about what a reading means (phase, staleness, who may create one) lives
 * in `installs.ts`, which is the only place vitest can reach for it.
 *
 * Mirrors `MachineClient` above: a `list`/`watchPath` pair for the screen's
 * `useLiveResource`, plus the one write this screen makes. Unlike
 * `MachineClient.power`, that write is a `create`, never a `patch` —
 * nothing in this lot lets the console edit or delete a FrameInstall once
 * submitted; the phase machine and the finalizer own its lifecycle from
 * there.
 */
class InstallClient {
  constructor(private readonly ns?: string) {}

  async list(): Promise<Install[]> {
    const res = await k8sFetch<ListResponse<InstallCR>>(installsPath(this.ns))
    return (res.items ?? []).map(toInstall).sort((a, b) => a.name.localeCompare(b.name))
  }

  /** For `useLiveResource`, so the console watches the same collection it reads. */
  watchPath(): string {
    return installsPath(this.ns)
  }

  /**
   * Creates a `FrameInstall`. `namespace` defaults to `frameNs()`, same as
   * every other namespaced create in this SDK — never the caller's own
   * default, so a screen that never thinks about namespace still lands the
   * object where the console reads.
   *
   * The object name is `installObjectName(hostname, randomNameSuffix())`,
   * not the hostname: design §3 says a reinstallation is a second object
   * and the first stays readable, and naming by hostname made a retry
   * collide 409 with the attempt it was retrying. Returns the name so a
   * caller can say which object it made.
   */
  async create(hostname: string, spec: InstallCreateSpec, namespace?: string): Promise<string> {
    const ns = namespace ?? frameNs()
    const name = installObjectName(hostname, randomNameSuffix())
    await k8sFetch<undefined>(installsPath(ns), {
      action: `create install ${name} on ${spec.machineRef}`,
      method: 'POST',
      body: {
        apiVersion: `${GROUP}/${VERSION}`,
        kind: 'FrameInstall',
        metadata: { name, namespace: ns },
        spec,
      },
    })
    return name
  }
}

/**
 * A FrameStorage CR as the apiserver returns it — cluster-scoped
 * (`+kubebuilder:resource:scope=Cluster` in
 * `api/frame/v1beta1/framestorage_types.go`), so unlike every other Frame
 * kind read here there is no `metadata.namespace`.
 */
export interface FrameStorageCR {
  metadata: { name: string }
  spec?: {
    type?: string
    content?: string[]
    storageClassName?: string
    adoptExisting?: boolean
    nodes?: string[]
  }
  status?: {
    shared?: boolean
    phase?: string
    capacity?: { usable?: string; used?: string; raw?: string }
    claims?: { total?: number; labelled?: number }
    adopted?: boolean
    conditions?: Array<{ type: string; status: string; reason?: string; message?: string }>
  }
}

/**
 * A FrameStorage entry, projected for the screen. `capacity`/`claims` are
 * never left `undefined` — `capacityLine`/`claimGapLine` (storage.ts) are
 * written against the zero-valued shape, not an optional one, so an entry
 * whose reconcile has not yet populated `status.capacity` still renders
 * "usable unknown" rather than throwing.
 */
export interface StorageEntry {
  name: string
  type: string
  content: string[]
  storageClassName: string
  shared: boolean
  phase: string
  adopted: boolean
  capacity: StorageCapacity
  claims: StorageClaimCounts
  /** From the `Healthy` condition — its message carries the WARN reasons for a Degraded entry. */
  healthy: { status: string; reason: string; message: string } | null
  /** From the `Available` condition — message is `all nodes` or a comma-separated node list. */
  available: { status: string; message: string } | null
}

function findCondition(
  conditions: Array<{ type: string; status: string; reason?: string; message?: string }> | undefined,
  type: string,
): { type: string; status: string; reason?: string; message?: string } | undefined {
  return (conditions ?? []).find((c) => c.type === type)
}

function crToStorageEntry(cr: FrameStorageCR): StorageEntry {
  const status = cr.status ?? {}
  const healthy = findCondition(status.conditions, 'Healthy')
  const available = findCondition(status.conditions, 'Available')
  return {
    name: cr.metadata.name,
    type: cr.spec?.type ?? '',
    content: cr.spec?.content ?? [],
    storageClassName: cr.spec?.storageClassName ?? '',
    shared: status.shared ?? false,
    phase: status.phase ?? 'Unknown',
    adopted: status.adopted ?? false,
    capacity: {
      usable: status.capacity?.usable ?? '',
      used: status.capacity?.used ?? '',
      raw: status.capacity?.raw ?? '',
    },
    claims: {
      total: status.claims?.total ?? 0,
      labelled: status.claims?.labelled ?? 0,
    },
    healthy: healthy ? { status: healthy.status, reason: healthy.reason ?? '', message: healthy.message ?? '' } : null,
    available: available ? { status: available.status, message: available.message ?? '' } : null,
  }
}

/**
 * Reads FrameStorage entries, mapping each CR through `crToStorageEntry`
 * above — a structural projection, not a decision, so it stays beside the
 * other CR-to-view mappers in this module rather than in `storage.ts`
 * (which is reserved for the pure, tested judgments: `capacityLine`,
 * `claimGapLine`, `cephWarningReasons`, `describeDivergence`).
 *
 * Cluster-scoped, so unlike every other client in this file there is no
 * namespace to carry and no constructor argument.
 */
class StorageClient {
  async list(): Promise<StorageEntry[]> {
    const res = await k8sFetch<ListResponse<FrameStorageCR>>(storageEntriesPath())
    return (res.items ?? []).map(crToStorageEntry).sort((a, b) => a.name.localeCompare(b.name))
  }

  /** For `useLiveResource`, so the console watches the same collection it reads. */
  watchPath(): string {
    return storageEntriesPath()
  }
}

/** A FrameDiskClaim CR as the apiserver returns it. */
export interface FrameDiskClaimCR {
  metadata: { name: string; namespace: string }
  spec?: {
    machineRef?: { name?: string }
    byIDPath?: string
    serial?: string
    destination?: string
  }
  status?: {
    phase?: string
    message?: string
    claimUID?: string
  }
}

/** A FrameDiskClaim, projected for the screen. */
export interface DiskClaim {
  name: string
  namespace: string
  machineName: string
  byIDPath: string
  serial: string
  destination: string
  phase: string
  message: string
}

function crToDiskClaim(cr: FrameDiskClaimCR): DiskClaim {
  return {
    name: cr.metadata.name,
    namespace: cr.metadata.namespace,
    machineName: cr.spec?.machineRef?.name ?? '',
    byIDPath: cr.spec?.byIDPath ?? '',
    serial: cr.spec?.serial ?? '',
    destination: cr.spec?.destination ?? '',
    phase: cr.status?.phase ?? 'Pending',
    message: cr.status?.message ?? '',
  }
}

/** Reads FrameDiskClaim objects — namespaced, mirroring `MachineClient`/`InstallClient` above. */
class DiskClaimClient {
  constructor(private readonly ns?: string) {}

  async list(): Promise<DiskClaim[]> {
    const res = await k8sFetch<ListResponse<FrameDiskClaimCR>>(diskClaimsPath(this.ns))
    return (res.items ?? []).map(crToDiskClaim).sort((a, b) => a.name.localeCompare(b.name))
  }

  /** For `useLiveResource`, so the console watches the same collection it reads. */
  watchPath(): string {
    return diskClaimsPath(this.ns)
  }
}

/** A FrameUser CR as the apiserver returns it — the shape `src/lib/accounts.ts` reshapes into `Account`. */
export interface FrameUserCR {
  metadata: { name: string }
  spec: { email: string; role: string; state?: string }
}

/**
 * Reads and role/state writes for FrameUser accounts.
 *
 * Exists so the Accounts screen goes through `k8sFetch` like every other
 * screen, instead of a bare `fetch` reading `window.__FRAME_TOKEN__` once and
 * giving up on a 401. `k8sFetch` forces a fresh token and retries exactly
 * once on 401 (see its comment above) — the case that matters here is a role
 * change or a revoke landing while the cached token still looks fresh: a bare
 * `fetch` repeats the same rejected token until the tab is reloaded, and this
 * doesn't. `email` on the write methods is not sent anywhere; it only phrases
 * `X-Frame-Action` so the Tasks screen records *what* changed
 * ("set bob@example.com to admin"), not just *that* something did
 * ("patch frameusers/bob" — the same string for a promotion and a demotion,
 * which is not an audit trail worth having for the most privileged writes in
 * the product).
 */
class UserClient {
  /**
   * Where FrameUser accounts live — pinned, not read from `frameNs()`.
   *
   * Every other Frame CR is namespaced by `config().frameNamespace`, which an
   * operator sets from the Settings screen. FrameUsers are not negotiable that
   * way: they exist only where authd runs. authd takes its namespace from
   * `fieldRef: metadata.namespace` (`cmd/authd/main.go`), the kustomize base
   * puts it in `cluster-control` (`deploy/kubernetes/authd/deployment.yaml`),
   * and its Role is namespaced there — so it *cannot* create a FrameUser
   * anywhere else. Building these paths from the configurable namespace meant
   * the shipped default (`default`, since the shipped ConfigMap carries no
   * `data` and nothing sets `frameNamespace`) pointed the Accounts screen at
   * an empty collection: the list rendered blank with no error and both
   * PATCHes 404'd, which made promotion — the only documented way to create a
   * second admin — impossible on a fresh deploy.
   *
   * `CONFIG_NAMESPACE` is reused rather than a second literal: it already
   * means "where the Frame control plane itself lives" (it is where the UI's
   * own ConfigMap is read from), and authd is deployed alongside it in the
   * same kustomize base. One value, one place to change it if that base ever
   * moves.
   */
  private static readonly ns = CONFIG_NAMESPACE

  async list(): Promise<FrameUserCR[]> {
    const res = await k8sFetch<ListResponse<FrameUserCR>>(frameListPath('frameusers', UserClient.ns))
    return res.items ?? []
  }

  async setRole(name: string, email: string, role: string): Promise<void> {
    await k8sFetch<undefined>(`${frameListPath('frameusers', UserClient.ns)}/${name}`, {
      action: `set ${email} to ${role}`,
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec: { role } },
    })
  }

  async setState(name: string, email: string, state: string): Promise<void> {
    await k8sFetch<undefined>(`${frameListPath('frameusers', UserClient.ns)}/${name}`, {
      action: `${state === 'disabled' ? 'disable' : 'enable'} ${email}`,
      method: 'PATCH',
      contentType: 'application/merge-patch+json',
      body: { spec: { state } },
    })
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
  public readonly talos: TalosClient
  public readonly users: UserClient
  public readonly workloads: WorkloadClient
  public readonly machines: MachineClient
  public readonly installs: InstallClient
  public readonly storage: StorageClient
  public readonly diskClaims: DiskClaimClient

  constructor(opts: FrameClientOptions = {}) {
    this.nodes     = new NodeClient(opts.namespace)
    this.jobs      = new JobClient(opts.namespace)
    this.scheduler = new SchedulerClient(opts.namespace)
    this.resources = new ResourceClient(opts.namespace)
    this.apps      = new ApplicationClient()
    this.cluster   = new ClusterClient()
    this.talos     = new TalosClient(opts.namespace)
    this.users     = new UserClient()
    this.workloads = new WorkloadClient()
    this.machines  = new MachineClient(opts.namespace)
    this.installs  = new InstallClient(opts.namespace)
    this.storage   = new StorageClient()
    this.diskClaims = new DiskClaimClient(opts.namespace)
  }

  async health(): Promise<HealthStatus> {
    try {
      await k8sFetch<unknown>(`/apis/${GROUP}/${VERSION}/`)
      return { status: 'ok', version: VERSION, uptime: 0 }
    } catch {
      return { status: 'degraded', version: VERSION, uptime: 0 }
    }
  }

  /** Who did what through the UI proxy, and how each write ended. */
  tasks = {
    list: async (limit = 200): Promise<TaskRecord[]> => {
      const res = await k8sFetch<{ items: FrameTaskCR[] }>(`${taskListPath()}?limit=${limit}`)
      return res.items
        .map(crToTask)
        .sort((a, b) => (b.startedAt ?? '').localeCompare(a.startedAt ?? ''))
    },
  }
}

export function createFrameClient(opts: FrameClientOptions = {}): FrameClient {
  return new FrameClient(opts)
}

/** Module-private mappers, exposed for unit tests only. Not part of the SDK. */
export const __testing = { crToJob, crToNode, crToPolicy, crToQuota, crToTask }
