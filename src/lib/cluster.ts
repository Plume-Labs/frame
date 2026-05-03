import { ClusterNode, NodeStatus, NodeMetrics, SystemEvent, EventSeverity, ClusterStats } from './types'

const NODE_NAMES = [
  'alpha', 'beta', 'gamma', 'delta', 'epsilon', 'zeta', 'eta', 'theta',
  'iota', 'kappa', 'lambda', 'mu', 'nu', 'xi', 'omicron', 'pi',
  'rho', 'sigma', 'tau', 'upsilon', 'phi', 'chi', 'psi', 'omega',
  'nebula', 'pulsar', 'quasar', 'vega', 'sirius', 'rigel', 'deneb', 'altair'
]

function randomInRange(min: number, max: number): number {
  return Math.random() * (max - min) + min
}

function generateNodeId(index: number): string {
  return `node-${String(index + 1).padStart(3, '0')}`
}

function generateNodeName(index: number): string {
  return NODE_NAMES[index % NODE_NAMES.length]
}

function generateRandomStatus(): NodeStatus {
  const rand = Math.random()
  if (rand < 0.70) return 'online'
  if (rand < 0.85) return 'degraded'
  if (rand < 0.95) return 'provisioning'
  return 'offline'
}

function generateMetrics(): NodeMetrics {
  return {
    cpu: randomInRange(10, 95),
    memory: randomInRange(20, 90),
    storage: randomInRange(30, 85),
    network: randomInRange(100, 10000)
  }
}

export function generateClusterNodes(count: number = 32): ClusterNode[] {
  const nodes: ClusterNode[] = []
  const now = Date.now()

  for (let i = 0; i < count; i++) {
    const status = generateRandomStatus()
    nodes.push({
      id: generateNodeId(i),
      name: generateNodeName(i),
      status,
      metrics: generateMetrics(),
      uptime: status === 'offline' ? 0 : randomInRange(3600000, 7776000000),
      lastSeen: status === 'offline' ? now - randomInRange(60000, 600000) : now
    })
  }

  return nodes
}

export function updateNodeMetrics(node: ClusterNode): ClusterNode {
  if (node.status === 'offline') {
    return node
  }

  const delta = 5

  return {
    ...node,
    metrics: {
      cpu: Math.max(0, Math.min(100, node.metrics.cpu + randomInRange(-delta, delta))),
      memory: Math.max(0, Math.min(100, node.metrics.memory + randomInRange(-delta, delta))),
      storage: Math.max(0, Math.min(100, node.metrics.storage + randomInRange(-delta * 0.5, delta * 0.5))),
      network: Math.max(0, node.metrics.network + randomInRange(-500, 500))
    },
    uptime: node.uptime + 2000,
    lastSeen: Date.now()
  }
}

export function simulateStatusChange(node: ClusterNode): ClusterNode {
  const rand = Math.random()
  
  if (rand > 0.98) {
    const statuses: NodeStatus[] = ['online', 'degraded', 'offline', 'provisioning']
    const newStatus = statuses[Math.floor(Math.random() * statuses.length)]
    
    return {
      ...node,
      status: newStatus,
      uptime: newStatus === 'offline' ? 0 : node.uptime,
      lastSeen: Date.now()
    }
  }

  return node
}

export function calculateClusterStats(nodes: ClusterNode[]): ClusterStats {
  const onlineNodes = nodes.filter(n => n.status === 'online').length
  const degradedNodes = nodes.filter(n => n.status === 'degraded').length
  const offlineNodes = nodes.filter(n => n.status === 'offline').length

  const activeNodes = nodes.filter(n => n.status !== 'offline')
  
  const totalCpu = nodes.length * 100
  const usedCpu = activeNodes.reduce((sum, n) => sum + n.metrics.cpu, 0)
  
  const totalMemory = nodes.length * 256
  const usedMemory = activeNodes.reduce((sum, n) => sum + (n.metrics.memory / 100 * 256), 0)
  
  const totalStorage = nodes.length * 2048
  const usedStorage = activeNodes.reduce((sum, n) => sum + (n.metrics.storage / 100 * 2048), 0)
  
  const networkThroughput = activeNodes.reduce((sum, n) => sum + n.metrics.network, 0)

  return {
    totalNodes: nodes.length,
    onlineNodes,
    degradedNodes,
    offlineNodes,
    totalCpu,
    usedCpu,
    totalMemory,
    usedMemory,
    totalStorage,
    usedStorage,
    networkThroughput
  }
}

const EVENT_TEMPLATES = {
  online: ['Node {node} joined cluster', 'Node {node} status: operational', 'Node {node} health check passed'],
  degraded: ['Node {node} performance degraded', 'Node {node} experiencing issues', 'Node {node} health check warning'],
  offline: ['Node {node} disconnected', 'Node {node} unresponsive', 'Lost connection to node {node}'],
  provisioning: ['Provisioning node {node}', 'Node {node} initializing', 'Bootstrapping node {node}'],
  info: ['Cluster rebalancing', 'Backup completed', 'Health check completed', 'Network optimization complete'],
  warning: ['High resource usage detected', 'Network latency spike', 'Storage threshold warning'],
  error: ['Failed to allocate resources', 'Connection timeout', 'Health check failed']
}

function getRandomTemplate(templates: string[]): string {
  return templates[Math.floor(Math.random() * templates.length)]
}

let eventIdCounter = 0

export function generateSystemEvent(
  nodes: ClusterNode[],
  previousNodes?: ClusterNode[]
): SystemEvent | null {
  if (Math.random() > 0.3) return null

  if (previousNodes) {
    for (let i = 0; i < nodes.length; i++) {
      if (nodes[i].status !== previousNodes[i].status) {
        const node = nodes[i]
        const templates = EVENT_TEMPLATES[node.status]
        const message = getRandomTemplate(templates).replace('{node}', node.name)
        
        let severity: EventSeverity = 'info'
        if (node.status === 'offline') severity = 'error'
        else if (node.status === 'degraded') severity = 'warning'
        else if (node.status === 'online') severity = 'success'
        
        return {
          id: `event-${eventIdCounter++}`,
          timestamp: Date.now(),
          severity,
          message,
          nodeId: node.id
        }
      }
    }
  }

  const rand = Math.random()
  let severity: EventSeverity
  let templates: string[]

  if (rand < 0.6) {
    severity = 'info'
    templates = EVENT_TEMPLATES.info
  } else if (rand < 0.85) {
    severity = 'warning'
    templates = EVENT_TEMPLATES.warning
  } else {
    severity = 'error'
    templates = EVENT_TEMPLATES.error
  }

  return {
    id: `event-${eventIdCounter++}`,
    timestamp: Date.now(),
    severity,
    message: getRandomTemplate(templates)
  }
}

export function formatUptime(ms: number): string {
  const seconds = Math.floor(ms / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (days > 0) return `${days}d ${hours % 24}h`
  if (hours > 0) return `${hours}h ${minutes % 60}m`
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`
  return `${seconds}s`
}

export function formatBytes(gb: number): string {
  if (gb >= 1024) return `${(gb / 1024).toFixed(1)} TB`
  return `${gb.toFixed(1)} GB`
}

export function formatBandwidth(mbps: number): string {
  if (mbps >= 1000) return `${(mbps / 1000).toFixed(2)} Gbps`
  return `${mbps.toFixed(0)} Mbps`
}
