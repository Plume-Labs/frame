import { ReactNode, useMemo, useState } from 'react'
import { ClusterNode } from '@/lib/types'
import { calculateClusterStats } from '@/lib/cluster'
import { useClusterSimulation } from '@/hooks/useClusterSimulation'

import { ClusterNodesView } from '@/components/ClusterNodesView'
import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { NodeProvisionWizard } from '@/components/NodeProvisionWizard'
import { ClusterStatsDashboard } from '@/components/ClusterStatsDashboard'
import { RackVisualization } from '@/components/RackVisualization'
import { DragDropRackManager } from '@/components/DragDropRackManager'

import { ApplicationsView } from '@/components/ApplicationsView'
import { FrameJobsView } from '@/components/FrameJobsView'
import { FrameSchedulerView } from '@/components/FrameSchedulerView'
import { ServiceClassesView } from '@/components/ServiceClassesView'
import { LineageView } from '@/components/LineageView'

import { GPUMonitoringDashboard } from '@/components/GPUMonitoringDashboard'
import { ClusterStorageView } from '@/components/ClusterStorageView'
import { NetworkView } from '@/components/NetworkView'
import { WorkloadPlacementView } from '@/components/WorkloadPlacementView'

import { KVCacheDashboard } from '@/components/KVCacheDashboard'
import { VolcanoPoolsView } from '@/components/VolcanoPoolsView'
import { SpeculativeDecodingDashboard } from '@/components/SpeculativeDecodingDashboard'
import { PipelinePPDashboard } from '@/components/PipelinePPDashboard'
import { MPSDashboard } from '@/components/MPSDashboard'
import { PTPSyncDashboard } from '@/components/PTPSyncDashboard'
import { BurstBufferDashboard } from '@/components/BurstBufferDashboard'
import { KsmView } from '@/components/KsmView'

import { CapacityView } from '@/components/CapacityView'
import { ResilienceView } from '@/components/ResilienceView'
import { ClusterEventsView } from '@/components/ClusterEventsView'

import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import {
  Archive,
  ArrowsLeftRight,
  Calendar,
  ChartLine,
  Clock,
  Cpu,
  Database,
  Gauge,
  GitBranch,
  HardDrive,
  HardDrives,
  Info,
  Lightning,
  Network,
  Package,
  Queue,
  ShieldCheck,
  ShieldWarning,
  Shuffle,
  Speedometer,
  Stack,
  Waveform,
} from '@phosphor-icons/react'

/**
 * Navigation model.
 *
 * The app used to expose all 21 surfaces as one flat tab strip that wrapped
 * onto three rows. They are grouped here by the question each answers —
 * what runs (Workloads), what it runs on (Compute), what it consumes
 * (Resources), how it is tuned (Tuning), and how it is behaving (Operations).
 */
interface NavItem {
  id: string
  label: string
  icon: ReactNode
  /** Shown under the page title; explains what the screen is for. */
  description: string
}

interface NavGroup {
  label: string
  items: NavItem[]
}

const NAV: NavGroup[] = [
  {
    label: 'Workloads',
    items: [
      { id: 'applications', label: 'Applications', icon: <Package />, description: 'Deployed apps read live from the cluster' },
      { id: 'jobs', label: 'Jobs', icon: <Queue />, description: 'Workflow DAGs, queue depth and checkpoint state' },
      { id: 'scheduler', label: 'Scheduler', icon: <Calendar />, description: 'Live SchedulingPolicy resources from the operator' },
      { id: 'service-classes', label: 'Service Classes', icon: <ShieldCheck />, description: 'HIGH / MEDIUM / LOW tiers against their SLA targets' },
      { id: 'lineage', label: 'Lineage', icon: <GitBranch />, description: 'Live Argo Workflow runs as timed span traces' },
    ],
  },
  {
    label: 'Compute',
    items: [
      { id: 'nodes', label: 'Nodes', icon: <Cpu />, description: 'Fleet health and per-node status' },
      { id: 'racks', label: 'Racks', icon: <Stack />, description: 'Rack elevations, power and cooling, layout editing' },
    ],
  },
  {
    label: 'Resources',
    items: [
      { id: 'gpu', label: 'GPU', icon: <Speedometer />, description: 'Utilisation, MIG partitioning, power and thermals' },
      { id: 'storage', label: 'Storage', icon: <Database />, description: 'Live Ceph health, OSDs, capacity and pools' },
      { id: 'network', label: 'Network', icon: <Network />, description: 'Live per-node NIC throughput' },
      { id: 'data-locality', label: 'Placement', icon: <HardDrives />, description: 'Live pod-to-node workload placement' },
    ],
  },
  {
    label: 'Tuning',
    items: [
      { id: 'kv-cache', label: 'KV-Cache', icon: <Lightning />, description: 'Distributed KV-cache over RDMA' },
      { id: 'elastic-pools', label: 'Elastic Pools', icon: <ArrowsLeftRight />, description: 'Live Volcano queues and gang-scheduled PodGroups' },
      { id: 'speculative', label: 'Speculative', icon: <Shuffle />, description: 'Draft-model speculative decoding' },
      { id: 'pipeline-pp', label: 'Pipeline PP', icon: <Waveform />, description: 'Prefill/decode pipeline parallelism' },
      { id: 'mps', label: 'MPS', icon: <Gauge />, description: 'NVIDIA Multi-Process Service sharing' },
      { id: 'ptp', label: 'PTP Sync', icon: <Clock />, description: 'IEEE 1588 clock synchronisation' },
      { id: 'burst-buffer', label: 'Burst Buffer', icon: <HardDrive />, description: 'NVMe write-behind checkpoint staging' },
      { id: 'ksm', label: 'KSM', icon: <Archive />, description: 'Live kernel same-page merging (node-exporter)' },
    ],
  },
  {
    label: 'Operations',
    items: [
      { id: 'capacity', label: 'Capacity', icon: <ChartLine />, description: 'Live allocatable vs used vs reserved' },
      { id: 'resilience', label: 'Resilience', icon: <ShieldWarning />, description: 'Live durability, disruption budgets, restart hotspots' },
      { id: 'events', label: 'Events', icon: <Info />, description: 'Cluster event feed' },
    ],
  },
]

const NAV_INDEX: Record<string, NavItem> = Object.fromEntries(
  NAV.flatMap((group) => group.items).map((item) => [item.id, item]),
)

function App() {
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [selectedRack, setSelectedRack] = useState<string | null>(null)
  const [screen, setScreen] = useState('jobs')
  const [provisionWizardOpen, setProvisionWizardOpen] = useState(false)

  const { nodes, setNodes } = useClusterSimulation(32)

  // Derive the selected node from the authoritative nodes array so the detail
  // panel always shows up-to-date metrics without an extra state update cycle.
  const syncedSelectedNode = useMemo(
    () => (selectedNode ? (nodes.find((n) => n.id === selectedNode.id) ?? null) : null),
    [nodes, selectedNode],
  )

  // Memoize cluster-wide stats so they aren't recomputed on every render
  const stats = useMemo(() => calculateClusterStats(nodes), [nodes])
  const racks = useMemo(() => Array.from(new Set(nodes.map((node) => node.rackId))).sort(), [nodes])
  const controlPlaneCount = useMemo(
    () => nodes.filter((node) => /control|master/i.test(node.name)).length,
    [nodes],
  )

  const active = NAV_INDEX[screen] ?? NAV_INDEX['jobs']

  // Only the active screen is mounted — the previous flat-tab layout kept all
  // 21 panels in the tree at once.
  function renderScreen(): ReactNode {
    switch (screen) {
      // ── Workloads ───────────────────────────────────────────────────────
      case 'applications':
        return <ApplicationsView />
      case 'jobs':
        return <FrameJobsView />
      case 'scheduler':
        return <FrameSchedulerView />
      case 'service-classes':
        return <ServiceClassesView />
      case 'lineage':
        return <LineageView />

      // ── Compute ─────────────────────────────────────────────────────────
      case 'nodes':
        return (
          <div className="space-y-6">
            <div className="flex justify-end">
              <Button className="font-mono" onClick={() => setProvisionWizardOpen(true)}>
                Provision Node
              </Button>
            </div>
            <ClusterNodesView />
          </div>
        )
      case 'racks':
        // Overview and editor both render the same zone→rack grid, so they are
        // sub-views rather than stacked one above the other.
        return (
          <Tabs defaultValue="overview" className="w-full">
            <TabsList className="font-mono mb-6">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="layout">Layout Editor</TabsTrigger>
            </TabsList>
            <TabsContent value="overview">
              <RackVisualization
                nodes={nodes}
                selectedNode={syncedSelectedNode}
                onSelectNode={setSelectedNode}
                selectedRack={selectedRack}
                onSelectRack={setSelectedRack}
              />
            </TabsContent>
            <TabsContent value="layout">
              <DragDropRackManager
                nodes={nodes}
                onNodesUpdate={setNodes}
                selectedNode={syncedSelectedNode}
                onSelectNode={setSelectedNode}
              />
            </TabsContent>
          </Tabs>
        )
      // ── Resources ───────────────────────────────────────────────────────
      case 'gpu':
        return <GPUMonitoringDashboard nodes={nodes} />
      case 'storage':
        return <ClusterStorageView />
      case 'network':
        return <NetworkView />
      case 'data-locality':
        return <WorkloadPlacementView />

      // ── Tuning ──────────────────────────────────────────────────────────
      case 'kv-cache':
        return <KVCacheDashboard />
      case 'elastic-pools':
        return <VolcanoPoolsView />
      case 'speculative':
        return <SpeculativeDecodingDashboard />
      case 'pipeline-pp':
        return <PipelinePPDashboard />
      case 'mps':
        return <MPSDashboard />
      case 'ptp':
        return <PTPSyncDashboard />
      case 'burst-buffer':
        return <BurstBufferDashboard />
      case 'ksm':
        return <KsmView />

      // ── Operations ──────────────────────────────────────────────────────
      case 'capacity':
        return <CapacityView />
      case 'resilience':
        return <ResilienceView />
      case 'events':
        return <ClusterEventsView />

      default:
        return null
    }
  }

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader className="border-b border-sidebar-border">
          <div className="flex items-center gap-2 px-2 py-1.5 group-data-[collapsible=icon]:px-0 group-data-[collapsible=icon]:justify-center">
            <Cpu size={22} className="text-primary shrink-0" weight="bold" />
            <div className="group-data-[collapsible=icon]:hidden min-w-0">
              <div className="font-mono font-bold text-primary leading-tight tracking-tight">FRAME</div>
              <div className="text-[10px] text-muted-foreground leading-tight truncate">
                Mainframe Framework for Kubernetes
              </div>
            </div>
          </div>
        </SidebarHeader>

        <SidebarContent>
          {NAV.map((group) => (
            <SidebarGroup key={group.label}>
              <SidebarGroupLabel className="font-mono text-[10px] uppercase tracking-widest">
                {group.label}
              </SidebarGroupLabel>
              <SidebarGroupContent>
                <SidebarMenu>
                  {group.items.map((item) => (
                    <SidebarMenuItem key={item.id}>
                      <SidebarMenuButton
                        isActive={screen === item.id}
                        tooltip={item.label}
                        onClick={() => setScreen(item.id)}
                        className="font-mono text-xs"
                      >
                        {item.icon}
                        <span>{item.label}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  ))}
                </SidebarMenu>
              </SidebarGroupContent>
            </SidebarGroup>
          ))}
        </SidebarContent>

        <SidebarRail />
      </Sidebar>

      <SidebarInset className="min-w-0 bg-background">
        {/* Page header — cluster stats live here on every screen, so no screen
            repeats them in its own body. */}
        <header className="sticky top-0 z-10 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80">
          <div className="flex items-center gap-3 px-4 sm:px-6 py-3">
            <SidebarTrigger className="shrink-0" />
            <div className="min-w-0 flex-1">
              <h1 className="font-mono text-lg font-bold text-primary leading-tight truncate">
                {active.label}
              </h1>
              <p className="text-xs text-muted-foreground leading-tight truncate">
                {active.description}
              </p>
            </div>
            <div className="hidden lg:block shrink-0">
              <ClusterStatsDashboard stats={stats} compact />
            </div>
          </div>
          {/* Stats wrap below the title on narrow viewports instead of vanishing */}
          <div className="lg:hidden px-4 sm:px-6 pb-3">
            <ClusterStatsDashboard stats={stats} compact />
          </div>
        </header>

        <div className="p-4 sm:p-6 min-w-0">{renderScreen()}</div>
      </SidebarInset>

      <NodeDetailPanel
        node={syncedSelectedNode}
        open={!!syncedSelectedNode}
        onClose={() => setSelectedNode(null)}
      />
      <NodeProvisionWizard
        open={provisionWizardOpen}
        onOpenChange={setProvisionWizardOpen}
        racks={racks}
        controlPlaneCount={controlPlaneCount}
        onNodeProvisioned={(node) => {
          setNodes((current) => [node, ...current])
          setSelectedNode(node)
        }}
      />
    </SidebarProvider>
  )
}


export default App
