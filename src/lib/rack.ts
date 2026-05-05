import { ClusterNode, RackPowerCooling, PowerMetrics, CoolingMetrics, PowerCoolingAlert } from './types'

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

export function generateRackPowerMetrics(rack: RackData): PowerMetrics {
  const onlineNodes = rack.nodes.filter(n => n.status === 'online').length
  const totalNodes = rack.nodes.length
  
  const avgCpuUsage = rack.nodes.reduce((sum, n) => sum + n.metrics.cpu, 0) / totalNodes
  const avgMemUsage = rack.nodes.reduce((sum, n) => sum + n.metrics.memory, 0) / totalNodes
  
  const typicalNodePower = 350
  const maxNodePower = 500
  const basePower = 200
  
  const utilizationFactor = (avgCpuUsage + avgMemUsage) / 200
  const currentDraw = basePower + (onlineNodes * typicalNodePower * (0.5 + utilizationFactor * 0.5))
  const maxCapacity = basePower + (totalNodes * maxNodePower)
  const peakDraw = currentDraw * (1 + Math.random() * 0.15)
  const averageDraw = currentDraw * 0.9
  
  const efficiency = Math.max(0.85, Math.min(0.98, 0.92 + (Math.random() - 0.5) * 0.08))
  const powerUsageEffectiveness = 1 + (1 - efficiency) * 2
  
  return {
    currentDraw: Math.round(currentDraw),
    maxCapacity: Math.round(maxCapacity),
    efficiency: parseFloat(efficiency.toFixed(3)),
    powerUsageEffectiveness: parseFloat(powerUsageEffectiveness.toFixed(3)),
    peakDraw: Math.round(peakDraw),
    averageDraw: Math.round(averageDraw)
  }
}

export function generateRackCoolingMetrics(rack: RackData, powerMetrics: PowerMetrics): CoolingMetrics {
  const avgTemp = rack.nodes.reduce((sum, n) => sum + n.hardware.temperature, 0) / rack.nodes.length
  const maxTemp = Math.max(...rack.nodes.map(n => n.hardware.temperature))
  
  const ambientTemp = 22 + Math.random() * 3
  const inletTemp = ambientTemp + Math.random() * 2
  const outletTemp = inletTemp + (maxTemp - inletTemp) * 0.6
  
  const utilizationFactor = powerMetrics.currentDraw / powerMetrics.maxCapacity
  const baseFanSpeed = 40
  const fanSpeed = Math.min(100, baseFanSpeed + utilizationFactor * 50 + (avgTemp - 50) * 0.5)
  
  const cfmPerNode = 120
  const airflowCFM = rack.nodes.length * cfmPerNode * (fanSpeed / 100)
  
  const deltaT = outletTemp - inletTemp
  const idealDeltaT = 10
  const coolingEfficiency = Math.max(0, Math.min(1, 1 - Math.abs(deltaT - idealDeltaT) / idealDeltaT))
  
  return {
    inletTemp: parseFloat(inletTemp.toFixed(1)),
    outletTemp: parseFloat(outletTemp.toFixed(1)),
    ambientTemp: parseFloat(ambientTemp.toFixed(1)),
    fanSpeed: parseFloat(fanSpeed.toFixed(1)),
    airflowCFM: Math.round(airflowCFM),
    deltaT: parseFloat(deltaT.toFixed(1)),
    coolingEfficiency: parseFloat(coolingEfficiency.toFixed(3))
  }
}

export function generatePowerCoolingAlerts(
  rackId: string,
  power: PowerMetrics,
  cooling: CoolingMetrics,
  nodes: ClusterNode[]
): PowerCoolingAlert[] {
  const alerts: PowerCoolingAlert[] = []
  
  const powerUtilization = (power.currentDraw / power.maxCapacity) * 100
  if (powerUtilization > 85) {
    alerts.push({
      id: `${rackId}-power-critical-${Date.now()}`,
      type: 'power',
      severity: 'critical',
      message: `Power utilization at ${powerUtilization.toFixed(1)}% - approaching capacity`,
      timestamp: Date.now(),
      value: powerUtilization,
      threshold: 85
    })
  } else if (powerUtilization > 75) {
    alerts.push({
      id: `${rackId}-power-warning-${Date.now()}`,
      type: 'power',
      severity: 'warning',
      message: `Power utilization at ${powerUtilization.toFixed(1)}% - consider load balancing`,
      timestamp: Date.now(),
      value: powerUtilization,
      threshold: 75
    })
  }
  
  if (power.powerUsageEffectiveness > 1.5) {
    alerts.push({
      id: `${rackId}-pue-warning-${Date.now()}`,
      type: 'power',
      severity: 'warning',
      message: `Poor PUE of ${power.powerUsageEffectiveness.toFixed(2)} - efficiency concerns`,
      timestamp: Date.now(),
      value: power.powerUsageEffectiveness,
      threshold: 1.5
    })
  }
  
  if (cooling.outletTemp > 35) {
    alerts.push({
      id: `${rackId}-cooling-critical-${Date.now()}`,
      type: 'cooling',
      severity: 'critical',
      message: `Outlet temperature ${cooling.outletTemp.toFixed(1)}°C exceeds safe threshold`,
      timestamp: Date.now(),
      value: cooling.outletTemp,
      threshold: 35
    })
  } else if (cooling.outletTemp > 32) {
    alerts.push({
      id: `${rackId}-cooling-warning-${Date.now()}`,
      type: 'cooling',
      severity: 'warning',
      message: `Outlet temperature ${cooling.outletTemp.toFixed(1)}°C approaching limit`,
      timestamp: Date.now(),
      value: cooling.outletTemp,
      threshold: 32
    })
  }
  
  if (cooling.fanSpeed > 90) {
    alerts.push({
      id: `${rackId}-fan-warning-${Date.now()}`,
      type: 'cooling',
      severity: 'warning',
      message: `Fan speed at ${cooling.fanSpeed.toFixed(1)}% - cooling system under stress`,
      timestamp: Date.now(),
      value: cooling.fanSpeed,
      threshold: 90
    })
  }
  
  const hotNodes = nodes.filter(n => n.hardware.temperature > 75)
  if (hotNodes.length > 0) {
    alerts.push({
      id: `${rackId}-thermal-warning-${Date.now()}`,
      type: 'thermal',
      severity: hotNodes.some(n => n.hardware.temperature > 80) ? 'critical' : 'warning',
      message: `${hotNodes.length} node(s) running hot - check airflow`,
      timestamp: Date.now(),
      value: Math.max(...hotNodes.map(n => n.hardware.temperature)),
      threshold: 75
    })
  }
  
  if (cooling.deltaT > 15) {
    alerts.push({
      id: `${rackId}-deltat-warning-${Date.now()}`,
      type: 'thermal',
      severity: 'warning',
      message: `High temperature delta (${cooling.deltaT.toFixed(1)}°C) - airflow restriction possible`,
      timestamp: Date.now(),
      value: cooling.deltaT,
      threshold: 15
    })
  }
  
  return alerts
}

export function calculateRackPowerCooling(rack: RackData): RackPowerCooling {
  const power = generateRackPowerMetrics(rack)
  const cooling = generateRackCoolingMetrics(rack, power)
  const alerts = generatePowerCoolingAlerts(rack.id, power, cooling, rack.nodes)
  
  const thermalLoad = power.currentDraw * 3.412
  
  const rackSpaceUsed = rack.nodes.length
  const powerDensity = rackSpaceUsed > 0 ? power.currentDraw / rackSpaceUsed : 0
  
  return {
    rackId: rack.id,
    power,
    cooling,
    thermalLoad: Math.round(thermalLoad),
    powerDensity: Math.round(powerDensity),
    alerts
  }
}

export function updateRackPowerCooling(
  existingMetrics: RackPowerCooling,
  rack: RackData
): RackPowerCooling {
  const newMetrics = calculateRackPowerCooling(rack)
  
  const smoothingFactor = 0.3
  
  return {
    rackId: rack.id,
    power: {
      currentDraw: Math.round(
        existingMetrics.power.currentDraw * (1 - smoothingFactor) +
        newMetrics.power.currentDraw * smoothingFactor
      ),
      maxCapacity: newMetrics.power.maxCapacity,
      efficiency: parseFloat((
        existingMetrics.power.efficiency * (1 - smoothingFactor) +
        newMetrics.power.efficiency * smoothingFactor
      ).toFixed(3)),
      powerUsageEffectiveness: parseFloat((
        existingMetrics.power.powerUsageEffectiveness * (1 - smoothingFactor) +
        newMetrics.power.powerUsageEffectiveness * smoothingFactor
      ).toFixed(3)),
      peakDraw: Math.max(existingMetrics.power.peakDraw, newMetrics.power.currentDraw),
      averageDraw: Math.round(
        existingMetrics.power.averageDraw * 0.9 +
        newMetrics.power.currentDraw * 0.1
      )
    },
    cooling: {
      inletTemp: parseFloat((
        existingMetrics.cooling.inletTemp * (1 - smoothingFactor) +
        newMetrics.cooling.inletTemp * smoothingFactor
      ).toFixed(1)),
      outletTemp: parseFloat((
        existingMetrics.cooling.outletTemp * (1 - smoothingFactor) +
        newMetrics.cooling.outletTemp * smoothingFactor
      ).toFixed(1)),
      ambientTemp: parseFloat((
        existingMetrics.cooling.ambientTemp * (1 - smoothingFactor) +
        newMetrics.cooling.ambientTemp * smoothingFactor
      ).toFixed(1)),
      fanSpeed: parseFloat((
        existingMetrics.cooling.fanSpeed * (1 - smoothingFactor) +
        newMetrics.cooling.fanSpeed * smoothingFactor
      ).toFixed(1)),
      airflowCFM: Math.round(
        existingMetrics.cooling.airflowCFM * (1 - smoothingFactor) +
        newMetrics.cooling.airflowCFM * smoothingFactor
      ),
      deltaT: parseFloat((
        existingMetrics.cooling.deltaT * (1 - smoothingFactor) +
        newMetrics.cooling.deltaT * smoothingFactor
      ).toFixed(1)),
      coolingEfficiency: parseFloat((
        existingMetrics.cooling.coolingEfficiency * (1 - smoothingFactor) +
        newMetrics.cooling.coolingEfficiency * smoothingFactor
      ).toFixed(3))
    },
    thermalLoad: newMetrics.thermalLoad,
    powerDensity: newMetrics.powerDensity,
    alerts: newMetrics.alerts
  }
}
