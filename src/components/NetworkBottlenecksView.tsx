import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { createFrameClient } from '@/lib/frame-sdk'
import type { Machine } from '@/lib/machines'
import { percentLabel, PodTraffic, rankLinks, rateLabel } from '@/lib/network-bottlenecks'

const frame = createFrameClient()

function LinksCard({ machines }: { machines: Machine[] }) {
  const rows = rankLinks(machines)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg">Busiest links</CardTitle>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">No rates yet: agents need two readings, a minute at most.</p>
        ) : (
          <table className="w-full text-xs font-mono">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="pr-4 py-1">Link</th>
                <th className="pr-4">Now</th>
                <th className="pr-4">5 min</th>
                <th className="pr-4">15 min peak</th>
                <th className="pr-4">In</th>
                <th className="pr-4">Out</th>
                <th className="pr-4">Errors/min</th>
                <th>Drops/min</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.node}/${r.name}`} className="border-t border-border">
                  <td className="pr-4 py-1">
                    {r.node} {r.name}
                    {r.kind === 'member' && <span className="text-muted-foreground"> (member)</span>}
                  </td>
                  <td className="pr-4">{percentLabel(r.traffic.utilizationPercent)}</td>
                  <td className="pr-4">{percentLabel(r.traffic.sustainedUtilizationPercent)}</td>
                  <td className="pr-4">{percentLabel(r.traffic.peakUtilizationPercent)}</td>
                  <td className="pr-4">{rateLabel(r.traffic.rxBytesPerSecond)}</td>
                  <td className="pr-4">{rateLabel(r.traffic.txBytesPerSecond)}</td>
                  <td className={`pr-4 ${r.traffic.errorsPerMinute > 0 ? 'text-warning' : ''}`}>{r.traffic.errorsPerMinute}</td>
                  <td className={r.traffic.dropsPerMinute > 0 ? 'text-warning' : ''}>{r.traffic.dropsPerMinute}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="mt-3 text-xs text-muted-foreground">
          Percentages are of the link's speed, in its busier direction. A dash is a link that gives no speed, as on virtio, not an idle one.
        </p>
      </CardContent>
    </Card>
  )
}

function PodList({ title, pods }: { title: string; pods: PodTraffic[] }) {
  return (
    <div className="space-y-1">
      <h3 className="text-sm font-medium">{title}</h3>
      {pods.map((p) => (
        <div key={`${p.namespace}/${p.pod}`} className="flex justify-between gap-4 text-xs font-mono">
          <span className="truncate">
            {p.namespace}/{p.pod}
          </span>
          <span>{rateLabel(p.bytesPerSecond)}</span>
        </div>
      ))}
    </div>
  )
}

function PodsCard() {
  const { state } = useLiveResource(() => frame.cluster.podTraffic(), [], [], 30_000)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg">Busiest pods</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="" />
        {state.phase === 'ready' &&
          (state.data === null ? (
            <p className="text-sm text-muted-foreground">Prometheus does not answer: no per-pod figures.</p>
          ) : (
            <>
              <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
                <PodList title="Sending" pods={state.data.tx} />
                <PodList title="Receiving" pods={state.data.rx} />
              </div>
              <p className="text-xs text-muted-foreground">Over five minutes. Pods on the host network are left out: their counters are the node's.</p>
            </>
          ))}
      </CardContent>
    </Card>
  )
}

/** Where the cluster's network is tight, measured. Nothing here limits anything. */
export function NetworkBottlenecksView() {
  const { state } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  return (
    <div className="space-y-6">
      <LiveStates state={state} emptyLabel="No FrameMachine declared yet." />
      {state.phase === 'ready' && <LinksCard machines={state.data} />}
      <PodsCard />
    </div>
  )
}
