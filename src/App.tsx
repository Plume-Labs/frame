import { lazy, ReactNode, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { NavigationContext } from '@/hooks/useNavigation'
import { ClusterNode } from '@/lib/types'
import { useClusterSimulation } from '@/hooks/useClusterSimulation'
import { currentSession, ensureToken, identityFromToken, isAdminToken, logout, onSessionLost, type Session } from '@/lib/auth'
import { inviteTokenFromLocation } from '@/lib/accounts'
import { loadConfig } from '@/lib/frame-config'

import { NodeDetailPanel } from '@/components/NodeDetailPanel'
import { PasskeysDialog } from '@/components/PasskeysDialog'
import { HeaderStats } from '@/components/HeaderStats'
import { NotEnabledView } from '@/components/NotEnabledView'
import { LoginView } from '@/components/LoginView'
import { InviteAcceptView } from '@/components/InviteAcceptView'

// Lazy: only the active screen is ever mounted (see renderScreen below), so
// eagerly importing all 20+ of them bundled every one into the initial
// chunk for nothing — dynamic import() gives each its own chunk, fetched
// only when its nav item is actually clicked.
const OverviewView = lazy(() => import('@/components/OverviewView').then((m) => ({ default: m.OverviewView })))
const ClusterNodesView = lazy(() => import('@/components/ClusterNodesView').then((m) => ({ default: m.ClusterNodesView })))
const FrameNodesView = lazy(() => import('@/components/FrameNodesView').then((m) => ({ default: m.FrameNodesView })))
const RacksView = lazy(() => import('@/components/RacksView').then((m) => ({ default: m.RacksView })))
const HardwareView = lazy(() => import('@/components/hardware/HardwareView').then((m) => ({ default: m.HardwareView })))
const InstallsTab = lazy(() => import('@/components/hardware/InstallsTab').then((m) => ({ default: m.InstallsTab })))
const WorkloadsView = lazy(() => import('@/components/workloads/WorkloadsView').then((m) => ({ default: m.WorkloadsView })))
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
const TasksView = lazy(() => import('@/components/TasksView').then((m) => ({ default: m.TasksView })))
const AccountsView = lazy(() => import('@/components/AccountsView').then((m) => ({ default: m.AccountsView })))
const SettingsView = lazy(() => import('@/components/SettingsView').then((m) => ({ default: m.SettingsView })))

import { Toaster } from '@/components/ui/sonner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarFooter,
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
  ComputerTower,
  Cpu,
  Database,
  Detective,
  Fingerprint,
  Gauge,
  Gear,
  HardDrives,
  ListChecks,
  Network,
  Package,
  Queue,
  SignOut,
  Speedometer,
  SquaresFour,
  TreeStructure,
  Users,
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
  | 'workloads'
  | 'applications'
  | 'jobs'
  | 'lineage'
  | 'scheduler'
  | 'service-classes'
  | 'elastic-pools'
  | 'nodes'
  | 'provisioned-nodes'
  | 'racks'
  | 'hardware'
  | 'installs'
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
  | 'tasks'
  | 'accounts'
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
        id: 'workloads',
        label: 'Workloads',
        icon: <TreeStructure />,
        description: 'Every workload on the cluster — logs, a shell, restart, scale and the manifest',
        tabs: [{ id: 'workloads', label: 'Workloads' }],
      },
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
      {
        id: 'hardware',
        label: 'Hardware',
        icon: <ComputerTower />,
        description: 'Physical chassis over Redfish: inventory, sensors, event log and power',
        tabs: [
          { id: 'hardware', label: 'Machines' },
          { id: 'installs', label: 'Installs' },
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
        id: 'tasks',
        label: 'Tasks',
        icon: <ListChecks />,
        description: 'Every write made through the UI, and how it ended',
        tabs: [{ id: 'tasks', label: 'Tasks' }],
      },
      {
        id: 'accounts',
        label: 'Accounts',
        icon: <Users />,
        description: 'Who can sign in, with what rights, and on which keys',
        tabs: [{ id: 'accounts', label: 'Accounts' }],
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

/** `checking` while the initial `currentSession()` call is in flight. */
type SessionState =
  | { phase: 'checking' }
  | { phase: 'signed-out' }
  | { phase: 'signed-in'; session: Session }

function App() {
  // Session gate: the console must not mount — and so must not fire a single
  // /api/ request — until authd has confirmed a signed-in session. Checked
  // once on mount; the early returns below keep the rest of the component
  // (Sidebar, HeaderStats, the screens) from ever being part of the render
  // tree while this is 'checking' or 'signed-out'.
  const [sessionState, setSessionState] = useState<SessionState>({ phase: 'checking' })

  useEffect(() => {
    let cancelled = false
    currentSession()
      .then((session) => {
        if (cancelled) return
        setSessionState(session ? { phase: 'signed-in', session } : { phase: 'signed-out' })
      })
      .catch(() => {
        // authd unreachable, or some other non-401 failure: there is no way
        // to confirm a session, so fall back to the login screen rather than
        // rendering the app on an unproven guess.
        if (!cancelled) setSessionState({ phase: 'signed-out' })
      })
    return () => {
      cancelled = true
    }
  }, [])

  // Keeps the bearer token from going stale for the life of a signed-in tab.
  // Gated on `signed-in` rather than running unconditionally: at
  // 'checking'/'signed-out' there is no session to refresh, and firing this
  // anyway would just draw a pointless 401 from /auth/token every five
  // minutes at the login screen. The dependency is the phase string, not the
  // whole sessionState object, so this effect only re-runs on an actual
  // phase transition — never on a re-render — which is what keeps a second
  // interval from ever stacking on top of this one; the cleanup is what
  // stops it on unmount or on sign-out.
  useEffect(() => {
    if (sessionState.phase !== 'signed-in') return
    const id = setInterval(() => {
      void ensureToken()
    }, 5 * 60_000)
    return () => clearInterval(id)
  }, [sessionState.phase])

  // Runtime config, loaded once the session exists and before any screen
  // mounts. It used to run in `main.tsx`, before `<App/>` and so outside this
  // gate — an apiserver read with no bearer token, which 401s on every page
  // load including the one that renders the login screen, leaving the console
  // silently on DEFAULT_CONFIG (whole-branch review, "Also fix").
  //
  // The property the old placement protected still holds: no screen renders,
  // and so no SDK call fires, until this has landed. `loadConfig` never
  // rejects — a missing ConfigMap or an RBAC denial leaves the compiled
  // defaults in force and records why for the Settings screen — so there is
  // no failure branch to handle here, only a wait.
  const [configLoaded, setConfigLoaded] = useState(false)
  useEffect(() => {
    if (sessionState.phase !== 'signed-in') return
    let cancelled = false
    void loadConfig().then(() => {
      if (!cancelled) setConfigLoaded(true)
    })
    return () => {
      cancelled = true
    }
  }, [sessionState.phase])

  // Back to the gate when the session goes, rather than leaving every screen
  // to accumulate 401s. `auth.ts` fires this when a session that existed
  // stops existing — the 12h cookie lapsing is the ordinary case, and it
  // happens on the refresh above or on the SDK's own retry after a 401,
  // whichever notices first (whole-branch review, I1).
  useEffect(() => onSessionLost(() => setSessionState({ phase: 'signed-out' })), [])

  const [selectedNode, setSelectedNode] = useState<ClusterNode | null>(null)
  const [screen, setScreen] = useState('overview')
  // Set when a navigation targets a specific tab; consumed by the Tabs below so
  // "3 nodes not ready" can land on Nodes › Fleet rather than Nodes' first tab.
  const [pendingTab, setPendingTab] = useState<string | undefined>(undefined)

  const navigate = useCallback((next: string, tab?: string) => {
    setScreen(next)
    setPendingTab(tab)
  }, [])
  const [passkeysOpen, setPasskeysOpen] = useState(false)

  const { nodes } = useClusterSimulation(32)

  // Derive the selected node from the authoritative nodes array so the detail
  // panel always shows up-to-date metrics without an extra state update cycle.
  const syncedSelectedNode = useMemo(
    () => (selectedNode ? (nodes.find((n) => n.id === selectedNode.id) ?? null) : null),
    [nodes, selectedNode],
  )

  // The nav gate is a courtesy, not a control: the token is decoded, not
  // verified (see identityFromToken), and every write the screen makes is
  // refused server-side for a non-admin regardless. It also does not
  // re-evaluate mid-session — the five-minute refresh replaces the token
  // without touching sessionState — so a demotion shows up at the next sign
  // in, while its effect is immediate at the apiserver.
  const admin = useMemo(
    () => (sessionState.phase === 'signed-in' ? isAdminToken(sessionState.session.token) : false),
    [sessionState],
  )
  // Who AccountsView is allowed to say "you" about, so it can decline to
  // offer a demotion or a disable that would strand the signed-in admin
  // (see isOnlyEnabledAdmin in @/lib/accounts). Same decode, same trust level
  // as `admin` above — a courtesy, not a security boundary.
  const selfEmail = useMemo(
    () => (sessionState.phase === 'signed-in' ? identityFromToken(sessionState.session.token)?.email : undefined),
    [sessionState],
  )
  const visibleNav = useMemo(
    () =>
      NAV.map((group) => ({ ...group, items: group.items.filter((i) => i.id !== 'accounts' || admin) }))
        .filter((group) => group.items.length > 0),
    [admin],
  )
  // State, not a memo: InviteAcceptView's designed ending has no session to
  // transition on (see its module doc), so nothing else naturally lifts this
  // gate. `onFinished` below clears it explicitly once the invitee is done
  // reading that screen — a memoized `[]` value never would have.
  const [inviteToken, setInviteToken] = useState(() =>
    inviteTokenFromLocation(globalThis.location.pathname, globalThis.location.hash),
  )

  if (sessionState.phase === 'checking') {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background">
        <div className="font-mono text-sm text-muted-foreground">Loading…</div>
      </div>
    )
  }

  if (inviteToken && sessionState.phase !== 'signed-in') {
    return (
      <InviteAcceptView
        token={inviteToken}
        onEnrolled={(session) => setSessionState({ phase: 'signed-in', session })}
        onFinished={() => setInviteToken(undefined)}
      />
    )
  }

  if (sessionState.phase === 'signed-out') {
    return (
      <LoginView onSignedIn={(session) => setSessionState({ phase: 'signed-in', session })} />
    )
  }

  if (!configLoaded) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background">
        <div className="font-mono text-sm text-muted-foreground">Loading…</div>
      </div>
    )
  }

  const active = NAV_INDEX[screen] ?? NAV_INDEX['overview']

  // Only the active tab of the active screen is mounted. Radix keeps inactive
  // TabsContent unmounted, so this holds at both levels.
  function renderTab(tab: TabId): ReactNode {
    switch (tab) {
      case 'overview':
        return <OverviewView />

      // ── Workloads ───────────────────────────────────────────────────────
      case 'workloads':
        return <WorkloadsView />
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
        return <ClusterNodesView />
      case 'provisioned-nodes':
        return <FrameNodesView />
      case 'racks':
        return <RacksView />
      case 'hardware':
        return <HardwareView />
      case 'installs':
        return <InstallsTab />
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
      case 'tasks':
        return <TasksView />
      case 'accounts':
        // Unreachable today: navigation is component state (`screen`), not
        // URL routing, and `visibleNav` already excludes this entry for a
        // non-admin. Gated anyway as a regression guard for the day
        // something drives `screen` from outside the sidebar's own clicks —
        // a deep link, browser history, a future router — since the real
        // authorization is server-side regardless of what renders here.
        return admin ? <AccountsView currentEmail={selfEmail} /> : null
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
          {visibleNav.map((group) => (
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

        <SidebarFooter className="border-t border-sidebar-border">
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip="Passkeys"
                className="font-mono text-xs"
                onClick={() => setPasskeysOpen(true)}
              >
                <Fingerprint />
                <span>Passkeys</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <SidebarMenuButton
                tooltip="Sign out"
                className="font-mono text-xs"
                onClick={() => {
                  // logout() clears the in-memory token in its own `finally`
                  // even when the network call to authd fails — it re-throws
                  // in that case, but the token is already gone by then. The
                  // UI must follow it to signed-out regardless of what authd
                  // said: the user asked to leave, and staying on
                  // 'signed-in' with no token would just mean every live
                  // screen starts 401ing instead of showing the login gate.
                  void logout()
                    .catch(() => {})
                    .finally(() => setSessionState({ phase: 'signed-out' }))
                }}
              >
                <SignOut />
                <span>Sign out</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>

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
      <PasskeysDialog open={passkeysOpen} onOpenChange={setPasskeysOpen} />
      <Toaster position="bottom-right" />
    </SidebarProvider>
    </NavigationContext.Provider>
  )
}


export default App
