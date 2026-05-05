# Planning Guide

Frame is a **mainframe framework for Kubernetes** — not merely a monitoring dashboard. It provides a complete operator control plane (REST API, TypeScript SDK, and UI) that lets operators, workloads, and CI pipelines manage jobs, scheduling policies, resource quotas, storage, networking, and resilience programmatically or through the interface.

> **Cluster scope:** Frame manages a **single local cluster** — one physical location, with one or more racks. The RDMA fabric (InfiniBand or RoCE) is a local, intra-datacenter interconnect and does **not** traverse the internet or WAN. `zones` and `racks` are failure-domain labels within the same site. Multi-site / multi-region federation is **out of scope** for this version.

**Experience Qualities**:
1. **Control-first** - The interface leads with operator actions: job submission, policy management, resource provisioning, and resilience controls. Observability is present but secondary.
2. **Programmable** - Every framework action available in the UI is also exposed through the REST API and TypeScript SDK so workloads and automation can interact without a human in the loop.
3. **Authoritative** - Design conveys ownership and control over complex distributed infrastructure, not just passive visibility into it.

**Complexity Level**: Complex Application (advanced functionality, multiple views, client/server split)
Frame is a full-stack mainframe framework: a React control-plane UI, an Express REST API server, a TypeScript operator SDK, and comprehensive IaC for bare-metal Kubernetes provisioning (RDMA networking, Ceph/MinIO storage, PXE boot, GitOps via Flux/ArgoCD, HPC scheduling via Volcano/YuniKorn, and Argo Workflow job orchestration).

## Essential Features

**Operator REST API & TypeScript SDK** *(primary)*
- Functionality: HTTP REST API (`server/index.ts`) exposing endpoints for job submission, scheduling policy management, resource quota control, and node inspection; TypeScript `FrameClient` SDK wrapping the API
- Purpose: Lets workloads, CI pipelines, and operator CLIs interact with Frame programmatically without the UI
- Trigger: Any client calls `POST /api/jobs`, `POST /api/scheduler/policies`, `PUT /api/resources/quotas/:ns`, etc.
- Progression: Client calls SDK → SDK calls REST API → API mutates in-memory/cluster state → Response returned with updated resource
- Success criteria: All CRUD operations complete with correct status codes; TypeScript types are accurate; full OpenAPI spec (`deploy/api/openapi.yaml`) documents every endpoint

**Job Orchestration Control Surface** *(primary)*
- Functionality: UI for submitting, monitoring, and cancelling Argo Workflow jobs; DAG visualisation; checkpoint/resume status
- Purpose: Operators submit training/inference jobs without writing Argo YAML manually
- Trigger: Active by default on app load (Jobs tab is the first tab)
- Progression: Operator opens UI → selects pipeline template → sets parameters → submits job → watches DAG progress
- Success criteria: Jobs visible in real-time; DAG nodes colour-coded by status; cancel action works

**Scheduling Policy Management** *(primary)*
- Functionality: Create/edit/delete PriorityClasses, PodGroups, and queue policies for Volcano or YuniKorn
- Purpose: Operators tune job priority, preemption, and fair-share without kubectl YAML
- Trigger: User opens Scheduler tab
- Success criteria: Policies persist; scheduler type (volcano/yunikorn/default) selectable; changes reflected in queue view

**Resource Provisioning** *(primary)*
- Functionality: Assign service classes (HIGH/MEDIUM/LOW) to nodes; set per-namespace GPU/CPU/memory quotas; configure MIG partitioning
- Purpose: Control workload placement and resource isolation
- Trigger: User opens Service Classes or GPU tabs
- Success criteria: Service class changes propagate to node cards; quota edits persist; MIG instance counts agree with migEnabled flag

**Cluster Topology View** *(secondary — management)*
- Functionality: Visual grid/network display of compute nodes with status indicators
- Purpose: Inspect and manage node state, rack placement, and zone health
- Trigger: User opens Nodes, Racks, or Zones tab
- Success criteria: All nodes visible, colour-coded by status, smooth animations, accurate metric display

**Observability** *(secondary — monitoring)*
- Functionality: Aggregate CPU/memory/network metrics, anomaly alerts, capacity forecasts, Prometheus/Grafana integration, Jaeger tracing, data lineage graph
- Purpose: Passive visibility into cluster health; not the primary interaction mode
- Trigger: User opens Observe, Lineage, or Events tab
- Success criteria: Metrics update smoothly; anomaly alerts fire at correct thresholds; lineage graph renders correctly

## Essential Features (Continued)

**GitOps Deployment Infrastructure**
- Functionality: Complete IaC for bare-metal Kubernetes with RDMA networking, PXE boot, Ceph/MinIO storage, HPC scheduling (Volcano/YuniKorn), Argo Workflows, and GitOps workflows
- Purpose: Production-ready deployment scripts for mainframe-like clustering with automated provisioning and continuous delivery
- Trigger: Operations team uses deployment scripts and Ansible playbooks to provision infrastructure
- Progression: Run bootstrap script → PXE provisions bare metal → Ansible configures K8s → Flux/ArgoCD syncs from Git → Frame API + UI deployed
- Success criteria: Full cluster deployed with RDMA networking, Ceph/MinIO storage, automated GitOps, hot-add node capability, HPC scheduler, and Frame API server running

## Edge Case Handling

- **API Server Unavailable**: UI falls back to simulated data; banner indicates API is offline
- **All Nodes Offline**: Display prominent alert banner with cluster unavailable message
- **Job Submission Validation**: API returns 400 with clear error if required fields missing; UI shows inline validation
- **Policy Conflict**: If a new policy conflicts with an existing queue, API returns descriptive error; UI surfaces it inline
- **Single Node Selected**: Gracefully handle detail panel for nodes with varying metric availability
- **Rapid State Changes**: Debounce updates to prevent UI thrashing during simulated chaos scenarios
- **Mobile Viewport**: Stack topology and metrics vertically, reduce node grid density
- **Empty State**: Show "Initializing cluster..." skeleton loaders during first render
- **Hot Node Addition**: Support dynamic node provisioning via IPMI/PXE without cluster disruption
- **Storage Failures**: Gracefully handle Ceph OSD failures with visible cluster health degradation
- **Multi-site Request**: Out of scope — Frame manages a single local cluster. If a user requests multi-region federation, surface a clear message that it is not yet supported.

## Design Direction

The design should evoke the aesthetic of legacy mainframe terminals merged with modern infrastructure control panels — think green phosphor displays reimagined with contemporary data visualisation. High information density balanced with clear visual hierarchy. The interface should feel like mission-critical **control** infrastructure: precise, dense with data, but never cluttered. Actions (submit, cancel, apply policy) should be visually prominent; monitoring views are present but visually subordinate.

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
