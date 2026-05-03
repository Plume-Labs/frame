import { useState, useEffect, useRef } from 'react'
import { ClusterNode, SystemEvent, ResourceDataPoint, ResourceForecast, CapacityAlert, CapacityPlan, Anomaly } from '@/lib/types'
import {
  generateClusterNodes,
  updateNodeMetrics,
  simulateStatusChange,
  calculateClusterStats,
  generateSystemEvent
} from '@/lib/cluster'
import {
  generateForecast,
  generateCapacityAlerts,
  generateCapacityPlan,
  collectHistoricalData
} from '@/lib/forecasting'
import { buildAnomalyPatterns, detectAnomalies } from '@/lib/anomaly'
import { NodeGrid } from '@/components/NodeGrid'
import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { ClusterStatsDashboard } from '@/components/ClusterStatsDashboard'
import { NetworkDashboard } from '@/components/NetworkDashboard'
import { StorageDashboard } from '@/components/StorageDashboard'
import { EventLog } from '@/components/EventLog'
import { CapacityPlanningDashboard } from '@/components/CapacityPlanningDashboard'
import { ForecastChart } from '@/components/ForecastChart'
import { CapacityPlanCard } from '@/components/CapacityPlanCard'
import { HistoricalTrendsAnalysis } from '@/components/HistoricalTrendsAnalysis'
import { AnomalyAlerts } from '@/components/AnomalyAlerts'
import { RackVisualization } from '@/components/RackVisualization'
import { ZoneView } from '@/components/ZoneView'
import { ZoneHeatmap } from '@/components/ZoneHeatmap'
import { NodesSummaryCard } from '@/components/NodesSummaryCard'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useKV } from '@github/spark/hooks'

function App() {
  const [nodes, setNodes] = useState<ClusterNode[]>(() => generateClusterNodes(32))
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [selectedRack, setSelectedRack] = useState<string | null>(null)
  const [events, setEvents] = useState<SystemEvent[]>([])
  const [activeTab, setActiveTab] = useState('overview')
  const [selectedZoneFromHeatmap, setSelectedZoneFromHeatmap] = useState<string | null>(null)
  const previousNodesRef = useRef<ClusterNode[]>(nodes)

  const [historicalData, setHistoricalData] = useKV<ResourceDataPoint[]>('capacity-historical-data', [])
  const [forecast, setForecast] = useState<ResourceForecast>({ cpu: [], memory: [], storage: [], network: [] })
  const [alerts, setAlerts] = useState<CapacityAlert[]>([])
  const [capacityPlan, setCapacityPlan] = useState<CapacityPlan | null>(null)
  const [anomalies, setAnomalies] = useState<Anomaly[]>([])

  useEffect(() => {
    const metricsInterval = setInterval(() => {
      setNodes((currentNodes) => {
        const updated = currentNodes.map((node) => {
          let updatedNode = updateNodeMetrics(node)
          updatedNode = simulateStatusChange(updatedNode)
          return updatedNode
        })

        const newEvent = generateSystemEvent(updated, previousNodesRef.current)
        if (newEvent) {
          setEvents((currentEvents) => [newEvent, ...currentEvents].slice(0, 100))
        }

        previousNodesRef.current = updated
        return updated
      })
    }, 2000)

    return () => clearInterval(metricsInterval)
  }, [])

  useEffect(() => {
    if (selectedNode) {
      const updatedNode = nodes.find((n) => n.id === selectedNode.id)
      if (updatedNode) {
        setSelectedNode(updatedNode)
      }
    }
  }, [nodes, selectedNode])

  useEffect(() => {
    const dataCollectionInterval = setInterval(() => {
      const stats = calculateClusterStats(nodes)
      const dataPoint = collectHistoricalData(stats)
      
      setHistoricalData((currentData) => {
        const newData = [...(currentData || []), dataPoint]
        return newData.slice(-50)
      })
    }, 10000)

    return () => clearInterval(dataCollectionInterval)
  }, [nodes, setHistoricalData])

  useEffect(() => {
    if (historicalData && historicalData.length >= 5) {
      const newForecast = generateForecast(historicalData, 12)
      setForecast(newForecast)

      const stats = calculateClusterStats(nodes)
      const newAlerts = generateCapacityAlerts(newForecast, stats)
      setAlerts(newAlerts)

      const plan = generateCapacityPlan(newForecast, stats, nodes, 6)
      setCapacityPlan(plan)

      const patterns = buildAnomalyPatterns(historicalData)
      const currentData = collectHistoricalData(stats)
      const detectedAnomalies = detectAnomalies(currentData, historicalData, patterns, nodes)
      setAnomalies(detectedAnomalies)
    }
  }, [historicalData, nodes])

  const stats = calculateClusterStats(nodes)

  return (
    <div className="min-h-screen bg-background">
      <div className="container mx-auto p-4 sm:p-6">
        <header className="flex items-center justify-between mb-6">
          <div>
            <h1 className="text-3xl font-mono font-bold text-primary tracking-tight">
              CLUSTER CONTROL
            </h1>
            <p className="text-sm text-muted-foreground">
              Distributed Systems Monitor
            </p>
          </div>
          <ClusterStatsDashboard stats={stats} compact />
        </header>

        <Tabs value={activeTab} onValueChange={setActiveTab} className="w-full">
          <TabsList className="font-mono mb-6">
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="racks">Racks</TabsTrigger>
            <TabsTrigger value="heatmap">Heatmap</TabsTrigger>
            <TabsTrigger value="zones">Zones</TabsTrigger>
            <TabsTrigger value="topology">Topology</TabsTrigger>
            <TabsTrigger value="analytics">Analytics</TabsTrigger>
            <TabsTrigger value="events">Events</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-6">
            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
              <NodesSummaryCard nodes={nodes} />
              <div className="space-y-6">
                <NetworkDashboard nodes={nodes} />
                <StorageDashboard nodes={nodes} />
              </div>
            </div>
            
            {anomalies.length > 0 && (
              <AnomalyAlerts anomalies={anomalies} />
            )}
            
            {alerts.length > 0 && (
              <CapacityPlanningDashboard alerts={alerts} />
            )}
          </TabsContent>

          <TabsContent value="racks" className="space-y-6">
            <RackVisualization
              nodes={nodes}
              selectedNode={selectedNode}
              onSelectNode={setSelectedNode}
              selectedRack={selectedRack}
              onSelectRack={setSelectedRack}
            />
          </TabsContent>

          <TabsContent value="heatmap" className="space-y-6">
            <ZoneHeatmap
              nodes={nodes}
              onZoneClick={(zoneName) => {
                setSelectedZoneFromHeatmap(zoneName)
                setActiveTab('zones')
              }}
            />
          </TabsContent>

          <TabsContent value="zones" className="space-y-6">
            <ZoneView
              nodes={nodes}
              selectedNode={selectedNode}
              onSelectNode={setSelectedNode}
            />
          </TabsContent>

          <TabsContent value="topology" className="space-y-6">
            <NodeGrid
              nodes={nodes}
              selectedNode={selectedNode}
              onSelectNode={setSelectedNode}
            />
          </TabsContent>

          <TabsContent value="analytics" className="space-y-6">
            {historicalData && historicalData.length > 0 && (
              <HistoricalTrendsAnalysis historicalData={historicalData} />
            )}
            
            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
              {historicalData && historicalData.length >= 5 && (
                <ForecastChart forecast={forecast} />
              )}
              {capacityPlan && (
                <CapacityPlanCard plan={capacityPlan} />
              )}
            </div>
          </TabsContent>

          <TabsContent value="events" className="space-y-6">
            <EventLog events={events} />
          </TabsContent>
        </Tabs>

        <NodeDetailPanel
          node={selectedNode}
          open={!!selectedNode}
          onClose={() => setSelectedNode(null)}
        />
      </div>
    </div>
  )
}

export default App