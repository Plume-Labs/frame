import {
  ResourceDataPoint,
  TrendStatistics,
  TimeframeOption,
  HistoricalTrendData
} from './types'

export function getTimeframeLabel(timeframe: TimeframeOption): string {
  const labels: Record<TimeframeOption, string> = {
    '1h': 'Last Hour',
    '6h': 'Last 6 Hours',
    '24h': 'Last 24 Hours',
    '7d': 'Last 7 Days',
    '30d': 'Last 30 Days',
    'all': 'All Time'
  }
  return labels[timeframe]
}

export function getTimeframeMilliseconds(timeframe: TimeframeOption): number | null {
  const durations: Record<TimeframeOption, number | null> = {
    '1h': 60 * 60 * 1000,
    '6h': 6 * 60 * 60 * 1000,
    '24h': 24 * 60 * 60 * 1000,
    '7d': 7 * 24 * 60 * 60 * 1000,
    '30d': 30 * 24 * 60 * 60 * 1000,
    'all': null
  }
  return durations[timeframe]
}

export function filterDataByTimeframe(
  data: ResourceDataPoint[],
  timeframe: TimeframeOption
): ResourceDataPoint[] {
  const duration = getTimeframeMilliseconds(timeframe)
  
  if (duration === null) {
    return data
  }
  
  const cutoffTime = Date.now() - duration
  return data.filter(point => point.timestamp >= cutoffTime)
}

function calculateMedian(values: number[]): number {
  if (values.length === 0) return 0
  
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  
  if (sorted.length % 2 === 0) {
    return (sorted[mid - 1] + sorted[mid]) / 2
  }
  return sorted[mid]
}

function calculateStandardDeviation(values: number[]): number {
  if (values.length === 0) return 0
  
  const mean = values.reduce((a, b) => a + b, 0) / values.length
  const squaredDiffs = values.map(x => Math.pow(x - mean, 2))
  const variance = squaredDiffs.reduce((a, b) => a + b, 0) / values.length
  return Math.sqrt(variance)
}

function calculateTrend(values: number[]): { trend: 'increasing' | 'decreasing' | 'stable'; percentage: number } {
  if (values.length < 2) {
    return { trend: 'stable', percentage: 0 }
  }
  
  const firstHalf = values.slice(0, Math.floor(values.length / 2))
  const secondHalf = values.slice(Math.floor(values.length / 2))
  
  const firstAvg = firstHalf.reduce((a, b) => a + b, 0) / firstHalf.length
  const secondAvg = secondHalf.reduce((a, b) => a + b, 0) / secondHalf.length
  
  const change = secondAvg - firstAvg
  const percentage = firstAvg > 0 ? (change / firstAvg) * 100 : 0
  
  if (Math.abs(percentage) < 2) {
    return { trend: 'stable', percentage: 0 }
  }
  
  return {
    trend: change > 0 ? 'increasing' : 'decreasing',
    percentage: Math.abs(percentage)
  }
}

export function calculateTrendStatistics(
  data: ResourceDataPoint[],
  resource: 'cpu' | 'memory' | 'storage' | 'network',
  timeframe: TimeframeOption
): TrendStatistics {
  const filteredData = filterDataByTimeframe(data, timeframe)
  
  if (filteredData.length === 0) {
    return {
      resource,
      timeframe,
      current: 0,
      average: 0,
      min: 0,
      max: 0,
      median: 0,
      stdDeviation: 0,
      trend: 'stable',
      trendPercentage: 0,
      peakTime: Date.now(),
      peakValue: 0,
      lowTime: Date.now(),
      lowValue: 0
    }
  }
  
  const values = filteredData.map(d => d[resource])
  const current = filteredData[filteredData.length - 1][resource]
  const average = values.reduce((a, b) => a + b, 0) / values.length
  const min = Math.min(...values)
  const max = Math.max(...values)
  const median = calculateMedian(values)
  const stdDeviation = calculateStandardDeviation(values)
  const { trend, percentage } = calculateTrend(values)
  
  const maxIndex = values.indexOf(max)
  const minIndex = values.indexOf(min)
  
  return {
    resource,
    timeframe,
    current,
    average,
    min,
    max,
    median,
    stdDeviation,
    trend,
    trendPercentage: percentage,
    peakTime: filteredData[maxIndex].timestamp,
    peakValue: max,
    lowTime: filteredData[minIndex].timestamp,
    lowValue: min
  }
}

export function analyzeHistoricalTrends(
  data: ResourceDataPoint[],
  timeframe: TimeframeOption
): HistoricalTrendData {
  const filteredData = filterDataByTimeframe(data, timeframe)
  
  const statistics: TrendStatistics[] = [
    calculateTrendStatistics(data, 'cpu', timeframe),
    calculateTrendStatistics(data, 'memory', timeframe),
    calculateTrendStatistics(data, 'storage', timeframe),
    calculateTrendStatistics(data, 'network', timeframe)
  ]
  
  return {
    timeframe,
    data: filteredData,
    statistics
  }
}
