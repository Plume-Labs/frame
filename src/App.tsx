import { ReactNode, useMemo, useState } from 'react'
import { ClusterNode } from '@/lib/types'
import { useClusterSimulation } from '@/hooks/useClusterSimulation'

import { ClusterNodesView } from '@/components/ClusterNodesView'
import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { NodeProvisionWizard } from '@/components/NodeProvisionWizard'
import { HeaderStats } from '@/components/HeaderStats'
import { RacksView } from '@/components/RacksView'

import { ApplicationsView } from '@/components/ApplicationsView'
import { FrameJobsView } from '@/components/FrameJobsView'
import { FrameSchedulerView } from '@/components/FrameSchedulerView'
import { ServiceClassesView } from '@/components/ServiceClassesView'
import { LineageView } from '@/components/LineageView'

import { GpuView } from '@/components/GpuView'
import { ClusterStorageView } from '@/components/ClusterStorageView'
import { NetworkView } from '@/components/NetworkView'
import { WorkloadPlacementView } from '@/components/WorkloadPlacementView'

import { InferenceOverviewView } from '@/components/InferenceOverviewView'
import { InferenceView } from '@/components/InferenceView'
import { VolcanoPoolsView } from '@/components/VolcanoPoolsView'
import { MpsView } from '@/components/MpsView'
import { NotEnabledView } from '@/components/NotEnabledView'
import { PtpView } from '@/components/PtpView'
import { BurstBufferView } from '@/components/BurstBufferView'
import { KsmView } from '@/components/KsmView'

import { CapacityView } from '@/components/CapacityView'
import { ResilienceView } from '@/components/ResilienceView'
import { SecurityView } from '@/components/SecurityView'
import { AlertsView } from '@/components/AlertsView'
import { ClusterEventsView } from '@/components/ClusterEventsView'

import { Button } from '@/components/ui/button'
import { Toaster } from '@/components/ui/sonner'
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
  Bell,
  ArrowsLeftRight,
  Calendar,
  ChartBar,
  ChartLine,
  Clock,
  Cpu,
  Database,
  Detective,
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
      { id: 'racks', label: 'Racks', icon: <Stack />, description: 'Nodes grouped by real hypervisor host (capacity, oversubscription)' },
    ],
  },
  {
    label: 'Resources',
    items: [
      { id: 'gpu', label: 'GPU', icon: <Speedometer />, description: 'Live GPU telemetry (DCGM) — util, memory, temp, power' },
      { id: 'storage', label: 'Storage', icon: <Database />, description: 'Live Ceph health, OSDs, capacity and pools' },
      { id: 'network', label: 'Network', icon: <Network />, description: 'Live per-node NIC throughput' },
      { id: 'data-locality', label: 'Placement', icon: <HardDrives />, description: 'Live pod-to-node workload placement' },
    ],
  },
  {
    label: 'Tuning',
    items: [
      { id: 'inference', label: 'Inference', icon: <ChartBar />, description: 'GPU + llama.cpp + TEI serving status, aggregated' },
      { id: 'kv-cache', label: 'KV-Cache', icon: <Lightning />, description: 'Live KV-cache depth and inference throughput (llama.cpp)' },
      { id: 'elastic-pools', label: 'Elastic Pools', icon: <ArrowsLeftRight />, description: 'Live Volcano queues and gang-scheduled PodGroups' },
      { id: 'speculative', label: 'Speculative', icon: <Shuffle />, description: 'Draft-model speculative decoding (not enabled)' },
      { id: 'pipeline-pp', label: 'Pipeline PP', icon: <Waveform />, description: 'Pipeline parallelism (N/A — single GPU)' },
      { id: 'mps', label: 'MPS', icon: <Gauge />, description: 'Live GPU sharing status (MPS off — exclusive)' },
      { id: 'ptp', label: 'PTP Sync', icon: <Clock />, description: 'Live clock sync (ptp_kvm + adjtimex)' },
      { id: 'burst-buffer', label: 'Burst Buffer', icon: <HardDrive />, description: 'Live SSD scratch tier per node (node-exporter fs)' },
      { id: 'ksm', label: 'KSM', icon: <Archive />, description: 'Live kernel same-page merging (node-exporter)' },
    ],
  },
  {
    label: 'Operations',
    items: [
      { id: 'capacity', label: 'Capacity', icon: <ChartLine />, description: 'Live allocatable vs used vs reserved' },
      { id: 'resilience', label: 'Resilience', icon: <ShieldWarning />, description: 'Durability, disruption budgets, restarts + Velero backups' },
      { id: 'security', label: 'Security', icon: <Detective />, description: 'Runtime (Falco) + posture (trivy) + network (Tetragon)' },
      { id: 'alerts', label: 'Alerts', icon: <Bell />, description: 'Active Alertmanager alerts (rules + Falco)' },
      { id: 'events', label: 'Events', icon: <Info />, description: 'Cluster event feed' },
    ],
  },
]

const NAV_INDEX: Record<string, NavItem> = Object.fromEntries(
  NAV.flatMap((group) => group.items).map((item) => [item.id, item]),
)

function App() {
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
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
        return <RacksView />
      // ── Resources ───────────────────────────────────────────────────────
      case 'gpu':
        return <GpuView />
      case 'storage':
        return <ClusterStorageView />
      case 'network':
        return <NetworkView />
      case 'data-locality':
        return <WorkloadPlacementView />

      // ── Tuning ──────────────────────────────────────────────────────────
      case 'inference':
        return <InferenceOverviewView />
      case 'kv-cache':
        return <InferenceView />
      case 'elastic-pools':
        return <VolcanoPoolsView />
      case 'speculative':
        return (
          <NotEnabledView
            title="Speculative decoding"
            reason="Draft-model speculative decoding is available in the llama.cpp server but no draft model is loaded on this deployment, so no accept-rate telemetry is produced."
            enable="llama-server --model <target> --model-draft <small-draft> --draft-max 16"
          />
        )
      case 'pipeline-pp':
        return (
          <NotEnabledView
            title="Pipeline parallelism"
            reason="Pipeline parallelism splits a model across multiple GPUs. This cluster has a single GPU (Tesla P4 on neura-k3s-w2), so PP is not applicable — the model runs fully on one device."
            enable="Add GPUs across nodes, then serve with tensor/pipeline-parallel size > 1."
          />
        )
      case 'mps':
        return <MpsView />
      case 'ptp':
        return <PtpView />
      case 'burst-buffer':
        return <BurstBufferView />
      case 'ksm':
        return <KsmView />

      // ── Operations ──────────────────────────────────────────────────────
      case 'capacity':
        return <CapacityView />
      case 'resilience':
        return <ResilienceView />
      case 'security':
        return <SecurityView />
      case 'alerts':
        return <AlertsView />
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
              <HeaderStats />
            </div>
          </div>
          {/* Stats wrap below the title on narrow viewports instead of vanishing */}
          <div className="lg:hidden px-4 sm:px-6 pb-3">
            <HeaderStats />
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
      <Toaster position="bottom-right" />
    </SidebarProvider>
  )
}


export default App
