export type NodeStatus = 'online' | 'degraded' | 'offline' | 'provisioning'

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
}

export interface StorageInfo {
  cephOSDs: number
  cephPGs: number
  totalCapacity: number
  usedCapacity: number
  readIOPS: number
  writeIOPS: number
  replicationFactor: number
}

export interface HardwareInfo {
  cpuModel: string
  cpuCores: number
  memoryGB: number
  storageGB: number
  networkAdapters: number
  pxeBooted: boolean
  temperature: number
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
}

export type EventSeverity = 'info' | 'warning' | 'error' | 'success'

export interface SystemEvent {
  id: string
  timestamp: number
  severity: EventSeverity
  message: string
  nodeId?: string
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
