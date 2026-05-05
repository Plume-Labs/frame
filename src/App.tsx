import { useMemo, useState } from 'react'
import { ClusterNode } from '@/lib/types'
import { calculateClusterStats } from '@/lib/cluster'
import { useClusterSimulation } from '@/hooks/useClusterSimulation'
import { useCapacityAnalytics } from '@/hooks/useCapacityAnalytics'
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
import { DragDropRackManager } from '@/components/DragDropRackManager'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
// HPC components
import { SchedulerDashboard } from '@/components/SchedulerDashboard'
import { ServiceClassPanel } from '@/components/ServiceClassPanel'
import { DataLocalityView } from '@/components/DataLocalityView'
import { JobOrchestrationView } from '@/components/JobOrchestrationView'
import { DataFabricDashboard } from '@/components/DataFabricDashboard'
import { GPUMonitoringDashboard } from '@/components/GPUMonitoringDashboard'
import { DataLineageView } from '@/components/DataLineageView'
import { ResiliencePanel } from '@/components/ResiliencePanel'

function App() {
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [selectedRack, setSelectedRack] = useState<string | null>(null)
  const [activeTab, setActiveTab] = useState('overview')
  const [activeHpcTab, setActiveHpcTab] = useState('scheduler')
  const [selectedZoneFromHeatmap, setSelectedZoneFromHeatmap] = useState<string | null>(null)

  const { nodes, setNodes, events, nodesRef } = useClusterSimulation(32)
  const { historicalData, forecast, alerts, capacityPlan, anomalies } = useCapacityAnalytics(nodesRef)

  // Derive the selected node from the authoritative nodes array so the detail
  // panel always shows up-to-date metrics without an extra state update cycle.
  const syncedSelectedNode = useMemo(
    () => (selectedNode ? (nodes.find((n) => n.id === selectedNode.id) ?? null) : null),
    [nodes, selectedNode]
  )

  // Memoize cluster-wide stats so they aren't recomputed on every render
  const stats = useMemo(() => calculateClusterStats(nodes), [nodes])

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
            <TabsTrigger value="organize">Organize</TabsTrigger>
            <TabsTrigger value="heatmap">Heatmap</TabsTrigger>
            <TabsTrigger value="zones">Zones</TabsTrigger>
            <TabsTrigger value="topology">Topology</TabsTrigger>
            <TabsTrigger value="analytics">Analytics</TabsTrigger>
            <TabsTrigger value="events">Events</TabsTrigger>
            <TabsTrigger value="hpc">HPC</TabsTrigger>
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
              selectedNode={syncedSelectedNode}
              onSelectNode={setSelectedNode}
              selectedRack={selectedRack}
              onSelectRack={setSelectedRack}
            />
          </TabsContent>

          <TabsContent value="organize" className="space-y-6">
            <DragDropRackManager
              nodes={nodes}
              onNodesUpdate={setNodes}
              selectedNode={syncedSelectedNode}
              onSelectNode={setSelectedNode}
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
              selectedNode={syncedSelectedNode}
              onSelectNode={setSelectedNode}
              initialZone={selectedZoneFromHeatmap}
              onZoneConsumed={() => setSelectedZoneFromHeatmap(null)}
            />
          </TabsContent>

          <TabsContent value="topology" className="space-y-6">
            <NodeGrid
              nodes={nodes}
              selectedNode={syncedSelectedNode}
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

          {/* HPC tab — all Neura HPC + Mainframe views */}
          <TabsContent value="hpc" className="space-y-6">
            <Tabs value={activeHpcTab} onValueChange={setActiveHpcTab} className="w-full">
              <TabsList className="font-mono mb-4 flex-wrap h-auto">
                <TabsTrigger value="scheduler">Scheduler</TabsTrigger>
                <TabsTrigger value="service-classes">Service Classes</TabsTrigger>
                <TabsTrigger value="data-locality">Data Locality</TabsTrigger>
                <TabsTrigger value="jobs">Jobs</TabsTrigger>
                <TabsTrigger value="storage">Storage</TabsTrigger>
                <TabsTrigger value="gpu">GPU</TabsTrigger>
                <TabsTrigger value="lineage">Lineage</TabsTrigger>
                <TabsTrigger value="resilience">Resilience</TabsTrigger>
              </TabsList>

              <TabsContent value="scheduler">
                <SchedulerDashboard nodes={nodes} />
              </TabsContent>

              <TabsContent value="service-classes">
                <ServiceClassPanel nodes={nodes} />
              </TabsContent>

              <TabsContent value="data-locality">
                <DataLocalityView nodes={nodes} />
              </TabsContent>

              <TabsContent value="jobs">
                <JobOrchestrationView />
              </TabsContent>

              <TabsContent value="storage">
                <DataFabricDashboard nodes={nodes} />
              </TabsContent>

              <TabsContent value="gpu">
                <GPUMonitoringDashboard nodes={nodes} />
              </TabsContent>

              <TabsContent value="lineage">
                <DataLineageView />
              </TabsContent>

              <TabsContent value="resilience">
                <ResiliencePanel nodes={nodes} />
              </TabsContent>
            </Tabs>
          </TabsContent>
        </Tabs>

        <NodeDetailPanel
          node={syncedSelectedNode}
          open={!!syncedSelectedNode}
          onClose={() => setSelectedNode(null)}
        />
      </div>
    </div>
  )
}

export default App
