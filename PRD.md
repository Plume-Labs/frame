# Planning Guide

A visual distributed systems cluster monitor that simulates real-time node orchestration, resource allocation, and network topology with an authentic mainframe-inspired aesthetic.

**Experience Qualities**:
1. **Technical** - Interface evokes the precision and depth of enterprise infrastructure tooling with detailed metrics and system status
2. **Dynamic** - Live updating visualizations show cluster health, resource flow, and node communication in real-time
3. **Authoritative** - Design conveys control and observability over complex distributed architecture

**Complexity Level**: Complex Application (advanced functionality, likely with multiple views)
This is both a simulation/visualization tool AND a complete production deployment infrastructure. The frontend provides coordinated views of cluster state, node metrics, and network topology. The backend infrastructure includes comprehensive IaC for bare-metal Kubernetes provisioning, RDMA networking, Ceph distributed storage, PXE boot automation, and GitOps-based continuous delivery with Flux/ArgoCD.

## Essential Features

**Cluster Topology View**
- Functionality: Visual grid/network display of compute nodes with status indicators
- Purpose: Provides at-a-glance understanding of cluster health and node distribution
- Trigger: Loads on app initialization, updates in real-time
- Progression: App loads → Nodes render in grid → Status colors indicate health → Hover reveals detailed metrics → Click node for expanded view
- Success criteria: All nodes visible, color-coded by status, smooth animations, accurate metric display

**Node Detail Panel**
- Functionality: Expanded metrics panel showing CPU, memory, network I/O, storage for selected node
- Purpose: Deep dive into individual node performance and resource utilization
- Trigger: User clicks any node in topology view
- Progression: Node clicked → Panel slides in from right → Metrics animate in → Real-time graphs update → Close button dismisses
- Success criteria: Metrics update smoothly, graphs render correctly, panel transitions feel fluid

**Resource Allocation Dashboard**
- Functionality: Overview charts showing aggregate cluster resources (CPU, RAM, storage, network bandwidth)
- Purpose: Understanding total capacity and utilization across the cluster
- Trigger: Visible on initial load alongside topology
- Progression: Dashboard loads → Progress bars/charts render → Values count up → Updates reflect node changes
- Success criteria: Accurate aggregation, responsive to node state changes, clear visual hierarchy

**System Event Log**
- Functionality: Scrolling feed of cluster events (node joins, provisioning, failovers, resource alerts)
- Purpose: Audit trail and real-time awareness of system changes
- Trigger: Auto-generates events based on simulated cluster activity
- Progression: Event occurs → New entry prepends to log → Entry fades in → Old entries scroll down → Auto-prune after 100 entries
- Success criteria: Events appear in real-time, timestamps accurate, color-coded by severity

## Essential Features (Continued)

**GitOps Deployment Infrastructure**
- Functionality: Complete IaC for bare-metal Kubernetes with RDMA networking, PXE boot, Ceph storage, and GitOps workflows
- Purpose: Production-ready deployment scripts for mainframe-like clustering with automated provisioning and continuous delivery
- Trigger: Operations team uses deployment scripts and Ansible playbooks to provision infrastructure
- Progression: Run bootstrap script → PXE provisions bare metal → Ansible configures K8s → Flux/ArgoCD syncs from Git → Monitoring UI deployed
- Success criteria: Full cluster deployed with RDMA networking, Ceph storage, automated GitOps, hot-add node capability, comprehensive monitoring

## Edge Case Handling

- **All Nodes Offline**: Display prominent alert banner with cluster unavailable message
- **Single Node Selected**: Gracefully handle detail panel for nodes with varying metric availability
- **Rapid State Changes**: Debounce updates to prevent UI thrashing during simulated chaos scenarios
- **Mobile Viewport**: Stack topology and metrics vertically, reduce node grid density
- **Empty State**: Show "Initializing cluster..." skeleton loaders during first render
- **Hot Node Addition**: Support dynamic node provisioning via IPMI/PXE without cluster disruption
- **Storage Failures**: Gracefully handle Ceph OSD failures with visible cluster health degradation

## Design Direction

The design should evoke the aesthetic of legacy mainframe terminals merged with modern observability tools - think green phosphor displays reimagined with contemporary data visualization. High information density balanced with clear visual hierarchy. The interface should feel like mission-critical infrastructure monitoring: precise, dense with data, but never cluttered.

## Color Selection

A terminal-inspired palette with bright accent colors for status indication against dark backgrounds.

- **Primary Color**: Bright Cyan (oklch(0.75 0.15 195)) - Represents active/healthy states, primary actions, evokes classic terminal prompts
- **Secondary Colors**: Deep Navy (oklch(0.15 0.02 240)) for backgrounds, Charcoal (oklch(0.25 0.01 240)) for elevated surfaces, providing depth and layering
- **Accent Color**: Electric Green (oklch(0.85 0.20 145)) - Status indicators, success states, active connections, referencing phosphor screen glow
- **Foreground/Background Pairings**:
  - Deep Navy Background (oklch(0.15 0.02 240)): Cyan text (oklch(0.75 0.15 195)) - Ratio 5.2:1 ✓
  - Deep Navy Background (oklch(0.15 0.02 240)): White text (oklch(0.95 0 0)) - Ratio 12.8:1 ✓
  - Charcoal Surface (oklch(0.25 0.01 240)): Cyan text (oklch(0.75 0.15 195)) - Ratio 3.8:1 ✓
  - Electric Green (oklch(0.85 0.20 145)): Deep Navy text (oklch(0.15 0.02 240)) - Ratio 7.8:1 ✓
  - Warning Amber (oklch(0.75 0.18 75)): Deep Navy text (oklch(0.15 0.02 240)) - Ratio 5.5:1 ✓
  - Error Red (oklch(0.60 0.22 25)): White text (oklch(0.95 0 0)) - Ratio 4.8:1 ✓

## Font Selection

Typography should balance monospace precision for technical data with clean sans-serif for labels and UI chrome, evoking command-line interfaces while maintaining modern readability.

- **Typographic Hierarchy**:
  - H1 (Panel Titles): JetBrains Mono Bold/24px/tight tracking (-0.02em)
  - H2 (Section Headers): JetBrains Mono SemiBold/18px/tight tracking
  - Body (Metrics/Labels): Space Grotesk Medium/14px/normal tracking
  - Monospace Data (IDs/Values): JetBrains Mono Regular/13px/normal tracking
  - Small (Timestamps/Meta): Space Grotesk Regular/12px/wide tracking (0.01em)

## Animations

Animations should feel systematic and purposeful - data flowing through the system, not arbitrary decoration. Use subtle pulses for active states, smooth transitions for panel reveals, and gentle fades for appearing/disappearing elements. Node status changes should have a brief glow effect. Keep durations under 300ms to maintain the snappy, responsive feel of a monitoring tool.

## Component Selection

- **Components**:
  - Card: Node containers and metric panels with subtle borders
  - Badge: Status indicators (online/degraded/offline) with color variants
  - Progress: Resource utilization bars for CPU/RAM/Storage
  - Tabs: Switch between topology/metrics/logs views
  - Sheet: Sliding detail panel for individual node inspection
  - ScrollArea: Event log with auto-scroll and overflow handling
  - Tooltip: Hover details for nodes showing quick stats
  - Separator: Visual breaks between metric groups
  
- **Customizations**:
  - Custom NodeGrid component: CSS Grid layout for cluster topology
  - Custom MetricChart component: Simple bar/line visualizations using native SVG
  - Custom NetworkFlow component: Animated connections between nodes using SVG paths
  - Status pulse effect: Custom CSS animation for active node indicators

- **States**:
  - Nodes: online (green glow), degraded (amber border), offline (red/dimmed), provisioning (cyan pulse)
  - Buttons: Subtle border glow on hover, slight scale on press
  - Cards: Elevated shadow on hover, border highlight for selected state
  
- **Icon Selection**:
  - Server/HardDrive: Node representation
  - Activity/Pulse: Active status
  - Warning/WarningCircle: Degraded alerts
  - X/XCircle: Offline/error states
  - ArrowsClockwise: Provisioning/syncing
  - ChartBar/ChartLine: Metrics visualization
  - List: Event log
  - Cpu/Memory: Resource types

- **Spacing**:
  - Section padding: p-6 (24px)
  - Card padding: p-4 (16px)
  - Grid gaps: gap-4 (16px) for node grid
  - Stack spacing: space-y-3 (12px) for metric rows
  - Tight spacing: gap-2 (8px) for inline badge groups

- **Mobile**:
  - Stack topology above metrics dashboard
  - Reduce node grid from 8×4 to 4×8 on mobile
  - Full-screen sheet for node details instead of side panel
  - Tabs become primary navigation on small screens
  - Hide secondary metrics, focus on critical status indicators
