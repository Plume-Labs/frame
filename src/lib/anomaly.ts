import {
  ResourceDataPoint,
  Anomaly,
  AnomalyPattern,
  AnomalyType
} from './types'

function calculateMean(values: number[]): number {
  return values.reduce((sum, val) => sum + val, 0) / values.length
}

function calculateStdDev(values: number[], mean: number): number {
  const squaredDiffs = values.map(val => Math.pow(val - mean, 2))
  const variance = squaredDiffs.reduce((sum, val) => sum + val, 0) / values.length
  return Math.sqrt(variance)
}

function calculateMovingAverage(values: number[], windowSize: number = 5): number[] {
  const result: number[] = []
  for (let i = 0; i < values.length; i++) {
    const start = Math.max(0, i - windowSize + 1)
    const window = values.slice(start, i + 1)
    result.push(calculateMean(window))
  }
  return result
}

function detectTrend(values: number[]): 'increasing' | 'decreasing' | 'stable' {
  if (values.length < 3) return 'stable'
  
  const recentValues = values.slice(-5)
  const firstHalf = recentValues.slice(0, Math.floor(recentValues.length / 2))
  const secondHalf = recentValues.slice(Math.floor(recentValues.length / 2))
  
  const firstMean = calculateMean(firstHalf)
  const secondMean = calculateMean(secondHalf)
  
  const changePercent = ((secondMean - firstMean) / firstMean) * 100
  
  if (changePercent > 10) return 'increasing'
  if (changePercent < -10) return 'decreasing'
  return 'stable'
}

export function buildAnomalyPatterns(historicalData: ResourceDataPoint[]): {
  cpu: AnomalyPattern
  memory: AnomalyPattern
  storage: AnomalyPattern
  network: AnomalyPattern
} {
  if (historicalData.length === 0) {
    const emptyPattern: AnomalyPattern = {
      resource: 'cpu',
      baseline: 0,
      baselineStdDev: 0,
      movingAverage: [],
      recentTrend: 'stable',
      lastUpdated: Date.now()
    }
    return {
      cpu: { ...emptyPattern, resource: 'cpu' },
      memory: { ...emptyPattern, resource: 'memory' },
      storage: { ...emptyPattern, resource: 'storage' },
      network: { ...emptyPattern, resource: 'network' }
    }
  }

  const cpuValues = historicalData.map(d => d.cpu)
  const memoryValues = historicalData.map(d => d.memory)
  const storageValues = historicalData.map(d => d.storage)
  const networkValues = historicalData.map(d => d.network)

  const cpuMean = calculateMean(cpuValues)
  const memoryMean = calculateMean(memoryValues)
  const storageMean = calculateMean(storageValues)
  const networkMean = calculateMean(networkValues)

  return {
    cpu: {
      resource: 'cpu',
      baseline: cpuMean,
      baselineStdDev: calculateStdDev(cpuValues, cpuMean),
      movingAverage: calculateMovingAverage(cpuValues),
      recentTrend: detectTrend(cpuValues),
      lastUpdated: Date.now()
    },
    memory: {
      resource: 'memory',
      baseline: memoryMean,
      baselineStdDev: calculateStdDev(memoryValues, memoryMean),
      movingAverage: calculateMovingAverage(memoryValues),
      recentTrend: detectTrend(memoryValues),
      lastUpdated: Date.now()
    },
    storage: {
      resource: 'storage',
      baseline: storageMean,
      baselineStdDev: calculateStdDev(storageValues, storageMean),
      movingAverage: calculateMovingAverage(storageValues),
      recentTrend: detectTrend(storageValues),
      lastUpdated: Date.now()
    },
    network: {
      resource: 'network',
      baseline: networkMean,
      baselineStdDev: calculateStdDev(networkValues, networkMean),
      movingAverage: calculateMovingAverage(networkValues),
      recentTrend: detectTrend(networkValues),
      lastUpdated: Date.now()
    }
  }
}

function detectSpikeAnomaly(
  value: number,
  pattern: AnomalyPattern,
  threshold: number = 3
): { isAnomaly: boolean; deviation: number; confidence: number } {
  const deviation = (value - pattern.baseline) / pattern.baselineStdDev
  const isAnomaly = deviation > threshold
  const confidence = Math.min(100, Math.abs(deviation) / threshold * 100)
  
  return { isAnomaly, deviation, confidence }
}

function detectDropAnomaly(
  value: number,
  pattern: AnomalyPattern,
  threshold: number = -2.5
): { isAnomaly: boolean; deviation: number; confidence: number } {
  const deviation = (value - pattern.baseline) / pattern.baselineStdDev
  const isAnomaly = deviation < threshold
  const confidence = Math.min(100, Math.abs(deviation) / Math.abs(threshold) * 100)
  
  return { isAnomaly, deviation, confidence }
}

function detectPatternDeviation(
  value: number,
  pattern: AnomalyPattern,
  recentValues: number[]
): { isAnomaly: boolean; deviation: number; confidence: number } {
  if (pattern.movingAverage.length === 0 || recentValues.length < 3) {
    return { isAnomaly: false, deviation: 0, confidence: 0 }
  }

  const expectedValue = pattern.movingAverage[pattern.movingAverage.length - 1]
  const recentMean = calculateMean(recentValues.slice(-3))
  const deviation = Math.abs(value - expectedValue) / pattern.baselineStdDev
  
  const patternDeviation = Math.abs(recentMean - expectedValue) / pattern.baselineStdDev
  const isAnomaly = patternDeviation > 2
  const confidence = Math.min(100, patternDeviation / 2 * 100)
  
  return { isAnomaly, deviation, confidence }
}

function detectSustainedHigh(
  recentValues: number[],
  pattern: AnomalyPattern,
  sustainedThreshold: number = 80,
  minDuration: number = 3
): { isAnomaly: boolean; duration: number; confidence: number } {
  if (recentValues.length < minDuration) {
    return { isAnomaly: false, duration: 0, confidence: 0 }
  }

  const recentHigh = recentValues.slice(-minDuration)
  const allHigh = recentHigh.every(val => val >= sustainedThreshold)
  const avgValue = calculateMean(recentHigh)
  
  let duration = 0
  for (let i = recentValues.length - 1; i >= 0; i--) {
    if (recentValues[i] >= sustainedThreshold) {
      duration++
    } else {
      break
    }
  }

  const confidence = allHigh ? Math.min(100, (avgValue - sustainedThreshold) / (100 - sustainedThreshold) * 100) : 0
  
  return { isAnomaly: allHigh, duration, confidence }
}

function detectOscillation(
  recentValues: number[],
  pattern: AnomalyPattern,
  minSwings: number = 3
): { isAnomaly: boolean; confidence: number } {
  if (recentValues.length < minSwings * 2) {
    return { isAnomaly: false, confidence: 0 }
  }

  const recent = recentValues.slice(-10)
  let swings = 0
  let previousDirection: 'up' | 'down' | null = null

  for (let i = 1; i < recent.length; i++) {
    const diff = recent[i] - recent[i - 1]
    const currentDirection = diff > 0 ? 'up' : 'down'
    
    if (previousDirection && currentDirection !== previousDirection) {
      swings++
    }
    previousDirection = currentDirection
  }

  const swingMagnitude = Math.max(...recent) - Math.min(...recent)
  const isAnomaly = swings >= minSwings && swingMagnitude > pattern.baselineStdDev * 2
  const confidence = isAnomaly ? Math.min(100, (swings / minSwings) * 50 + (swingMagnitude / (pattern.baselineStdDev * 4)) * 50) : 0
  
  return { isAnomaly, confidence }
}

function getSeverity(deviation: number, confidence: number): 'low' | 'medium' | 'high' | 'critical' {
  if (confidence < 60) return 'low'
  if (deviation > 4 || confidence > 90) return 'critical'
  if (deviation > 3 || confidence > 80) return 'high'
  if (deviation > 2 || confidence > 70) return 'medium'
  return 'low'
}

function getRecommendation(anomaly: {
  type: AnomalyType
  resource: string
  severity: string
}): string {
  const recommendations: Record<AnomalyType, Record<string, string>> = {
    spike: {
      cpu: 'Investigate processes causing CPU spike. Check for runaway containers or workload bursts.',
      memory: 'Check for memory leaks or large data processing jobs. Review container memory limits.',
      storage: 'Investigate sudden storage consumption. Check for log accumulation or data dumps.',
      network: 'Analyze network traffic patterns. Check for DDoS attempts or data transfers.'
    },
    drop: {
      cpu: 'CPU utilization dropped significantly. Verify nodes are receiving workload properly.',
      memory: 'Memory usage dropped unexpectedly. Check if services crashed or restarted.',
      storage: 'Storage usage decreased. Verify data integrity and check for cleanup operations.',
      network: 'Network traffic dropped. Check for connectivity issues or service outages.'
    },
    pattern_deviation: {
      cpu: 'CPU usage deviates from normal pattern. Monitor for workload changes or issues.',
      memory: 'Memory usage pattern anomaly detected. Review application behavior.',
      storage: 'Storage growth pattern changed. Investigate data ingestion pipeline.',
      network: 'Network traffic pattern unusual. Review application communication patterns.'
    },
    sustained_high: {
      cpu: 'CPU running at sustained high levels. Scale cluster or optimize workloads.',
      memory: 'Memory consistently high. Add nodes or optimize memory-intensive applications.',
      storage: 'Storage consistently near capacity. Expand storage or implement data retention policies.',
      network: 'Network bandwidth consistently high. Consider upgrading network capacity.'
    },
    oscillation: {
      cpu: 'CPU oscillating rapidly. Check for competing processes or scheduling issues.',
      memory: 'Memory usage oscillating. Investigate memory allocation patterns.',
      storage: 'Storage showing oscillation. Review data write/delete patterns.',
      network: 'Network traffic oscillating. Check for retry storms or connection issues.'
    }
  }

  return recommendations[anomaly.type]?.[anomaly.resource] || 'Monitor resource and investigate anomaly.'
}

export function detectAnomalies(
  currentData: ResourceDataPoint,
  historicalData: ResourceDataPoint[],
  patterns: ReturnType<typeof buildAnomalyPatterns>
): Anomaly[] {
  if (historicalData.length < 5) {
    return []
  }

  const anomalies: Anomaly[] = []
  let anomalyId = 0

  const resources: Array<'cpu' | 'memory' | 'storage' | 'network'> = ['cpu', 'memory', 'storage', 'network']

  resources.forEach(resource => {
    const currentValue = currentData[resource]
    const pattern = patterns[resource]
    const recentValues = historicalData.slice(-10).map(d => d[resource])

    const spike = detectSpikeAnomaly(currentValue, pattern)
    if (spike.isAnomaly) {
      const severity = getSeverity(spike.deviation, spike.confidence)
      anomalies.push({
        id: `anomaly-${anomalyId++}`,
        timestamp: currentData.timestamp,
        type: 'spike',
        resource,
        severity,
        value: currentValue,
        expectedValue: pattern.baseline,
        deviation: spike.deviation,
        confidence: spike.confidence,
        description: `Sudden ${resource.toUpperCase()} spike detected: ${currentValue.toFixed(1)}% (expected ~${pattern.baseline.toFixed(1)}%)`,
        recommendation: getRecommendation({ type: 'spike', resource, severity })
      })
    }

    const drop = detectDropAnomaly(currentValue, pattern)
    if (drop.isAnomaly) {
      const severity = getSeverity(Math.abs(drop.deviation), drop.confidence)
      anomalies.push({
        id: `anomaly-${anomalyId++}`,
        timestamp: currentData.timestamp,
        type: 'drop',
        resource,
        severity,
        value: currentValue,
        expectedValue: pattern.baseline,
        deviation: drop.deviation,
        confidence: drop.confidence,
        description: `Unexpected ${resource.toUpperCase()} drop detected: ${currentValue.toFixed(1)}% (expected ~${pattern.baseline.toFixed(1)}%)`,
        recommendation: getRecommendation({ type: 'drop', resource, severity })
      })
    }

    const patternDev = detectPatternDeviation(currentValue, pattern, recentValues)
    if (patternDev.isAnomaly) {
      const severity = getSeverity(patternDev.deviation, patternDev.confidence)
      const expectedValue = pattern.movingAverage[pattern.movingAverage.length - 1]
      anomalies.push({
        id: `anomaly-${anomalyId++}`,
        timestamp: currentData.timestamp,
        type: 'pattern_deviation',
        resource,
        severity,
        value: currentValue,
        expectedValue,
        deviation: patternDev.deviation,
        confidence: patternDev.confidence,
        description: `${resource.toUpperCase()} usage deviates from normal pattern: ${currentValue.toFixed(1)}% vs expected ${expectedValue.toFixed(1)}%`,
        recommendation: getRecommendation({ type: 'pattern_deviation', resource, severity })
      })
    }

    const sustained = detectSustainedHigh(recentValues, pattern)
    if (sustained.isAnomaly) {
      const severity = sustained.confidence > 80 ? 'critical' : sustained.confidence > 60 ? 'high' : 'medium'
      anomalies.push({
        id: `anomaly-${anomalyId++}`,
        timestamp: currentData.timestamp,
        type: 'sustained_high',
        resource,
        severity,
        value: currentValue,
        expectedValue: pattern.baseline,
        deviation: (currentValue - pattern.baseline) / pattern.baselineStdDev,
        confidence: sustained.confidence,
        duration: sustained.duration,
        description: `${resource.toUpperCase()} sustained at high levels: ${currentValue.toFixed(1)}% for ${sustained.duration} intervals`,
        recommendation: getRecommendation({ type: 'sustained_high', resource, severity })
      })
    }

    const oscillation = detectOscillation(recentValues, pattern)
    if (oscillation.isAnomaly) {
      const severity = oscillation.confidence > 80 ? 'high' : oscillation.confidence > 60 ? 'medium' : 'low'
      anomalies.push({
        id: `anomaly-${anomalyId++}`,
        timestamp: currentData.timestamp,
        type: 'oscillation',
        resource,
        severity,
        value: currentValue,
        expectedValue: pattern.baseline,
        deviation: (currentValue - pattern.baseline) / pattern.baselineStdDev,
        confidence: oscillation.confidence,
        description: `${resource.toUpperCase()} showing rapid oscillation pattern`,
        recommendation: getRecommendation({ type: 'oscillation', resource, severity })
      })
    }
  })

  return anomalies.sort((a, b) => {
    const severityOrder = { critical: 0, high: 1, medium: 2, low: 3 }
    return severityOrder[a.severity] - severityOrder[b.severity]
  })
}
