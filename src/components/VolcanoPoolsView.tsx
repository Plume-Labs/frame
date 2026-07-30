import { useState } from 'react'
import { toast } from 'sonner'
import { VolcanoQueue, VolcanoStats, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ArrowsLeftRight, ArrowClockwise, Play, Pause, Scales } from '@phosphor-icons/react'

const frame = createFrameClient()

const phaseTone = (p: string) =>
  p === 'Running' ? 'text-accent' : p === 'Pending' || p === 'Inqueue' ? 'text-warning' : 'text-muted-foreground'

export function VolcanoPoolsView() {
  const { state, reload } = useLiveResource<VolcanoStats>(() => frame.cluster.volcano())
  const d = state.phase === 'ready' ? state.data : null
  const [closeTarget, setCloseTarget] = useState<VolcanoQueue | null>(null)
  const [weightTarget, setWeightTarget] = useState<VolcanoQueue | null>(null)
  const [weight, setWeight] = useState('1')
  const [busy, setBusy] = useState<string | null>(null)

  async function run(q: VolcanoQueue, action: () => Promise<void>, what: string) {
    setBusy(q.name)
    try {
      await action()
      toast.success(`${q.name} ${what}`)
      reload()
    } catch (e) {
      toast.error(`Failed to update ${q.name}`, { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
      setCloseTarget(null)
      setWeightTarget(null)
    }
  }

  const setState = (q: VolcanoQueue, state: 'Open' | 'Closed') =>
    run(q, () => frame.cluster.setQueueState(q.name, state), state === 'Open' ? 'reopened' : 'closed')

  function openWeightDialog(q: VolcanoQueue) {
    setWeight(String(q.weight))
    setWeightTarget(q)
  }

  function submitWeight() {
    if (!weightTarget) return
    const n = Number(weight)
    // Volcano treats weight as a positive share divisor — 0 or negative would
    // make the queue's fair share meaningless, so refuse before the API does.
    if (!Number.isInteger(n) || n < 1) {
      toast.error('Weight must be a positive integer')
      return
    }
    run(weightTarget, () => frame.cluster.setQueueWeight(weightTarget.name, n), `weight set to ${n}`)
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowsLeftRight className="text-primary" />
            Elastic Pools — Volcano Queues
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Live Volcano scheduling queues (elastic resource pools) and the gang-scheduled
            PodGroups running in them.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="Volcano not installed / no queues." />

      {d && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {d.queues.map((q) => {
            const groups = d.podGroups.filter((p) => p.queue === q.name)
            return (
              <Card key={q.name}>
                <CardHeader className="space-y-2">
                  <div className="flex items-center justify-between gap-2">
                    <CardTitle className="font-mono text-lg">{q.name}</CardTitle>
                    <Badge
                      variant="outline"
                      className={`font-mono text-[10px] border-current ${
                        q.state === 'Closed' ? 'text-warning' : 'text-accent'
                      }`}
                    >
                      {q.state}
                    </Badge>
                  </div>
                  <div className="flex items-center gap-2">
                    {q.state === 'Closed' ? (
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-7 font-mono text-[10px] gap-1"
                        disabled={busy === q.name}
                        onClick={() => setState(q, 'Open')}
                      >
                        <Play size={10} />
                        Open
                      </Button>
                    ) : (
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-7 font-mono text-[10px] gap-1"
                        disabled={busy === q.name}
                        onClick={() => setCloseTarget(q)}
                      >
                        <Pause size={10} />
                        Close
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 font-mono text-[10px] gap-1"
                      disabled={busy === q.name}
                      onClick={() => openWeightDialog(q)}
                    >
                      <Scales size={10} />
                      Weight
                    </Button>
                  </div>
                </CardHeader>
                <CardContent className="space-y-2 text-xs font-mono">
                  <Row label="Weight" value={`${q.weight}`} />
                  <Row label="Capability" value={`${q.cpuCapability} CPU · ${q.memCapability}`} />
                  <Row label="Reclaimable" value={q.reclaimable ? 'yes' : 'no'} />
                  <Row label="Running" value={`${q.running}`} />
                  {groups.length > 0 && (
                    <div className="pt-2 space-y-1">
                      {groups.map((g) => (
                        <div key={`${g.namespace}/${g.name}`} className="flex items-center justify-between">
                          <span className="text-muted-foreground truncate">
                            {g.namespace}/{g.name.slice(0, 20)}
                          </span>
                          <span className={`${phaseTone(g.phase)}`}>
                            {g.phase} ·{g.minMember}
                          </span>
                        </div>
                      ))}
                    </div>
                  )}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}

      <AlertDialog open={!!closeTarget} onOpenChange={(open) => !open && setCloseTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Close queue {closeTarget?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Stops Volcano from admitting new PodGroups to this queue. The {closeTarget?.running ?? 0}{' '}
              already running keep their resources — nothing is evicted. Reversible with Open.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep open</AlertDialogCancel>
            <AlertDialogAction onClick={() => closeTarget && setState(closeTarget, 'Closed')}>
              Close queue
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog open={!!weightTarget} onOpenChange={(open) => !open && setWeightTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="font-mono">Share weight — {weightTarget?.name}</DialogTitle>
            <DialogDescription>
              Relative share of cluster capacity when queues compete. Only the ratio between queues
              matters, not the absolute number.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="queue-weight" className="text-xs font-mono">
              Weight
            </Label>
            <Input
              id="queue-weight"
              type="number"
              min={1}
              step={1}
              value={weight}
              onChange={(e) => setWeight(e.target.value)}
              className="font-mono"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setWeightTarget(null)}>
              Cancel
            </Button>
            <Button onClick={submitWeight} disabled={busy === weightTarget?.name}>
              Apply
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      <Badge variant="outline" className="font-mono text-[10px]">
        {value}
      </Badge>
    </div>
  )
}
