import { useState } from 'react'
import { toast } from 'sonner'
import { VolcanoQueue, VolcanoStats, createFrameClient, crdListPath } from '@/lib/frame-sdk'
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
import { useI18n } from '@/i18n/context'

const frame = createFrameClient()

const phaseTone = (p: string) =>
  p === 'Running' ? 'text-accent' : p === 'Pending' || p === 'Inqueue' ? 'text-warning' : 'text-muted-foreground'

export function VolcanoPoolsView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<VolcanoStats>(
    () => frame.cluster.volcano(),
    [],
    [
      crdListPath('scheduling.volcano.sh', 'v1beta1', 'queues'),
      crdListPath('scheduling.volcano.sh', 'v1beta1', 'podgroups'),
    ],
  )
  const d = state.phase === 'ready' ? state.data : null
  const [closeTarget, setCloseTarget] = useState<VolcanoQueue | null>(null)
  const [weightTarget, setWeightTarget] = useState<VolcanoQueue | null>(null)
  const [weight, setWeight] = useState('1')
  const [busy, setBusy] = useState<string | null>(null)

  async function run(q: VolcanoQueue, action: () => Promise<void>, what: string) {
    setBusy(q.name)
    try {
      await action()
      toast.success(t('vq.done', { name: q.name, what }))
      reload()
    } catch (e) {
      toast.error(t('vq.failed', { name: q.name }), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
      setCloseTarget(null)
      setWeightTarget(null)
    }
  }

  const setState = (q: VolcanoQueue, state: 'Open' | 'Closed') =>
    run(q, () => frame.cluster.setQueueState(q.name, state), state === 'Open' ? t('vq.reopened') : t('vq.closed'))

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
      toast.error(t('vq.weightInvalid'))
      return
    }
    run(weightTarget, () => frame.cluster.setQueueWeight(weightTarget.name, n), t('vq.weightSet', { n }))
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowsLeftRight className="text-primary" />
            {t('vq.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {t('vq.desc')}
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel={t('vq.empty')} />

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
                        {t('vq.open')}
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
                        {t('vq.close')}
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
                      {t('vq.weight')}
                    </Button>
                  </div>
                </CardHeader>
                <CardContent className="space-y-2 text-xs font-mono">
                  <Row label={t('vq.weight')} value={`${q.weight}`} />
                  <Row label={t('vq.capability')} value={`${q.cpuCapability} CPU · ${q.memCapability}`} />
                  <Row label={t('vq.reclaimable')} value={q.reclaimable ? t('common.yes') : t('common.no')} />
                  <Row label={t('vq.running')} value={`${q.running}`} />
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
            <AlertDialogTitle>{t('vq.closeTitle', { name: closeTarget?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('vq.closeDesc', { running: closeTarget?.running ?? 0 })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('vq.keepOpen')}</AlertDialogCancel>
            <AlertDialogAction onClick={() => closeTarget && setState(closeTarget, 'Closed')}>
              {t('vq.closeQueue')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog open={!!weightTarget} onOpenChange={(open) => !open && setWeightTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="font-mono">{t('vq.weightTitle', { name: weightTarget?.name ?? '' })}</DialogTitle>
            <DialogDescription>
              {t('vq.weightDesc')}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5">
            <Label htmlFor="queue-weight" className="text-xs font-mono">
              {t('vq.weight')}
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
              {t('common.cancel')}
            </Button>
            <Button onClick={submitWeight} disabled={busy === weightTarget?.name}>
              {t('pol.apply')}
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
