import { useState } from 'react'
import { toast } from 'sonner'
import { isAdminToken } from '@/lib/auth'
import { createFrameClient } from '@/lib/frame-sdk'
import {
  availableActions,
  isTerminal,
  newReplacementError,
  normalizeSerial,
  REPLACEMENT_STEPS,
  stepStates,
  type OSDReplacement,
  type ReplacementDisk,
  type StepState,
  shownPath,
} from '@/lib/osd-replacement'
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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { CheckCircle, Circle, HardDrives, PauseCircle, Spinner, XCircle } from '@phosphor-icons/react'

const frame = createFrameClient()

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function phaseBadge(phase: string) {
  if (phase === 'Succeeded') return <Badge variant="secondary">Succeeded</Badge>
  if (phase === 'Paused' || phase === 'Failed') return <Badge variant="destructive">{phase}</Badge>
  if (phase === 'Cancelled') return <Badge variant="outline">Cancelled</Badge>
  if (phase.startsWith('Awaiting')) return <Badge>Waiting for you</Badge>
  return <Badge variant="outline">{phase}</Badge>
}

function StepIcon({ state }: { state: StepState }) {
  switch (state) {
    case 'done':
      return <CheckCircle size={16} weight="fill" className="text-emerald-500" aria-label="done" />
    case 'current':
      return <Spinner size={16} className="text-primary animate-spin" aria-label="in progress" />
    case 'paused':
      return <PauseCircle size={16} weight="fill" className="text-destructive" aria-label="paused" />
    case 'stopped':
      return <XCircle size={16} weight="fill" className="text-muted-foreground" aria-label="stopped" />
    default:
      return <Circle size={16} className="text-muted-foreground" aria-label="not started" />
  }
}

function Steps({ r }: { r: OSDReplacement }) {
  const states = stepStates(r)
  return (
    <ol className="grid grid-cols-2 sm:grid-cols-5 gap-x-4 gap-y-1 text-xs">
      {REPLACEMENT_STEPS.map((s, i) => (
        <li key={s.phase} className={`flex items-center gap-1.5 ${states[i] === 'pending' ? 'text-muted-foreground' : ''}`}>
          <StepIcon state={states[i]} />
          <span className={s.human ? 'font-semibold' : ''}>{s.label}</span>
        </li>
      ))}
    </ol>
  )
}

/** A disk as found. The new disk's path goes through shownPath: an NVMe path can carry its serial. */
function DiskFacts({ title, d }: { title: string; d: ReplacementDisk }) {
  return (
    <div className="rounded-md border p-3 text-xs space-y-1">
      <div className="font-semibold">{title}</div>
      <div className="font-mono break-all">{shownPath(d.byIdPath)}</div>
      <div>
        {d.model || 'model unknown'}
        {d.sizeGB > 0 && <> · {d.sizeGB} GB</>}
        {d.logicalSectorBytes > 0 && <> · {d.logicalSectorBytes}-byte sectors</>}
        {d.path && <> · now {d.path}</>}
        {d.smartPassed === true && <> · SMART passed</>}
        {d.smartPassed === false && <span className="text-destructive"> · SMART FAILED</span>}
      </div>
      <div className="text-muted-foreground">The serial is not shown on purpose: read it on the disk's label.</div>
    </div>
  )
}

/** Retype a serial, then confirm in a dialog that says what cannot be undone. */
function SerialApproval({
  title,
  explain,
  confirmLabel,
  onApprove,
}: {
  title: string
  explain: string
  confirmLabel: string
  onApprove: (serial: string) => Promise<void>
}) {
  const [serial, setSerial] = useState('')
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const typed = normalizeSerial(serial)
  return (
    <div className="rounded-md border border-destructive/50 bg-destructive/5 p-3 space-y-2">
      <div className="text-sm font-semibold">{title}</div>
      <p className="text-xs text-muted-foreground">{explain}</p>
      <div className="flex flex-col sm:flex-row gap-2 sm:items-end">
        <div className="space-y-1 flex-1">
          <Label htmlFor={`serial-${title}`} className="text-xs">Serial, as printed on the label</Label>
          <Input
            id={`serial-${title}`}
            value={serial}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            onChange={(e) => setSerial(e.target.value)}
          />
        </div>
        <Button variant="destructive" disabled={!typed || busy} onClick={() => setOpen(true)}>
          {confirmLabel}
        </Button>
      </div>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{confirmLabel}?</AlertDialogTitle>
            <AlertDialogDescription>
              {explain} Serial typed: <span className="font-mono">{typed}</span>. Frame checks it against the disk and
              does nothing if it does not match.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Back</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={async () => {
                setBusy(true)
                try {
                  await onApprove(typed)
                  setSerial('')
                  toast.success('Approval sent; Frame checks the serial before acting.')
                } catch (e) {
                  toast.error(`Not sent: ${errText(e)}`)
                } finally {
                  setBusy(false)
                }
              }}
            >
              {confirmLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function ReplacementCard({ r, admin }: { r: OSDReplacement; admin: boolean }) {
  const acts = availableActions(r)
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [showEvents, setShowEvents] = useState(!isTerminal(r.phase))
  const run = async (what: string, f: () => Promise<void>) => {
    try {
      await f()
      toast.success(what)
    } catch (e) {
      toast.error(`${what} failed: ${errText(e)}`)
    }
  }
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          <HardDrives size={18} />
          <span className="font-mono">osd.{r.osdID}</span>
          {r.host && <span className="text-muted-foreground text-sm">on {r.host}</span>}
          {phaseBadge(r.phase)}
          {r.pgs && <span className="text-xs text-muted-foreground font-mono">PGs {r.pgs} active+clean</span>}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Steps r={r} />
        {r.message && (
          <p className={`text-sm ${r.phase === 'Paused' || r.phase === 'Failed' ? 'text-destructive' : ''}`}>{r.message}</p>
        )}
        {r.oldDisk && r.phase === 'AwaitingDisk' && (
          <div className="rounded-md border p-3 text-sm">
            Remove the disk with serial <span className="font-mono font-semibold">{r.oldDisk.serial}</span> from{' '}
            <span className="font-mono">{r.host}</span>
            {r.machineRef && <> (machine {r.machineRef}: its bay is on the Disks tab)</>}, then insert the new disk. If it
            does not appear, set it to Non-RAID in iDRAC → Storage → Physical Disks.
          </div>
        )}
        {admin && acts.approvePurge && r.oldDisk && (
          <SerialApproval
            title={`Approval 1 — purge osd.${r.osdID}`}
            explain={`osd.${r.osdID} is drained and Ceph says it is safe to destroy. Purging removes it from Ceph for good; its data already has its copies elsewhere. Retype the serial on the label of the OLD disk (${r.oldDisk.byIdPath}).`}
            confirmLabel={`Purge osd.${r.osdID}`}
            onApprove={(s) => frame.osdReplacements.approvePurge(r, s)}
          />
        )}
        {r.newDisk && <DiskFacts title="New disk found" d={r.newDisk} />}
        {admin && acts.approveWipe && r.newDisk && (
          <SerialApproval
            title="Approval 2 — wipe the new disk"
            explain={`Wiping erases the start of ${shownPath(r.newDisk.byIdPath)} and its partition tables. Read the serial on the NEW disk's label and retype it.`}
            confirmLabel="Wipe the new disk"
            onApprove={(s) => frame.osdReplacements.approveWipe(r, s)}
          />
        )}
        {admin && (acts.resume || acts.cancel) && (
          <div className="flex flex-wrap gap-2">
            {acts.resume && (
              <Button size="sm" onClick={() => run('Resumed', () => frame.osdReplacements.resume(r))}>
                Resume (retry this step)
              </Button>
            )}
            {acts.cancel && (
              <Button size="sm" variant="outline" onClick={() => setConfirmCancel(true)}>
                Cancel
              </Button>
            )}
          </div>
        )}
        {!admin && !isTerminal(r.phase) && (
          <p className="text-xs text-muted-foreground">Only an admin can approve, resume or cancel a replacement.</p>
        )}
        <div>
          <button
            type="button"
            className="text-xs text-muted-foreground underline"
            aria-expanded={showEvents}
            onClick={() => setShowEvents((v) => !v)}
          >
            {showEvents ? 'Hide' : 'Show'} the journal ({r.events.length})
          </button>
          {showEvents && (
            <ul className="mt-2 space-y-0.5 text-xs font-mono max-h-64 overflow-y-auto">
              {r.events.map((e, i) => (
                <li key={i}>
                  <span className="text-muted-foreground">{e.at.replace('T', ' ').replace('Z', '')}</span> {e.message}
                </li>
              ))}
            </ul>
          )}
        </div>
        <AlertDialog open={confirmCancel} onOpenChange={setConfirmCancel}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Cancel the replacement of osd.{r.osdID}?</AlertDialogTitle>
              <AlertDialogDescription>
                {acts.cancelUndoes
                  ? `Nothing is destroyed yet: osd.${r.osdID} is marked in again and Ceph moves its data back.`
                  : `osd.${r.osdID} is already purged: nothing is undone. Frame stops here and says where things stand; finish by hand with the fiche.`}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Back</AlertDialogCancel>
              <AlertDialogAction onClick={() => run('Cancel sent', () => frame.osdReplacements.cancel(r))}>
                Cancel the replacement
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}

function NewReplacement({ busyElsewhere }: { busyElsewhere: boolean }) {
  const [osd, setOsd] = useState('')
  const [serial, setSerial] = useState('')
  const [open, setOpen] = useState(false)
  const error = newReplacementError(osd, serial)
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-base">Replace an OSD</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          One OSD at a time. Frame checks Ceph, drains the OSD, then stops twice for you: before the purge and before it
          wipes the new disk. Each time you retype a serial read on a disk's label. Nothing irreversible happens without it.
        </p>
        {busyElsewhere && (
          <p className="text-sm">A replacement is already running: a new one waits for it to finish.</p>
        )}
        <div className="flex flex-col sm:flex-row gap-2 sm:items-end">
          <div className="space-y-1 sm:w-32">
            <Label htmlFor="new-osd" className="text-xs">OSD number</Label>
            <Input id="new-osd" inputMode="numeric" placeholder="8" value={osd} onChange={(e) => setOsd(e.target.value)} />
          </div>
          <div className="space-y-1 flex-1">
            <Label htmlFor="new-serial" className="text-xs">Serial of its current disk (on the label)</Label>
            <Input
              id="new-serial"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
              value={serial}
              onChange={(e) => setSerial(e.target.value)}
            />
          </div>
          <Button disabled={error !== null} onClick={() => setOpen(true)}>
            Start
          </Button>
        </div>
        {osd !== '' && error && <p className="text-xs text-muted-foreground">{error}</p>}
        <AlertDialog open={open} onOpenChange={setOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Start replacing osd.{osd.trim()}?</AlertDialogTitle>
              <AlertDialogDescription>
                Frame runs the checks, then marks osd.{osd.trim()} out and waits for its data to move (about 15 minutes per
                500 GB). This part is undone by Cancel. It then waits for your first approval.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Back</AlertDialogCancel>
              <AlertDialogAction
                onClick={async () => {
                  try {
                    await frame.osdReplacements.create(Number(osd.trim()), serial)
                    setOsd('')
                    setSerial('')
                    toast.success('Replacement created; Frame starts with the checks.')
                  } catch (e) {
                    toast.error(`Not created: ${errText(e)}`)
                  }
                }}
              >
                Start
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}

/** Storage → Replace OSD: the replacements, newest first, and the form to start one. */
export function OSDReplacementView() {
  const { state } = useLiveResource<OSDReplacement[]>(
    () => frame.osdReplacements.list(),
    [],
    [frame.osdReplacements.watchPath()],
    30_000,
  )
  // Hides the controls only: the apiserver is what refuses a non-admin write.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  const admin = typeof token === 'string' ? isAdminToken(token) : false
  const items = state.phase === 'ready' ? state.data : []
  const running = items.some((r) => !isTerminal(r.phase))
  return (
    <div className="space-y-4">
      {admin && <NewReplacement busyElsewhere={running} />}
      <LiveStates state={state} emptyLabel="No OSD replacement yet." />
      {items.map((r) => (
        <ReplacementCard key={`${r.namespace}/${r.name}`} r={r} admin={admin} />
      ))}
    </div>
  )
}
