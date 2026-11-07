import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import type { RegistryAlert } from '@/lib/alert-registry'
import { isAdminToken } from '@/lib/auth'
import { alertRegistryPath, createFrameClient } from '@/lib/frame-sdk'
import type { Machine } from '@/lib/machines'
import { checkVerdicts, heldDevices, mirrorRows, readingNotes, StorageThresholds, validThreshold } from '@/lib/storage-health'
import { TONE_TEXT } from '@/lib/thresholds'
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
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Heartbeat } from '@phosphor-icons/react'

const frame = createFrameClient()

/** The two numbers of the StorageHealthPolicy. A save lands on every machine at its next pass. */
function ThresholdsCard() {
  const { state, reload } = useLiveResource<StorageThresholds>(
    () => frame.storageHealthPolicy.get(),
    [],
    [frame.storageHealthPolicy.watchPath()],
  )
  const saved = state.phase === 'ready' ? state.data : null
  const [draft, setDraft] = useState<StorageThresholds | null>(null)
  const [busy, setBusy] = useState(false)
  // By value: a watch event re-reads an identical object, and that must not
  // wipe what is being typed.
  const savedKey = saved && `${saved.zfsCapacityPercent}/${saved.ssdWearPercent}`
  useEffect(() => {
    if (saved) setDraft(saved)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [savedKey])

  const fields = [
    { key: 'zfsCapacityPercent', label: 'ZFS pool capacity (%)', hint: 'Alert at this level, clears two points below.' },
    { key: 'ssdWearPercent', label: 'SSD endurance used (%)', hint: 'From the BMC, or the disk\'s own SMART where the BMC gives no figure.' },
  ] as const
  const valid = draft !== null && fields.every((f) => validThreshold(draft[f.key]))
  const changed = draft !== null && saved !== null && fields.some((f) => draft[f.key] !== saved[f.key])

  async function save() {
    if (!draft) return
    setBusy(true)
    try {
      await frame.storageHealthPolicy.save(draft)
      toast.success('Thresholds saved')
      reload()
    } catch (e) {
      toast.error('Échec : thresholds not saved', { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Heartbeat className="text-primary" />
          Alert thresholds
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="" />
        {draft && (
          <>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              {fields.map((f) => (
                <div key={f.key} className="space-y-1">
                  <Label htmlFor={f.key}>{f.label}</Label>
                  <Input
                    id={f.key}
                    type="number"
                    min={1}
                    max={100}
                    step={1}
                    value={Number.isNaN(draft[f.key]) ? '' : draft[f.key]}
                    onChange={(e) => setDraft({ ...draft, [f.key]: e.target.valueAsNumber })}
                  />
                  <p className="text-xs text-muted-foreground">{f.hint}</p>
                </div>
              ))}
            </div>
            <Button onClick={save} disabled={!valid || !changed || busy}>
              Save
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

/** Admin only: says what no read can, and the alert closes for the subscribers too. */
function ReleaseDevice({ machine, device, checks }: { machine: Machine; device: string; checks: string[] }) {
  const [confirming, setConfirming] = useState(false)
  async function release() {
    setConfirming(false)
    try {
      await frame.machines.releaseAlert(machine, device)
      toast.success(`${device}: declared removed`)
    } catch (e) {
      toast.error(`Échec : ${device}`, { description: e instanceof Error ? e.message : String(e) })
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-border p-2">
      <span className="font-bold">{device}</span>
      <span className="text-muted-foreground">{checks.join(', ')} held, no read mentions it</span>
      <Button variant="outline" size="sm" className="ml-auto" onClick={() => setConfirming(true)}>
        Removed on purpose
      </Button>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Was {device} removed on purpose?</AlertDialogTitle>
            <AlertDialogDescription>
              This closes its alerts and tells subscribers the incident is resolved. A pool a reboot failed to import looks the
              same: check before confirming. An alert a read still shows stays open.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={release}>Close the alerts</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function MachineHealthCard({ machine, alerts, admin }: { machine: Machine; alerts: RegistryAlert[]; admin: boolean }) {
  const rows = mirrorRows(machine)
  const attention = checkVerdicts(machine).filter((v) => v.detail)
  return (
    <Card>
      <CardHeader className="space-y-2">
        <CardTitle className="font-mono text-base">{machine.nodeRef || machine.name}</CardTitle>
        <div className="flex flex-wrap gap-2">
          {checkVerdicts(machine).map((v) => (
            <Badge key={v.type} variant="outline" className={`font-mono text-[10px] border-current ${TONE_TEXT[v.tone]}`}>
              {v.label}: {v.word}
            </Badge>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-2 text-xs font-mono">
        {rows.length === 0 && <p className="text-muted-foreground">No md array or ZFS pool reported by this node.</p>}
        {rows.map((r) => (
          <div key={r.key} className="flex flex-wrap gap-x-4 rounded-md border border-border p-2">
            <span className="font-bold">{r.name}</span>
            <span className="text-muted-foreground">{r.layout}</span>
            <span className={TONE_TEXT[r.tone]}>{r.state}</span>
            <span className="text-muted-foreground">{r.detail}</span>
          </div>
        ))}
        {readingNotes(machine, Date.now()).map((n) => (
          <p key={n} className="text-warning">
            {n}
          </p>
        ))}
        {admin &&
          heldDevices(machine, alerts).map((h) => <ReleaseDevice key={h.device} machine={machine} device={h.device} checks={h.checks} />)}
        {attention.map((v) => (
          <p key={v.type} className={TONE_TEXT[v.tone]}>
            {v.label}: {v.detail}
          </p>
        ))}
      </CardContent>
    </Card>
  )
}

/** Storage health per machine and per mirror, and the thresholds that alert on it. */
export function StorageHealthView() {
  const { state } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  const { state: alertState } = useLiveResource<RegistryAlert[]>(() => frame.cluster.alertRegistry(), [], [alertRegistryPath()])
  const alerts = alertState.phase === 'ready' ? alertState.data : []
  // Hides the button only: patch on framemachines is what is admin-only.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  const admin = typeof token === 'string' ? isAdminToken(token) : false
  return (
    <div className="space-y-6">
      <ThresholdsCard />
      <LiveStates state={state} emptyLabel="No FrameMachine declared yet." />
      {state.phase === 'ready' && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {state.data.map((m) => (
            <MachineHealthCard key={`${m.namespace}/${m.name}`} machine={m} alerts={alerts} admin={admin} />
          ))}
        </div>
      )}
    </div>
  )
}
