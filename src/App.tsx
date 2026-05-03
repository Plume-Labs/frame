import { useState, useEffect, useRef } from 'react'
import { ClusterNode, SystemEvent, ResourceDataPoint, ResourceForecast, CapacityAlert, CapacityPlan } from '@/lib/types'
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
import { NodeGrid } from '@/components/NodeGrid'
import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { ClusterStatsDashboard } from '@/components/ClusterStatsDashboard'
import { NetworkDashboard } from '@/components/NetworkDashboard'
import { StorageDashboard } from '@/components/StorageDashboard'
import { EventLog } from '@/components/EventLog'
import { CapacityPlanningDashboard } from '@/components/CapacityPlanningDashboard'
import { ForecastChart } from '@/components/ForecastChart'
import { CapacityPlanCard } from '@/components/CapacityPlanCard'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsMobile } from '@/hooks/use-mobile'
import { useKV } from '@github/spark/hooks'

function App() {
  const [nodes, setNodes] = useState<ClusterNode[]>(() => generateClusterNodes(32))
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [events, setEvents] = useState<SystemEvent[]>([])
  const previousNodesRef = useRef<ClusterNode[]>(nodes)
  const isMobile = useIsMobile()

  const [historicalData, setHistoricalData] = useKV<ResourceDataPoint[]>('capacity-historical-data', [])
  const [forecast, setForecast] = useState<ResourceForecast>({ cpu: [], memory: [], storage: [], network: [] })
  const [alerts, setAlerts] = useState<CapacityAlert[]>([])
  const [capacityPlan, setCapacityPlan] = useState<CapacityPlan | null>(null)

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
    }
  }, [historicalData, nodes])

  const stats = calculateClusterStats(nodes)

  return (
    <div className="min-h-screen bg-background">
      <div className="container mx-auto p-4 sm:p-6 space-y-6">
        <header className="space-y-2">
          <h1 className="text-4xl font-mono font-bold text-primary tracking-tight">
            CLUSTER CONTROL
          </h1>
          <p className="text-muted-foreground">
            Distributed Systems Monitor
          </p>
        </header>

        {isMobile ? (
          <Tabs defaultValue="topology" className="w-full">
            <TabsList className="grid w-full grid-cols-5 text-xs">
              <TabsTrigger value="topology" className="font-mono">Nodes</TabsTrigger>
              <TabsTrigger value="metrics" className="font-mono">Metrics</TabsTrigger>
              <TabsTrigger value="infra" className="font-mono">Infra</TabsTrigger>
              <TabsTrigger value="capacity" className="font-mono">Plan</TabsTrigger>
              <TabsTrigger value="events" className="font-mono">Events</TabsTrigger>
            </TabsList>
            <TabsContent value="topology" className="space-y-4 mt-6">
              <NodeGrid
                nodes={nodes}
                selectedNode={selectedNode}
                onSelectNode={setSelectedNode}
              />
            </TabsContent>
            <TabsContent value="metrics" className="space-y-4 mt-6">
              <ClusterStatsDashboard stats={stats} />
            </TabsContent>
            <TabsContent value="infra" className="space-y-4 mt-6">
              <NetworkDashboard nodes={nodes} />
              <StorageDashboard nodes={nodes} />
            </TabsContent>
            <TabsContent value="capacity" className="space-y-4 mt-6">
              <CapacityPlanningDashboard alerts={alerts} />
              {historicalData && historicalData.length >= 5 && <ForecastChart forecast={forecast} />}
              {capacityPlan && <CapacityPlanCard plan={capacityPlan} />}
            </TabsContent>
            <TabsContent value="events" className="space-y-4 mt-6">
              <EventLog events={events} />
            </TabsContent>
          </Tabs>
        ) : (
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
            <div className="lg:col-span-2 space-y-6">
              <div className="space-y-4">
                <h2 className="text-2xl font-mono font-semibold text-foreground">
                  Node Topology
                </h2>
                <NodeGrid
                  nodes={nodes}
                  selectedNode={selectedNode}
                  onSelectNode={setSelectedNode}
                />
              </div>

              <NetworkDashboard nodes={nodes} />
              <StorageDashboard nodes={nodes} />

              <div className="space-y-4">
                <h2 className="text-2xl font-mono font-semibold text-foreground">
                  Capacity Planning
                </h2>
                <CapacityPlanningDashboard alerts={alerts} />
                {historicalData && historicalData.length >= 5 && <ForecastChart forecast={forecast} />}
              </div>

              <EventLog events={events} />
            </div>

            <div className="space-y-6">
              <ClusterStatsDashboard stats={stats} />
              {capacityPlan && <CapacityPlanCard plan={capacityPlan} />}
            </div>
          </div>
        )}

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