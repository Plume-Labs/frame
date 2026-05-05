export type NodeStatus = 'online' | 'degraded' | 'offline' | 'provisioning'

// ── Service classes (mainframe QoS) ─────────────────────────────────────────
export type ServiceClass = 'HIGH' | 'MEDIUM' | 'LOW'

// ── Scheduler types ──────────────────────────────────────────────────────────
export type SchedulerType = 'default' | 'volcano' | 'yunikorn'
export type WorkloadPriority = 'critical' | 'high' | 'medium' | 'low'

export interface PodGroupStatus {
  name: string
  queue: string
  minMember: number
  running: number
  pending: number
  phase: 'Pending' | 'Running' | 'Completed' | 'Failed' | 'Unknown'
  gpusRequested: number
}

// ── Job orchestration ────────────────────────────────────────────────────────
export type JobStatus = 'queued' | 'running' | 'checkpointed' | 'completed' | 'failed'

export interface CheckpointInfo {
  id: string
  timestamp: number
  size: number
  storageLocation: string
  step?: number
}

export interface DAGNode {
  id: string
  name: string
  status: JobStatus
  template: string
  dependencies: string[]
  startTime?: number
  endTime?: number
  retries: number
  gpuCount: number
}

export interface Job {
  id: string
  name: string
  pipeline: string
  status: JobStatus
  serviceClass: ServiceClass
  priority: WorkloadPriority
  queue: string
  createdAt: number
  startedAt?: number
  completedAt?: number
  nodes: DAGNode[]
  checkpoints: CheckpointInfo[]
  retryCount: number
  maxRetries: number
  traceId?: string
  namespace: string
}

// ── Resilience ───────────────────────────────────────────────────────────────
export interface SnapshotInfo {
  id: string
  name: string
  namespace: string
  timestamp: number
  sizeGB: number
  type: 'full' | 'incremental'
  status: 'pending' | 'completed' | 'failed'
  ttlHours: number
}

export interface CheckpointStatus {
  nodeId: string
  lastCheckpointAt: number
  nextCheckpointAt: number
  checkpointCount: number
  storageUsedGB: number
  healthy: boolean
}

// ── GPU metrics ───────────────────────────────────────────────────────────────
export interface GPUMetrics {
  gpuIndex: number
  model: string
  utilizationPercent: number
  memoryUsedGB: number
  memoryTotalGB: number
  temperatureC: number
  powerWatts: number
  nvlinkBandwidthGBps: number
  smOccupancyPercent: number
  eccErrors: number
  migEnabled: boolean
  migInstances: number
}

// ── Pipeline tracing ──────────────────────────────────────────────────────────
export interface PipelineSpan {
  spanId: string
  operationName: string
  startTime: number
  durationMs: number
  status: 'ok' | 'error'
  tags: Record<string, string>
}

export interface PipelineTrace {
  traceId: string
  pipelineName: string
  startTime: number
  totalDurationMs: number
  spans: PipelineSpan[]
  serviceClass: ServiceClass
}

export interface NodeMetrics {
  cpu: number
  memory: number
  storage: number
  network: number
}

export interface NetworkInfo {
  rxBytes: number
  txBytes: number
  latency: number
  rdmaActive: boolean
  rdmaQueuePairs: number
  bandwidth: number
  packetLoss: number
  // HPC networking extensions
  sriovVFs: number
  dpdkEnabled: boolean
  ciliumVersion: string
  ebpfBypassActive: boolean
}

export interface StorageInfo {
  cephOSDs: number
  cephPGs: number
  totalCapacity: number
  usedCapacity: number
  readIOPS: number
  writeIOPS: number
  replicationFactor: number
  // Data fabric extensions
  dataFabricEnabled: boolean
  metadataEntries: number
  activeDatasets: number
}

export type DeviceType = 'server' | 'storage' | 'network' | 'pdu' | 'ups' | 'blank'

export interface HardwareInfo {
  cpuModel: string
  cpuCores: number
  memoryGB: number
  storageGB: number
  networkAdapters: number
  pxeBooted: boolean
  temperature: number
  deviceType: DeviceType
  rackUnits: number
  // NUMA + caching extensions
  numaNode: number
  cacheHitRate: number
  storageTier: 'ram' | 'nvme' | 'object'
  // GPU + resource isolation extensions
  gpuMIGInstances: number
  hugepagesGB: number
  cpuPinnedCores: number
  topologyManagerPolicy: 'none' | 'best-effort' | 'restricted' | 'single-numa-node'
}

export interface ClusterNode {
  id: string
  name: string
  status: NodeStatus
  metrics: NodeMetrics
  uptime: number
  lastSeen: number
  network: NetworkInfo
  storage: StorageInfo
  hardware: HardwareInfo
  zone: string
  rackId: string
  rackPosition: number
  // Service class + scheduling
  serviceClass: ServiceClass
  gpuMetrics?: GPUMetrics[]
}

export type EventSeverity = 'info' | 'warning' | 'error' | 'success'

export interface SystemEvent {
  id: string
  timestamp: number
  severity: EventSeverity
  message: string
  nodeId?: string
  serviceClass?: ServiceClass
}

export interface ClusterStats {
  totalNodes: number
  onlineNodes: number
  degradedNodes: number
  offlineNodes: number
  totalCpu: number
  usedCpu: number
  totalMemory: number
  usedMemory: number
  totalStorage: number
  usedStorage: number
  networkThroughput: number
}

export interface ResourceDataPoint {
  timestamp: number
  cpu: number
  memory: number
  storage: number
  network: number
}

export interface ForecastPoint {
  timestamp: number
  predicted: number
  confidence: {
    lower: number
    upper: number
  }
}

export interface ResourceForecast {
  cpu: ForecastPoint[]
  memory: ForecastPoint[]
  storage: ForecastPoint[]
  network: ForecastPoint[]
}

export interface CapacityAlert {
  id: string
  resource: 'cpu' | 'memory' | 'storage' | 'network'
  severity: 'warning' | 'critical'
  message: string
  estimatedDaysUntilFull: number
  currentUsage: number
  projectedUsage: number
  recommendation: string
}

export interface CapacityPlan {
  currentCapacity: {
    cpu: number
    memory: number
    storage: number
  }
  recommendedCapacity: {
    cpu: number
    memory: number
    storage: number
  }
  growthRate: {
    cpu: number
    memory: number
    storage: number
  }
  nodesRequired: number
  estimatedCost: number
  timeframe: string
}

export type TimeframeOption = '1h' | '6h' | '24h' | '7d' | '30d' | 'all'

export interface TrendStatistics {
  resource: 'cpu' | 'memory' | 'storage' | 'network'
  timeframe: TimeframeOption
  current: number
  average: number
  min: number
  max: number
  median: number
  stdDeviation: number
  trend: 'increasing' | 'decreasing' | 'stable'
  trendPercentage: number
  peakTime: number
  peakValue: number
  lowTime: number
  lowValue: number
}

export interface HistoricalTrendData {
  timeframe: TimeframeOption
  data: ResourceDataPoint[]
  statistics: TrendStatistics[]
}

export type AnomalyType = 'spike' | 'drop' | 'pattern_deviation' | 'sustained_high' | 'oscillation'

export interface Anomaly {
  id: string
  timestamp: number
  type: AnomalyType
  resource: 'cpu' | 'memory' | 'storage' | 'network'
  severity: 'low' | 'medium' | 'high' | 'critical'
  value: number
  expectedValue: number
  deviation: number
  confidence: number
  description: string
  recommendation: string
  nodeId?: string
  duration?: number
}

export interface AnomalyPattern {
  resource: 'cpu' | 'memory' | 'storage' | 'network'
  baseline: number
  baselineStdDev: number
  movingAverage: number[]
  recentTrend: 'increasing' | 'decreasing' | 'stable'
  lastUpdated: number
}

export interface PowerMetrics {
  currentDraw: number
  maxCapacity: number
  efficiency: number
  powerUsageEffectiveness: number
  peakDraw: number
  averageDraw: number
}

export interface CoolingMetrics {
  inletTemp: number
  outletTemp: number
  ambientTemp: number
  fanSpeed: number
  airflowCFM: number
  deltaT: number
  coolingEfficiency: number
}

export interface RackPowerCooling {
  rackId: string
  power: PowerMetrics
  cooling: CoolingMetrics
  thermalLoad: number
  powerDensity: number
  alerts: PowerCoolingAlert[]
}

export interface PowerCoolingAlert {
  id: string
  type: 'power' | 'cooling' | 'thermal'
  severity: 'info' | 'warning' | 'critical'
  message: string
  timestamp: number
  value: number
  threshold: number
}

// ── Distributed KV-Cache RDMA ─────────────────────────────────────────────────

export interface KVCacheNodeStats {
  nodeId: string
  nodeName: string
  /** Fraction of token lookups satisfied from local VRAM KV cache (0–1) */
  localHitRate: number
  /** Fraction of token lookups satisfied via RDMA from a remote node (0–1) */
  rdmaHitRate: number
  /** RDMA read bandwidth currently consumed (GB/s) */
  rdmaBandwidthGBps: number
  /** KV cache capacity used (GB) */
  usedCapacityGB: number
  /** Total KV cache capacity (GB) */
  totalCapacityGB: number
  /** Number of active RDMA queue pairs to peer nodes */
  activeQueuePairs: number
  /** Latency of RDMA KV-cache reads (µs) */
  rdmaLatencyUs: number
}

export type VLLMKVTransferBackend = 'mooncake' | 'lmcache' | 'nixl'

export interface KVCacheClusterStats {
  backend: VLLMKVTransferBackend
  nodes: KVCacheNodeStats[]
  /** Total tokens evicted cluster-wide in the last minute */
  evictionsPerMinute: number
  /** Overall prefill time reduction vs. no KV transfer (%) */
  prefillSavingsPct: number
}

// ── Elastic LPAR Pools ────────────────────────────────────────────────────────

export type ElasticPoolState = 'stable' | 'expanding' | 'contracting' | 'draining'

export interface ElasticPoolQuota {
  cpuMin: number
  cpuMax: number
  memoryMinGi: number
  memoryMaxGi: number
  gpuMin: number
  gpuMax: number
}

export interface ElasticPool {
  name: string
  /** Volcano Queue name backing this pool */
  volcanoQueue: string
  serviceClass: ServiceClass
  state: ElasticPoolState
  quota: ElasticPoolQuota
  /** Current allocations */
  allocatedCPU: number
  allocatedMemoryGi: number
  allocatedGPU: number
  /** Number of workload pods in this pool */
  podCount: number
  /** Queue weight for DRF scheduling */
  weight: number
  /** Whether idle resources can be reclaimed by other pools */
  reclaimable: boolean
}

// ── Speculative Decoding ──────────────────────────────────────────────────────

export type SpecDecodeStrategy = 'draft-model' | 'medusa' | 'eagle'

export interface SpecDecodeRouteStats {
  /** Endpoint path */
  path: string
  /** Fraction of requests routed to speculative path (0–1) */
  speculativeFraction: number
  /** Mean latency on speculative path (ms) */
  speculativeLatencyMs: number
  /** Mean latency on baseline path (ms) */
  baselineLatencyMs: number
}

export interface SpecDecodeMetrics {
  strategy: SpecDecodeStrategy
  draftModel: string
  targetModel: string
  /** Mean number of speculative tokens proposed per step */
  meanDraftTokens: number
  /** Fraction of draft tokens accepted by the target model (0–1) */
  tokenAcceptanceRate: number
  /** Effective throughput speedup vs. no speculation */
  throughputSpeedup: number
  /** Total speculative decode requests served */
  totalRequests: number
  /** Requests using speculative path right now */
  activeSpecRequests: number
  routes: SpecDecodeRouteStats[]
}

