import {
  ResourceDataPoint,
  ForecastPoint,
  ResourceForecast,
  CapacityAlert,
  CapacityPlan,
  ClusterNode,
  ClusterStats
} from './types'

function linearRegression(data: number[]): { slope: number; intercept: number } {
  const n = data.length
  const indices = Array.from({ length: n }, (_, i) => i)
  
  const sumX = indices.reduce((a, b) => a + b, 0)
  const sumY = data.reduce((a, b) => a + b, 0)
  const sumXY = indices.reduce((sum, x, i) => sum + x * data[i], 0)
  const sumX2 = indices.reduce((sum, x) => sum + x * x, 0)
  
  const slope = (n * sumXY - sumX * sumY) / (n * sumX2 - sumX * sumX)
  const intercept = (sumY - slope * sumX) / n
  
  return { slope, intercept }
}

function calculateStandardDeviation(data: number[]): number {
  const mean = data.reduce((a, b) => a + b, 0) / data.length
  const squaredDiffs = data.map(x => Math.pow(x - mean, 2))
  const variance = squaredDiffs.reduce((a, b) => a + b, 0) / data.length
  return Math.sqrt(variance)
}

export function generateForecast(
  historicalData: ResourceDataPoint[],
  periodsAhead: number = 12
): ResourceForecast {
  if (historicalData.length < 3) {
    return {
      cpu: [],
      memory: [],
      storage: [],
      network: []
    }
  }

  const cpuData = historicalData.map(d => d.cpu)
  const memoryData = historicalData.map(d => d.memory)
  const storageData = historicalData.map(d => d.storage)
  const networkData = historicalData.map(d => d.network)

  const cpuRegression = linearRegression(cpuData)
  const memoryRegression = linearRegression(memoryData)
  const storageRegression = linearRegression(storageData)
  const networkRegression = linearRegression(networkData)

  const cpuStdDev = calculateStandardDeviation(cpuData)
  const memoryStdDev = calculateStandardDeviation(memoryData)
  const storageStdDev = calculateStandardDeviation(storageData)
  const networkStdDev = calculateStandardDeviation(networkData)

  const lastTimestamp = historicalData[historicalData.length - 1].timestamp
  const timeInterval = historicalData.length > 1
    ? historicalData[historicalData.length - 1].timestamp - historicalData[historicalData.length - 2].timestamp
    : 3600000

  const createForecastPoints = (
    regression: { slope: number; intercept: number },
    stdDev: number,
    currentLength: number,
    clampToPercent = true
  ): ForecastPoint[] => {
    return Array.from({ length: periodsAhead }, (_, i) => {
      const index = currentLength + i
      const rawPredicted = regression.slope * index + regression.intercept
      const predicted = clampToPercent
        ? Math.max(0, Math.min(100, rawPredicted))
        : Math.max(0, rawPredicted)
      const confidenceMargin = stdDev * 1.96 * Math.sqrt(1 + 1 / currentLength)

      return {
        timestamp: lastTimestamp + (i + 1) * timeInterval,
        predicted,
        confidence: {
          lower: Math.max(0, predicted - confidenceMargin),
          upper: clampToPercent
            ? Math.min(100, predicted + confidenceMargin)
            : predicted + confidenceMargin
        }
      }
    })
  }

  return {
    cpu: createForecastPoints(cpuRegression, cpuStdDev, cpuData.length),
    memory: createForecastPoints(memoryRegression, memoryStdDev, memoryData.length),
    storage: createForecastPoints(storageRegression, storageStdDev, storageData.length),
    // Network is in Mbps, not a percentage — do not clamp to [0, 100]
    network: createForecastPoints(networkRegression, networkStdDev, networkData.length, false)
  }
}

export function generateCapacityAlerts(
  forecast: ResourceForecast,
  currentStats: ClusterStats,
  thresholds: { warning: number; critical: number } = { warning: 80, critical: 90 }
): CapacityAlert[] {
  const alerts: CapacityAlert[] = []
  let alertId = 0

  const checkResource = (
    resource: 'cpu' | 'memory' | 'storage' | 'network',
    forecastData: ForecastPoint[],
    currentUsage: number,
    totalCapacity: number
  ) => {
    const currentUsagePercent = (currentUsage / totalCapacity) * 100
    
    const criticalPoint = forecastData.findIndex(point => point.predicted >= thresholds.critical)
    const warningPoint = forecastData.findIndex(point => point.predicted >= thresholds.warning)

    if (criticalPoint !== -1) {
      const daysUntilCritical = Math.round((forecastData[criticalPoint].timestamp - Date.now()) / (1000 * 60 * 60 * 24))
      const projectedUsage = forecastData[criticalPoint].predicted
      
      alerts.push({
        id: `alert-${alertId++}`,
        resource,
        severity: 'critical',
        message: `${resource.toUpperCase()} expected to reach ${thresholds.critical}% capacity in ${daysUntilCritical} days`,
        estimatedDaysUntilFull: daysUntilCritical,
        currentUsage: currentUsagePercent,
        projectedUsage,
        recommendation: `Immediate action required: Add ${Math.ceil((projectedUsage - thresholds.critical) / 10)} additional nodes or optimize ${resource} usage`
      })
    } else if (warningPoint !== -1) {
      const daysUntilWarning = Math.round((forecastData[warningPoint].timestamp - Date.now()) / (1000 * 60 * 60 * 24))
      const projectedUsage = forecastData[warningPoint].predicted
      
      alerts.push({
        id: `alert-${alertId++}`,
        resource,
        severity: 'warning',
        message: `${resource.toUpperCase()} approaching capacity threshold (${thresholds.warning}%) in ${daysUntilWarning} days`,
        estimatedDaysUntilFull: daysUntilWarning,
        currentUsage: currentUsagePercent,
        projectedUsage,
        recommendation: `Plan capacity expansion: Monitor ${resource} usage trends and prepare to add nodes`
      })
    }

    if (currentUsagePercent >= thresholds.critical) {
      alerts.push({
        id: `alert-${alertId++}`,
        resource,
        severity: 'critical',
        message: `${resource.toUpperCase()} currently at critical capacity (${currentUsagePercent.toFixed(1)}%)`,
        estimatedDaysUntilFull: 0,
        currentUsage: currentUsagePercent,
        projectedUsage: currentUsagePercent,
        recommendation: `URGENT: ${resource} at critical levels. Add nodes immediately or reduce workload`
      })
    }
  }

  checkResource('cpu', forecast.cpu, currentStats.usedCpu, currentStats.totalCpu)
  checkResource('memory', forecast.memory, currentStats.usedMemory, currentStats.totalMemory)
  checkResource('storage', forecast.storage, currentStats.usedStorage, currentStats.totalStorage)

  return alerts.sort((a, b) => {
    if (a.severity === 'critical' && b.severity === 'warning') return -1
    if (a.severity === 'warning' && b.severity === 'critical') return 1
    return a.estimatedDaysUntilFull - b.estimatedDaysUntilFull
  })
}

export function generateCapacityPlan(
  forecast: ResourceForecast,
  currentStats: ClusterStats,
  nodes: ClusterNode[],
  timeframeMonths: number = 6
): CapacityPlan {
  const timeframeIndex = Math.min(timeframeMonths * 4, forecast.cpu.length - 1)
  
  if (timeframeIndex < 0) {
    return {
      currentCapacity: {
        cpu: currentStats.totalCpu,
        memory: currentStats.totalMemory,
        storage: currentStats.totalStorage
      },
      recommendedCapacity: {
        cpu: currentStats.totalCpu,
        memory: currentStats.totalMemory,
        storage: currentStats.totalStorage
      },
      growthRate: {
        cpu: 0,
        memory: 0,
        storage: 0
      },
      nodesRequired: 0,
      estimatedCost: 0,
      timeframe: `${timeframeMonths} months`
    }
  }

  const projectedCpuUsage = forecast.cpu[timeframeIndex].predicted
  const projectedMemoryUsage = forecast.memory[timeframeIndex].predicted
  const projectedStorageUsage = forecast.storage[timeframeIndex].predicted

  const currentCpuUsagePercent = (currentStats.usedCpu / currentStats.totalCpu) * 100
  const currentMemoryUsagePercent = (currentStats.usedMemory / currentStats.totalMemory) * 100
  const currentStorageUsagePercent = (currentStats.usedStorage / currentStats.totalStorage) * 100

  const cpuGrowthRate = ((projectedCpuUsage - currentCpuUsagePercent) / timeframeMonths) * 100
  const memoryGrowthRate = ((projectedMemoryUsage - currentMemoryUsagePercent) / timeframeMonths) * 100
  const storageGrowthRate = ((projectedStorageUsage - currentStorageUsagePercent) / timeframeMonths) * 100

  const targetCapacityPercent = 70
  
  const requiredCpuCapacity = (currentStats.usedCpu * (projectedCpuUsage / currentCpuUsagePercent)) / (targetCapacityPercent / 100)
  const requiredMemoryCapacity = (currentStats.usedMemory * (projectedMemoryUsage / currentMemoryUsagePercent)) / (targetCapacityPercent / 100)
  const requiredStorageCapacity = (currentStats.usedStorage * (projectedStorageUsage / currentStorageUsagePercent)) / (targetCapacityPercent / 100)

  const avgNodeCpu = currentStats.totalCpu / currentStats.totalNodes
  const avgNodeMemory = currentStats.totalMemory / currentStats.totalNodes
  const avgNodeStorage = currentStats.totalStorage / currentStats.totalNodes

  const nodesForCpu = Math.max(0, Math.ceil((requiredCpuCapacity - currentStats.totalCpu) / avgNodeCpu))
  const nodesForMemory = Math.max(0, Math.ceil((requiredMemoryCapacity - currentStats.totalMemory) / avgNodeMemory))
  const nodesForStorage = Math.max(0, Math.ceil((requiredStorageCapacity - currentStats.totalStorage) / avgNodeStorage))

  const nodesRequired = Math.max(nodesForCpu, nodesForMemory, nodesForStorage)

  const costPerNode = 5000
  const estimatedCost = nodesRequired * costPerNode

  return {
    currentCapacity: {
      cpu: currentStats.totalCpu,
      memory: currentStats.totalMemory,
      storage: currentStats.totalStorage
    },
    recommendedCapacity: {
      cpu: Math.round(requiredCpuCapacity),
      memory: Math.round(requiredMemoryCapacity),
      storage: Math.round(requiredStorageCapacity)
    },
    growthRate: {
      cpu: cpuGrowthRate,
      memory: memoryGrowthRate,
      storage: storageGrowthRate
    },
    nodesRequired,
    estimatedCost,
    timeframe: `${timeframeMonths} months`
  }
}

export function collectHistoricalData(stats: ClusterStats): ResourceDataPoint {
  return {
    timestamp: Date.now(),
    cpu: (stats.usedCpu / stats.totalCpu) * 100,
    memory: (stats.usedMemory / stats.totalMemory) * 100,
    storage: (stats.usedStorage / stats.totalStorage) * 100,
    network: stats.networkThroughput
  }
}
