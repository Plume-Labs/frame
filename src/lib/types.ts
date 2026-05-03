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
