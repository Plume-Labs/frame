import { useState, useEffect, useRef } from 'react'
import { ClusterNode, SystemEvent } from '@/lib/types'
import {
  generateClusterNodes,
  updateNodeMetrics,
  simulateStatusChange,
  calculateClusterStats,
  generateSystemEvent
} from '@/lib/cluster'
import { NodeGrid } from '@/components/NodeGrid'
import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { ClusterStatsDashboard } from '@/components/ClusterStatsDashboard'
import { EventLog } from '@/components/EventLog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useIsMobile } from '@/hooks/use-mobile'

function App() {
  const [nodes, setNodes] = useState<ClusterNode[]>(() => generateClusterNodes(32))
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [events, setEvents] = useState<SystemEvent[]>([])
  const previousNodesRef = useRef<ClusterNode[]>(nodes)
  const isMobile = useIsMobile()

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
            <TabsList className="grid w-full grid-cols-3">
              <TabsTrigger value="topology" className="font-mono">Topology</TabsTrigger>
              <TabsTrigger value="metrics" className="font-mono">Metrics</TabsTrigger>
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

              <EventLog events={events} />
            </div>

            <div className="space-y-6">
              <ClusterStatsDashboard stats={stats} />
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