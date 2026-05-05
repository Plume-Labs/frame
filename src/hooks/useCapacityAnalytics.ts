import { useState, useEffect, type MutableRefObject } from 'react'
import { useKV } from '@github/spark/hooks'
import { ClusterNode, ResourceDataPoint, ResourceForecast, CapacityAlert, CapacityPlan, Anomaly } from '@/lib/types'
import {
  generateForecast,
  generateCapacityAlerts,
  generateCapacityPlan,
  collectHistoricalData
} from '@/lib/forecasting'
import { buildAnomalyPatterns, detectAnomalies } from '@/lib/anomaly'
import { calculateClusterStats } from '@/lib/cluster'

/**
 * Manages all capacity-related analytics: historical data collection,
 * forecasting, capacity alerts, capacity planning and anomaly detection.
 *
 * Accepts a `nodesRef` (from `useClusterSimulation`) instead of the `nodes`
 * state directly, so the 10-second data-collection interval is not torn down
 * and recreated on every 2-second node-metrics tick.
 */
export function useCapacityAnalytics(nodesRef: MutableRefObject<ClusterNode[]>) {
  const [historicalData, setHistoricalData] = useKV<ResourceDataPoint[]>('capacity-historical-data', [])
  const [forecast, setForecast] = useState<ResourceForecast>({ cpu: [], memory: [], storage: [], network: [] })
  const [alerts, setAlerts] = useState<CapacityAlert[]>([])
  const [capacityPlan, setCapacityPlan] = useState<CapacityPlan | null>(null)
  const [anomalies, setAnomalies] = useState<Anomaly[]>([])

  // Collect a data point every 10 seconds. Uses nodesRef so this interval
  // is stable and never torn down due to the 2-second node update cycle.
  useEffect(() => {
    const interval = setInterval(() => {
      const stats = calculateClusterStats(nodesRef.current)
      const dataPoint = collectHistoricalData(stats)
      setHistoricalData((currentData) => {
        const newData = [...(currentData ?? []), dataPoint]
        return newData.slice(-50)
      })
    }, 10000)

    return () => clearInterval(interval)
  }, [nodesRef, setHistoricalData])

  // Recompute forecasts, alerts, plan and anomalies only when
  // historicalData changes — not on every 2-second node tick.
  useEffect(() => {
    if (!historicalData || historicalData.length < 5) return

    const nodes = nodesRef.current
    const stats = calculateClusterStats(nodes)

    const newForecast = generateForecast(historicalData, 12)
    setForecast(newForecast)
    setAlerts(generateCapacityAlerts(newForecast, stats))
    setCapacityPlan(generateCapacityPlan(newForecast, stats, nodes, 6))

    const patterns = buildAnomalyPatterns(historicalData)
    const currentData = collectHistoricalData(stats)
    setAnomalies(detectAnomalies(currentData, historicalData, patterns))
  }, [historicalData, nodesRef])

  return { historicalData, forecast, alerts, capacityPlan, anomalies }
}
