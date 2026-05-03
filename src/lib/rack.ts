import { ClusterNode } from './types'

export interface RackData {
  id: string
  zone: string
  nodes: ClusterNode[]
  totalCapacity: number
  usedCapacity: number
  healthScore: number
}

export function organizeNodesByRack(nodes: ClusterNode[]): Map<string, RackData> {
  const racksMap = new Map<string, RackData>()

  nodes.forEach((node) => {
    if (!racksMap.has(node.rackId)) {
      racksMap.set(node.rackId, {
        id: node.rackId,
        zone: node.zone,
        nodes: [],
        totalCapacity: 0,
        usedCapacity: 0,
        healthScore: 0
      })
    }

    const rack = racksMap.get(node.rackId)!
    rack.nodes.push(node)
  })

  racksMap.forEach((rack) => {
    rack.nodes.sort((a, b) => a.rackPosition - b.rackPosition)
    
    const onlineNodes = rack.nodes.filter(n => n.status === 'online').length
    const totalNodes = rack.nodes.length
    rack.healthScore = totalNodes > 0 ? (onlineNodes / totalNodes) * 100 : 0
    
    rack.totalCapacity = rack.nodes.reduce((sum, node) => sum + node.hardware.storageGB, 0)
    rack.usedCapacity = rack.nodes.reduce((sum, node) => {
      const usagePercent = node.metrics.storage / 100
      return sum + (node.hardware.storageGB * usagePercent)
    }, 0)
  })

  return racksMap
}

export function organizeRacksByZone(racks: Map<string, RackData>): Map<string, RackData[]> {
  const zoneMap = new Map<string, RackData[]>()

  racks.forEach((rack) => {
    if (!zoneMap.has(rack.zone)) {
      zoneMap.set(rack.zone, [])
    }
    zoneMap.get(rack.zone)!.push(rack)
  })

  zoneMap.forEach((rackList) => {
    rackList.sort((a, b) => a.id.localeCompare(b.id))
  })

  return zoneMap
}
