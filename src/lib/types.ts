export type NodeStatus = 'online' | 'degraded' | 'offline' | 'provisioning'

export interface NodeMetrics {
  cpu: number
  memory: number
  storage: number
  network: number
}

export interface ClusterNode {
  id: string
  name: string
  status: NodeStatus
  metrics: NodeMetrics
  uptime: number
  lastSeen: number
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
