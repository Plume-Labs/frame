import { useState } from 'react'
import { toast } from 'sonner'
import type { RegistryAlert } from '@/lib/alert-registry'
import { isAdminToken } from '@/lib/auth'
import { alertRegistryPath, createFrameClient } from '@/lib/frame-sdk'
import type { Machine } from '@/lib/machines'
import { checkVerdicts, heldDevices, mirrorRows, readingNotes, StorageThresholds, validThreshold, VERDICT_WORD } from '@/lib/storage-health'
import { useI18n } from '@/i18n/context'
import type { Message } from '@/i18n'
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
import { useNow } from '@/hooks/useNow'
import { Heartbeat } from '@phosphor-icons/react'

const frame = createFrameClient()

/** The two numbers of the StorageHealthPolicy. A save lands on every machine at its next pass. */
function ThresholdsCard() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<StorageThresholds>(
    () => frame.storageHealthPolicy.get(),
    [],
    [frame.storageHealthPolicy.watchPath()],
  )
  const saved = state.phase === 'ready' ? state.data : null
  const [draft, setDraft] = useState<StorageThresholds | null>(null)
  const [draftOf, setDraftOf] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // By value: a watch event re-reads an identical object, and that must not
  // wipe what is being typed. A saved value that differs replaces the draft in
  // the same render, so the form never shows the old one for a frame.
  const savedKey = saved && `${saved.zfsCapacityPercent}/${saved.ssdWearPercent}`
  if (saved && savedKey !== draftOf) {
    setDraftOf(savedKey)
    setDraft(saved)
  }

  const fields = [
    { key: 'zfsCapacityPercent', label: 'hlt.zfsLabel', hint: 'hlt.zfsHint' },
    { key: 'ssdWearPercent', label: 'hlt.wearLabel', hint: 'hlt.wearHint' },
  ] as const
  const valid = draft !== null && fields.every((f) => validThreshold(draft[f.key]))
  const changed = draft !== null && saved !== null && fields.some((f) => draft[f.key] !== saved[f.key])

  async function save() {
    if (!draft) return
    setBusy(true)
    try {
      await frame.storageHealthPolicy.save(draft)
      toast.success(t('hlt.saved'))
      reload()
    } catch (e) {
      toast.error(t('hlt.saveFailed'), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Heartbeat className="text-primary" />
          {t('hlt.thresholds')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="" />
        {draft && (
          <>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              {fields.map((f) => (
                <div key={f.key} className="space-y-1">
                  <Label htmlFor={f.key}>{t(f.label)}</Label>
                  <Input
                    id={f.key}
                    type="number"
                    min={1}
                    max={100}
                    step={1}
                    value={Number.isNaN(draft[f.key]) ? '' : draft[f.key]}
                    onChange={(e) => setDraft({ ...draft, [f.key]: e.target.valueAsNumber })}
                  />
                  <p className="text-xs text-muted-foreground">{t(f.hint)}</p>
                </div>
              ))}
            </div>
            <Button onClick={save} disabled={!valid || !changed || busy}>
              {t('common.save')}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

/** Admin only: says what no read can, and the alert closes for the subscribers too. */
function ReleaseDevice({ machine, device, checks }: { machine: Machine; device: string; checks: string[] }) {
  const { t } = useI18n()
  const [confirming, setConfirming] = useState(false)
  async function release() {
    setConfirming(false)
    try {
      await frame.machines.releaseAlert(machine, device)
      toast.success(t('hlt.released', { device }))
    } catch (e) {
      toast.error(t('hlt.releaseFailed', { device }), { description: e instanceof Error ? e.message : String(e) })
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-border p-2">
      <span className="font-bold">{device}</span>
      <span className="text-muted-foreground">{t('hlt.held', { checks: checks.join(', ') })}</span>
      <Button variant="outline" size="sm" className="ml-auto" onClick={() => setConfirming(true)}>
        {t('hlt.removedOnPurpose')}
      </Button>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('hlt.confirmTitle', { device })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('hlt.confirmBody')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={release}>{t('hlt.closeAlerts')}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function MachineHealthCard({ machine, alerts, admin }: { machine: Machine; alerts: RegistryAlert[]; admin: boolean }) {
  const { t, tm } = useI18n()
  const text = (x: string | Message) => (typeof x === 'string' ? x : tm(x))
  const now = useNow()
  const rows = mirrorRows(machine)
  const attention = checkVerdicts(machine).filter((v) => v.detail)
  return (
    <Card>
      <CardHeader className="space-y-2">
        <CardTitle className="font-mono text-base">{machine.nodeRef || machine.name}</CardTitle>
        <div className="flex flex-wrap gap-2">
          {checkVerdicts(machine).map((v) => (
            <Badge key={v.type} variant="outline" className={`font-mono text-[10px] border-current ${TONE_TEXT[v.tone]}`}>
              {t(v.label)}: {VERDICT_WORD[v.word] ? t(VERDICT_WORD[v.word]) : v.word}
            </Badge>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-2 text-xs font-mono">
        {rows.length === 0 && <p className="text-muted-foreground">{t('hlt.noRows')}</p>}
        {rows.map((r) => (
          <div key={r.key} className="flex flex-wrap gap-x-4 rounded-md border border-border p-2">
            <span className="font-bold">{r.name}</span>
            <span className="text-muted-foreground">{r.layout}</span>
            <span className={TONE_TEXT[r.tone]}>{text(r.state)}</span>
            <span className="text-muted-foreground">{r.detail.map(text).join(' · ')}</span>
          </div>
        ))}
        {readingNotes(machine, now).map((n) => (
          <p key={tm(n)} className="text-warning">
            {tm(n)}
          </p>
        ))}
        {admin &&
          heldDevices(machine, alerts).map((h) => <ReleaseDevice key={h.device} machine={machine} device={h.device} checks={h.checks} />)}
        {attention.map((v) => (
          <p key={v.type} className={TONE_TEXT[v.tone]}>
            {t(v.label)}: {v.detail}
          </p>
        ))}
      </CardContent>
    </Card>
  )
}

/** Storage health per machine and per mirror, and the thresholds that alert on it. */
export function StorageHealthView() {
  const { t } = useI18n()
  const { state } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  const { state: alertState } = useLiveResource<RegistryAlert[]>(() => frame.cluster.alertRegistry(), [], [alertRegistryPath()])
  const alerts = alertState.phase === 'ready' ? alertState.data : []
  // Hides the button only: patch on framemachines is what is admin-only.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  const admin = typeof token === 'string' ? isAdminToken(token) : false
  return (
    <div className="space-y-6">
      <ThresholdsCard />
      <LiveStates state={state} emptyLabel={t('hlt.noMachines')} />
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
