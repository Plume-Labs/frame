import { lazy, ReactNode, Suspense, useCallback, useMemo, useState } from 'react'
import { NavigationContext } from '@/hooks/useNavigation'
import { ClusterNode } from '@/lib/types'
import { useClusterSimulation } from '@/hooks/useClusterSimulation'

import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { NodeProvisionWizard } from '@/components/NodeProvisionWizard'
import { HeaderStats } from '@/components/HeaderStats'
import { NotEnabledView } from '@/components/NotEnabledView'

// Lazy: only the active screen is ever mounted (see renderScreen below), so
// eagerly importing all 20+ of them bundled every one into the initial
// chunk for nothing — dynamic import() gives each its own chunk, fetched
// only when its nav item is actually clicked.
const OverviewView = lazy(() => import('@/components/OverviewView').then((m) => ({ default: m.OverviewView })))
const ClusterNodesView = lazy(() => import('@/components/ClusterNodesView').then((m) => ({ default: m.ClusterNodesView })))
const FrameNodesView = lazy(() => import('@/components/FrameNodesView').then((m) => ({ default: m.FrameNodesView })))
const RacksView = lazy(() => import('@/components/RacksView').then((m) => ({ default: m.RacksView })))
const ApplicationsView = lazy(() => import('@/components/ApplicationsView').then((m) => ({ default: m.ApplicationsView })))
const FrameJobsView = lazy(() => import('@/components/FrameJobsView').then((m) => ({ default: m.FrameJobsView })))
const FrameSchedulerView = lazy(() => import('@/components/FrameSchedulerView').then((m) => ({ default: m.FrameSchedulerView })))
const ServiceClassesView = lazy(() => import('@/components/ServiceClassesView').then((m) => ({ default: m.ServiceClassesView })))
const LineageView = lazy(() => import('@/components/LineageView').then((m) => ({ default: m.LineageView })))
const GpuView = lazy(() => import('@/components/GpuView').then((m) => ({ default: m.GpuView })))
const ClusterStorageView = lazy(() => import('@/components/ClusterStorageView').then((m) => ({ default: m.ClusterStorageView })))
const NetworkView = lazy(() => import('@/components/NetworkView').then((m) => ({ default: m.NetworkView })))
const WorkloadPlacementView = lazy(() => import('@/components/WorkloadPlacementView').then((m) => ({ default: m.WorkloadPlacementView })))
const InferenceOverviewView = lazy(() => import('@/components/InferenceOverviewView').then((m) => ({ default: m.InferenceOverviewView })))
const InferenceView = lazy(() => import('@/components/InferenceView').then((m) => ({ default: m.InferenceView })))
const VolcanoPoolsView = lazy(() => import('@/components/VolcanoPoolsView').then((m) => ({ default: m.VolcanoPoolsView })))
const MpsView = lazy(() => import('@/components/MpsView').then((m) => ({ default: m.MpsView })))
const PtpView = lazy(() => import('@/components/PtpView').then((m) => ({ default: m.PtpView })))
const BurstBufferView = lazy(() => import('@/components/BurstBufferView').then((m) => ({ default: m.BurstBufferView })))
const KsmView = lazy(() => import('@/components/KsmView').then((m) => ({ default: m.KsmView })))
const CapacityView = lazy(() => import('@/components/CapacityView').then((m) => ({ default: m.CapacityView })))
const ResilienceView = lazy(() => import('@/components/ResilienceView').then((m) => ({ default: m.ResilienceView })))
const SecurityView = lazy(() => import('@/components/SecurityView').then((m) => ({ default: m.SecurityView })))
const AlertsView = lazy(() => import('@/components/AlertsView').then((m) => ({ default: m.AlertsView })))
const ClusterEventsView = lazy(() => import('@/components/ClusterEventsView').then((m) => ({ default: m.ClusterEventsView })))
const SettingsView = lazy(() => import('@/components/SettingsView').then((m) => ({ default: m.SettingsView })))

import { Button } from '@/components/ui/button'
import { Toaster } from '@/components/ui/sonner'
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
  Calendar,
  ChartBar,
  ChartLine,
  Cpu,
  Database,
  Detective,
  Gauge,
  Gear,
  HardDrives,
  Network,
  Package,
  Queue,
  Speedometer,
  SquaresFour,
} from '@phosphor-icons/react'

/**
 * Navigation model.
 *
 * Two levels. The sidebar carries screens grouped by the question each answers —
 * what runs (Workloads), what it runs on (Compute), what it consumes
 * (Resources), how it is tuned (Tuning), and how it is behaving (Operations).
 * Within a screen, panels that answer the *same* question sit on tabs.
 *
 * The sidebar was 28 flat entries, most of them one-card panels (MPS, KSM, PTP,
 * burst buffer) or capabilities this cluster cannot run at all (speculative
 * decoding, pipeline parallelism). Folding those into tabs cuts the sidebar to
 * 13 without hiding anything: every former entry is still one click away.
 */
/**
 * Every panel the app can render. Typing it as a union — rather than `string` —
 * makes the nav table and `renderTab` check each other: a typo in NAV fails to
 * assign, and a panel with no `case` trips the exhaustiveness guard.
 */
type TabId =
  | 'overview'
  | 'applications'
  | 'jobs'
  | 'lineage'
  | 'scheduler'
  | 'service-classes'
  | 'elastic-pools'
  | 'nodes'
  | 'provisioned-nodes'
  | 'racks'
  | 'gpu'
  | 'storage'
  | 'network'
  | 'data-locality'
  | 'inference'
  | 'kv-cache'
  | 'speculative'
  | 'pipeline-pp'
  | 'mps'
  | 'ptp'
  | 'burst-buffer'
  | 'ksm'
  | 'capacity'
  | 'resilience'
  | 'security'
  | 'alerts'
  | 'events'
  | 'settings'

interface NavTab {
  id: TabId
  label: string
}

interface NavItem {
  id: string
  label: string
  icon: ReactNode
  /** Shown under the page title; explains what the screen is for. */
  description: string
  /** A single tab renders bare — no tab strip is drawn for it. */
  tabs: NavTab[]
}

interface NavGroup {
  label: string
  items: NavItem[]
}

const NAV: NavGroup[] = [
  {
    label: 'Start',
    items: [
      {
        id: 'overview',
        label: 'Overview',
        icon: <SquaresFour />,
        description: 'What needs attention, where the cluster is heading, what it is serving',
        tabs: [{ id: 'overview', label: 'Overview' }],
      },
    ],
  },
  {
    label: 'Workloads',
    items: [
      {
        id: 'applications',
        label: 'Applications',
        icon: <Package />,
        description: 'Deployed apps read live from the cluster',
        tabs: [{ id: 'applications', label: 'Applications' }],
      },
      {
        id: 'jobs',
        label: 'Jobs',
        icon: <Queue />,
        description: 'Workflow DAGs, queue depth, checkpoints and their Argo run traces',
        tabs: [
          { id: 'jobs', label: 'Queue' },
          { id: 'lineage', label: 'Lineage' },
        ],
      },
      {
        id: 'scheduling',
        label: 'Scheduling',
        icon: <Calendar />,
        description: 'Policies, service-class SLAs and the Volcano queues they land in',
        tabs: [
          { id: 'scheduler', label: 'Policies' },
          { id: 'service-classes', label: 'Service Classes' },
          { id: 'elastic-pools', label: 'Elastic Pools' },
        ],
      },
    ],
  },
  {
    label: 'Compute',
    items: [
      {
        id: 'nodes',
        label: 'Nodes',
        icon: <Cpu />,
        description: 'Fleet health, operator-managed FrameNodes and their hypervisor racks',
        tabs: [
          { id: 'nodes', label: 'Fleet' },
          { id: 'provisioned-nodes', label: 'Provisioned' },
          { id: 'racks', label: 'Racks' },
        ],
      },
    ],
  },
  {
    label: 'Resources',
    items: [
      {
        id: 'gpu',
        label: 'GPU',
        icon: <Speedometer />,
        description: 'Live GPU telemetry (DCGM) — util, memory, temp, power',
        tabs: [{ id: 'gpu', label: 'GPU' }],
      },
      {
        id: 'storage',
        label: 'Storage',
        icon: <Database />,
        description: 'Live Ceph health, OSDs, capacity and pools',
        tabs: [{ id: 'storage', label: 'Storage' }],
      },
      {
        id: 'network',
        label: 'Network',
        icon: <Network />,
        description: 'Live per-node NIC throughput',
        tabs: [{ id: 'network', label: 'Network' }],
      },
      {
        id: 'data-locality',
        label: 'Placement',
        icon: <HardDrives />,
        description: 'Live pod-to-node workload placement',
        tabs: [{ id: 'data-locality', label: 'Placement' }],
      },
    ],
  },
  {
    label: 'Tuning',
    items: [
      {
        id: 'inference',
        label: 'Inference',
        icon: <ChartBar />,
        description: 'Serving status, KV-cache pressure and the decode strategies in play',
        tabs: [
          { id: 'inference', label: 'Overview' },
          { id: 'kv-cache', label: 'KV-Cache' },
          { id: 'speculative', label: 'Speculative' },
          { id: 'pipeline-pp', label: 'Pipeline PP' },
        ],
      },
      {
        id: 'node-tuning',
        label: 'Node Tuning',
        icon: <Gauge />,
        description: 'Per-node knobs — GPU sharing, clock sync, scratch tier, page merging',
        tabs: [
          { id: 'mps', label: 'MPS' },
          { id: 'ptp', label: 'PTP Sync' },
          { id: 'burst-buffer', label: 'Burst Buffer' },
          { id: 'ksm', label: 'KSM' },
        ],
      },
    ],
  },
  {
    label: 'Operations',
    items: [
      {
        id: 'capacity',
        label: 'Capacity',
        icon: <ChartLine />,
        description: 'Allocatable vs used vs reserved, and how much disruption the cluster absorbs',
        tabs: [
          { id: 'capacity', label: 'Capacity' },
          { id: 'resilience', label: 'Resilience' },
        ],
      },
      {
        id: 'security',
        label: 'Security',
        icon: <Detective />,
        description: 'Runtime and posture findings, firing alerts and the raw cluster event feed',
        tabs: [
          { id: 'security', label: 'Runtime & Posture' },
          { id: 'alerts', label: 'Alerts' },
          { id: 'events', label: 'Events' },
        ],
      },
      {
        id: 'settings',
        label: 'Settings',
        icon: <Gear />,
        description: 'Namespaces, selectors and ports of every integration',
        tabs: [{ id: 'settings', label: 'Settings' }],
      },
    ],
  },
]

const NAV_INDEX: Record<string, NavItem> = Object.fromEntries(
  NAV.flatMap((group) => group.items).map((item) => [item.id, item]),
)

function App() {
  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [screen, setScreen] = useState('overview')
  // Set when a navigation targets a specific tab; consumed by the Tabs below so
  // "3 nodes not ready" can land on Nodes › Fleet rather than Nodes' first tab.
  const [pendingTab, setPendingTab] = useState<string | undefined>(undefined)

  const navigate = useCallback((next: string, tab?: string) => {
    setScreen(next)
    setPendingTab(tab)
  }, [])
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

  const active = NAV_INDEX[screen] ?? NAV_INDEX['overview']

  // Only the active tab of the active screen is mounted. Radix keeps inactive
  // TabsContent unmounted, so this holds at both levels.
  function renderTab(tab: TabId): ReactNode {
    switch (tab) {
      case 'overview':
        return <OverviewView />

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
      case 'provisioned-nodes':
        return <FrameNodesView />
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
      case 'settings':
        return <SettingsView />

      default: {
        // Compile error if a TabId above gains a member with no case here.
        const unhandled: never = tab
        return unhandled
      }
    }
  }

  function renderScreen(): ReactNode {
    if (active.tabs.length === 1) return renderTab(active.tabs[0].id)

    return (
      // key includes pendingTab so a navigation that names a tab re-mounts the
      // strip onto it; without that the uncontrolled defaultValue is ignored on
      // a screen that is already open.
      <Tabs
        key={`${active.id}:${pendingTab ?? ''}`}
        defaultValue={
          pendingTab && active.tabs.some((t) => t.id === pendingTab) ? pendingTab : active.tabs[0].id
        }
        className="gap-4"
      >
        <TabsList className="flex-wrap h-auto">
          {active.tabs.map((tab) => (
            <TabsTrigger key={tab.id} value={tab.id} className="font-mono text-xs">
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>
        {active.tabs.map((tab) => (
          <TabsContent key={tab.id} value={tab.id}>
            {renderTab(tab.id)}
          </TabsContent>
        ))}
      </Tabs>
    )
  }

  return (
    <NavigationContext.Provider value={{ screen, navigate, pendingTab }}>
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
                        onClick={() => navigate(item.id)}
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

        <div className="p-4 sm:p-6 min-w-0">
          <Suspense
            fallback={
              <div className="py-10 text-center font-mono text-sm text-muted-foreground">Loading…</div>
            }
          >
            {renderScreen()}
          </Suspense>
        </div>
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
    </NavigationContext.Provider>
  )
}


export default App
