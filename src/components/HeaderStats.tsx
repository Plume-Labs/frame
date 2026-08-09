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
    // Both calls read real Node/Pod objects underneath — watched.
    [coreListPath('nodes'), coreListPath('pods')],
    // The cpu/mem percentages layer metrics-server usage on top, best-effort,
    // with no watch support and continuous drift the object watch can't see.
    // Slow poll: the watch already covers every discrete change (a node
    // joining, a pod scheduled), this only tops up the number that moves on
    // its own between them.
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
