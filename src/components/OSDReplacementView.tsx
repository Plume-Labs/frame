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
import { LiveStates, StaleNotice } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { CheckCircle, Circle, HardDrives, PauseCircle, Spinner, XCircle } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import { richText } from '@/i18n/rich'

const frame = createFrameClient()

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function phaseBadge(phase: string, t: ReturnType<typeof useI18n>['t']) {
  if (phase === 'Succeeded') return <Badge variant="secondary">{t('osr.phase.succeeded')}</Badge>
  if (phase === 'Paused') return <Badge variant="destructive">{t('osr.phase.paused')}</Badge>
  if (phase === 'Failed') return <Badge variant="destructive">{t('cax.phase.failed')}</Badge>
  if (phase === 'Cancelled') return <Badge variant="outline">{t('osr.phase.cancelled')}</Badge>
  if (phase.startsWith('Awaiting')) return <Badge>{t('osr.phase.waiting')}</Badge>
  return <Badge variant="outline">{phase}</Badge>
}

function StepIcon({ state }: { state: StepState }) {
  const { t } = useI18n()
  switch (state) {
    case 'done':
      return <CheckCircle size={16} weight="fill" className="text-emerald-500" aria-label={t('osr.state.done')} />
    case 'current':
      return <Spinner size={16} className="text-primary animate-spin" aria-label={t('osr.state.current')} />
    case 'paused':
      return <PauseCircle size={16} weight="fill" className="text-destructive" aria-label={t('osr.state.paused')} />
    case 'stopped':
      return <XCircle size={16} weight="fill" className="text-muted-foreground" aria-label={t('osr.state.stopped')} />
    default:
      return <Circle size={16} className="text-muted-foreground" aria-label={t('osr.state.pending')} />
  }
}

function Steps({ r }: { r: OSDReplacement }) {
  const { t } = useI18n()
  const states = stepStates(r)
  return (
    <ol className="grid grid-cols-2 sm:grid-cols-5 gap-x-4 gap-y-1 text-xs">
      {REPLACEMENT_STEPS.map((s, i) => (
        <li key={s.phase} className={`flex items-center gap-1.5 ${states[i] === 'pending' ? 'text-muted-foreground' : ''}`}>
          <StepIcon state={states[i]} />
          <span className={s.human ? 'font-semibold' : ''}>{t(s.label)}</span>
        </li>
      ))}
    </ol>
  )
}

/** A disk as found. The new disk's path goes through shownPath: an NVMe path can carry its serial. */
function DiskFacts({ title, d }: { title: string; d: ReplacementDisk }) {
  const { t } = useI18n()
  return (
    <div className="rounded-md border p-3 text-xs space-y-1">
      <div className="font-semibold">{title}</div>
      <div className="font-mono break-all">{shownPath(d.byIdPath)}</div>
      <div>
        {d.model || t('osr.modelUnknown')}
        {d.sizeGB > 0 && <> · {d.sizeGB} GB</>}
        {d.logicalSectorBytes > 0 && <> · {t('osr.sectors', { n: d.logicalSectorBytes })}</>}
        {d.path && <> · {t('osr.now', { path: d.path })}</>}
        {d.smartPassed === true && <> · {t('osr.smartPassed')}</>}
        {d.smartPassed === false && <span className="text-destructive"> · {t('osr.smartFailed')}</span>}
      </div>
      <div className="text-muted-foreground">{t('osr.serialHidden')}</div>
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
  const { t } = useI18n()
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
          <Label htmlFor={`serial-${title}`} className="text-xs">{t('osr.serialLabel')}</Label>
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
              {explain} {t('osr.serialTyped')} <span className="font-mono">{typed}</span>. {t('osr.serialChecked')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('cax.back')}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={async () => {
                setBusy(true)
                try {
                  await onApprove(typed)
                  setSerial('')
                  toast.success(t('osr.approvalSent'))
                } catch (e) {
                  toast.error(t('cax.notSent', { error: errText(e) }))
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
  const { t } = useI18n()
  const acts = availableActions(r)
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [showEvents, setShowEvents] = useState(!isTerminal(r.phase))
  const run = async (what: string, f: () => Promise<void>) => {
    try {
      await f()
      toast.success(what)
    } catch (e) {
      toast.error(t('osr.failed', { what, error: errText(e) }))
    }
  }
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          <HardDrives size={18} />
          <span className="font-mono">osd.{r.osdID}</span>
          {r.host && <span className="text-muted-foreground text-sm">{t('osr.on', { host: r.host })}</span>}
          {phaseBadge(r.phase, t)}
          {r.pgs && <span className="text-xs text-muted-foreground font-mono">{t('osr.pgs', { pgs: r.pgs })}</span>}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <Steps r={r} />
        {r.message && (
          <p className={`text-sm ${r.phase === 'Paused' || r.phase === 'Failed' ? 'text-destructive' : ''}`}>{r.message}</p>
        )}
        {r.oldDisk && r.phase === 'AwaitingDisk' && (
          <div className="rounded-md border p-3 text-sm">
            {richText(t('osr.swap'), {
              serial: <span className="font-mono font-semibold">{r.oldDisk.serial}</span>,
              host: <span className="font-mono">{r.host}</span>,
              machine: r.machineRef ? ` ${t('osr.swapMachine', { machine: r.machineRef })}` : '',
            })}
          </div>
        )}
        {admin && acts.approvePurge && r.oldDisk && (
          <SerialApproval
            title={t('osr.a1.title', { osd: r.osdID })}
            explain={t('osr.a1.explain', { osd: r.osdID, path: r.oldDisk.byIdPath })}
            confirmLabel={t('osr.a1.confirm', { osd: r.osdID })}
            onApprove={(s) => frame.osdReplacements.approvePurge(r, s)}
          />
        )}
        {r.newDisk && <DiskFacts title={t('osr.newDisk')} d={r.newDisk} />}
        {admin && acts.approveWipe && r.newDisk && (
          <SerialApproval
            title={t('osr.a2.title')}
            explain={t('osr.a2.explain', { path: shownPath(r.newDisk.byIdPath) })}
            confirmLabel={t('osr.a2.confirm')}
            onApprove={(s) => frame.osdReplacements.approveWipe(r, s)}
          />
        )}
        {admin && (acts.resume || acts.cancel) && (
          <div className="flex flex-wrap gap-2">
            {acts.resume && (
              <Button size="sm" onClick={() => run(t('osr.resumed'), () => frame.osdReplacements.resume(r))}>
                {t('osr.resume')}
              </Button>
            )}
            {acts.cancel && (
              <Button size="sm" variant="outline" onClick={() => setConfirmCancel(true)}>
                {t('common.cancel')}
              </Button>
            )}
          </div>
        )}
        {!admin && !isTerminal(r.phase) && (
          <p className="text-xs text-muted-foreground">{t('osr.adminOnly')}</p>
        )}
        <div>
          <button
            type="button"
            className="text-xs text-muted-foreground underline"
            aria-expanded={showEvents}
            onClick={() => setShowEvents((v) => !v)}
          >
            {t(showEvents ? 'osr.hideJournal' : 'osr.showJournal', { count: r.events.length })}
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
              <AlertDialogTitle>{t('osr.cancelTitle', { osd: r.osdID })}</AlertDialogTitle>
              <AlertDialogDescription>
                {acts.cancelUndoes
                  ? t('osr.cancelUndoes', { osd: r.osdID })
                  : t('osr.cancelFinal', { osd: r.osdID })}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t('cax.back')}</AlertDialogCancel>
              <AlertDialogAction onClick={() => run(t('osr.cancelSent'), () => frame.osdReplacements.cancel(r))}>
                {t('osr.cancelReplacement')}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}

function NewReplacement({ busyElsewhere }: { busyElsewhere: boolean }) {
  const { t, tm } = useI18n()
  const [osd, setOsd] = useState('')
  const [serial, setSerial] = useState('')
  const [open, setOpen] = useState(false)
  const error = newReplacementError(osd, serial)
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-base">{t('osr.new.title')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          {t('osr.new.intro')}
        </p>
        {busyElsewhere && (
          <p className="text-sm">{t('osr.new.busy')}</p>
        )}
        <div className="flex flex-col sm:flex-row gap-2 sm:items-end">
          <div className="space-y-1 sm:w-32">
            <Label htmlFor="new-osd" className="text-xs">{t('cax.osdNumber')}</Label>
            <Input id="new-osd" inputMode="numeric" placeholder="8" value={osd} onChange={(e) => setOsd(e.target.value)} />
          </div>
          <div className="space-y-1 flex-1">
            <Label htmlFor="new-serial" className="text-xs">{t('osr.new.serialLabel')}</Label>
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
            {t('osr.new.start')}
          </Button>
        </div>
        {osd !== '' && error && <p className="text-xs text-muted-foreground">{tm(error)}</p>}
        <AlertDialog open={open} onOpenChange={setOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t('osr.new.confirmTitle', { osd: osd.trim() })}</AlertDialogTitle>
              <AlertDialogDescription>
                {t('osr.new.confirmBody', { osd: osd.trim() })}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t('cax.back')}</AlertDialogCancel>
              <AlertDialogAction
                onClick={async () => {
                  try {
                    await frame.osdReplacements.create(Number(osd.trim()), serial)
                    setOsd('')
                    setSerial('')
                    toast.success(t('osr.new.created'))
                  } catch (e) {
                    toast.error(t('osr.new.notCreated', { error: errText(e) }))
                  }
                }}
              >
                {t('osr.new.start')}
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
  const { t } = useI18n()
  const { state } = useLiveResource<OSDReplacement[]>(
    () => frame.osdReplacements.list(),
    [],
    [frame.osdReplacements.watchPath()],
    30_000,
    { keepStaleOnError: true },
  )
  // Hides the controls only: the apiserver is what refuses a non-admin write.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  const admin = typeof token === 'string' ? isAdminToken(token) : false
  const items = state.phase === 'ready' ? state.data : []
  const running = items.some((r) => !isTerminal(r.phase))
  return (
    <div className="space-y-4">
      {admin && <NewReplacement busyElsewhere={running} />}
      <StaleNotice state={state} />
      <LiveStates state={state} emptyLabel={t('osr.none')} />
      {items.map((r) => (
        <ReplacementCard key={`${r.namespace}/${r.name}`} r={r} admin={admin} />
      ))}
    </div>
  )
}
