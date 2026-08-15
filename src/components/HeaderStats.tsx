import { CapacityResource, ClusterNodeInfo, coreListPath, createFrameClient } from '@/lib/frame-sdk'
import { useLiveResource } from '@/hooks/useLiveResource'

const frame = createFrameClient()

/** Compact live cluster summary for the page header — real nodes + CPU/mem usage. */
export function HeaderStats() {
  const { state } = useLiveResource<{ nodes: ClusterNodeInfo[]; cap: CapacityResource[] }>(
    async () => {
      const [nodes, cap] = await Promise.all([frame.cluster.nodes(), frame.cluster.capacity()])
      return { nodes, cap }
    },
    [],
    // Watch nodes only. `capacity()` reads pods too, but this component is
    // mounted on every screen, and watching pods cluster-wide meant any pod
    // event anywhere — a cronjob, an Argo step, a restarting probe — re-ran a
    // full unfiltered pod list (3.7 MB, ~1 s of apiserver time) behind the
    // header. On a cluster that always has something churning, that was
    // continuous. Nodes are the half that changes rarely and matters visibly.
    [coreListPath('nodes')],
    // Everything else the header shows — requested CPU/mem from pod specs, and
    // metrics-server usage layered on top — refreshes on this poll instead.
    // Neither needs to be event-exact: requests move when workloads are
    // scheduled, usage drifts continuously and has no watch to begin with.
    30_000,
  )
  if (state.phase !== 'ready') return null

  const { nodes, cap } = state.data
  const ready = nodes.filter((n) => n.ready).length
  const cpu = cap.find((c) => c.name === 'CPU')
  const mem = cap.find((c) => c.name === 'Memory')
  const pct = (r?: CapacityResource) => (r && r.allocatable ? Math.round((r.used / r.allocatable) * 100) : 0)

  return (
    <div className="flex items-center gap-3 font-mono text-xs">
      <Item label="nodes" value={`${ready}/${nodes.length}`} tone={ready === nodes.length ? 'text-accent' : 'text-warning'} />
      <Item label="cpu" value={`${pct(cpu)}%`} />
      <Item label="mem" value={`${pct(mem)}%`} />
    </div>
  )
}

function Item({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="flex items-baseline gap-1">
      <span className={`font-bold ${tone ?? 'text-foreground'}`}>{value}</span>
      <span className="text-muted-foreground uppercase text-[10px]">{label}</span>
    </div>
  )
}
