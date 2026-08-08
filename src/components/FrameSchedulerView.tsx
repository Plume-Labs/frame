import { useState } from 'react'
import { toast } from 'sonner'
import { SchedulingPolicy, SchedulerType, createFrameClient, frameListPath } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
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
import { Calendar, ArrowClockwise, Plus, Trash } from '@phosphor-icons/react'

const frame = createFrameClient()

const SCHEDULERS: SchedulerType[] = ['volcano', 'yunikorn', 'default']

function NewPolicyDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [name, setName] = useState('')
  const [scheduler, setScheduler] = useState<SchedulerType>('volcano')
  const [queue, setQueue] = useState('default')
  const [priority, setPriority] = useState('50')
  const [preemption, setPreemption] = useState(false)

  async function submit() {
    setSaving(true)
    try {
      await frame.scheduler.applyPolicy({
        name,
        scheduler,
        queue,
        queueWeight: 1,
        priority: Number(priority) || 0,
        preemption,
        maxGPUs: 0,
        maxCPUs: 0,
      })
      toast.success(`Policy ${name} applied`)
      setOpen(false)
      setName('')
      onCreated()
    } catch (e) {
      toast.error(`Failed to apply ${name}`, { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" className="font-mono gap-1.5">
          <Plus />
          New Policy
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="font-mono">New Scheduling Policy</DialogTitle>
          <DialogDescription>Creates or updates a SchedulingPolicy resource.</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="policy-name">Name</Label>
            <Input id="policy-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="training-high-prio" />
          </div>
          <div className="space-y-1.5">
            <Label>Scheduler</Label>
            <Select value={scheduler} onValueChange={(v) => setScheduler(v as SchedulerType)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SCHEDULERS.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="policy-queue">Queue</Label>
            <Input id="policy-queue" value={queue} onChange={(e) => setQueue(e.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="policy-priority">Priority</Label>
            <Input id="policy-priority" type="number" value={priority} onChange={(e) => setPriority(e.target.value)} />
          </div>
          <div className="flex items-center justify-between">
            <Label htmlFor="policy-preemption">Preemption</Label>
            <Switch id="policy-preemption" checked={preemption} onCheckedChange={setPreemption} />
          </div>
        </div>
        <DialogFooter>
          <Button onClick={submit} disabled={!name || saving} className="font-mono">
            {saving ? 'Applying…' : 'Apply'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function FrameSchedulerView() {
  const { state, reload } = useLiveResource<{ items: SchedulingPolicy[] }>(
    () => frame.scheduler.listPolicies(),
    [],
    [frameListPath('schedulingpolicies')],
  )
  const policies = state.phase === 'ready' ? state.data.items : []
  const [deleteTarget, setDeleteTarget] = useState<SchedulingPolicy | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  async function doDelete(p: SchedulingPolicy) {
    setBusy(p.name)
    try {
      await frame.scheduler.deletePolicy(p.name)
      toast.success(`Policy ${p.name} deleted`)
      reload()
    } catch (e) {
      toast.error(`Failed to delete ${p.name}`, { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
      setDeleteTarget(null)
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Calendar className="text-primary" />
            Scheduling Policies
            <div className="ml-auto flex gap-2">
              <NewPolicyDialog onCreated={reload} />
              <Button
                variant="outline"
                size="sm"
                className="font-mono gap-1.5"
                onClick={reload}
                disabled={state.phase === 'loading'}
              >
                <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
                Refresh
              </Button>
            </div>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Live <span className="font-mono">SchedulingPolicy</span> resources — scheduler backend,
            queues and preemption settings reconciled by the operator.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No SchedulingPolicies defined." />

      {state.phase === 'ready' &&
        policies.map((p) => (
          <Card key={p.name}>
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <CardTitle className="font-mono text-lg">{p.name}</CardTitle>
                  <p className="text-xs text-muted-foreground font-mono">
                    scheduler: {p.scheduler}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="font-mono text-primary border-current">
                    queue {p.queue}
                  </Badge>
                  <Button
                    variant="outline"
                    size="sm"
                    className="font-mono gap-1.5 text-destructive border-destructive/40 hover:bg-destructive/10"
                    disabled={busy === p.name}
                    onClick={() => setDeleteTarget(p)}
                  >
                    <Trash />
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-2 text-[10px] font-mono">
              <Badge variant="outline">weight {p.queueWeight}</Badge>
              <Badge variant="outline">priority {p.priority}</Badge>
              <Badge variant="outline" className={p.preemption ? 'text-warning' : 'text-muted-foreground'}>
                preemption {p.preemption ? 'on' : 'off'}
              </Badge>
            </CardContent>
          </Card>
        ))}

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {deleteTarget?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Deletes the SchedulingPolicy resource. Jobs referencing its queue fall back to the scheduler
              default. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep policy</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => deleteTarget && doDelete(deleteTarget)}
            >
              Delete policy
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
