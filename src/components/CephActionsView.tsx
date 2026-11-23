import { useState } from 'react'
import { toast } from 'sonner'
import { isAdminToken } from '@/lib/auth'
import { createFrameClient } from '@/lib/frame-sdk'
import {
  actionLabel,
  activeFlags,
  DEFAULT_FLAG_MINUTES,
  describeAction,
  isOverdue,
  isTerminalAction,
  newActionError,
  SETTABLE_FLAGS,
  type CephAction,
  type CephActionKind,
  type SettableFlag,
} from '@/lib/ceph-action'
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { LiveStates, StaleNotice } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { Flag } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { Message, MessageKey } from '@/i18n'

const frame = createFrameClient()

const ACTIONS: { value: CephActionKind; label: MessageKey }[] = [
  { value: 'OSDOut', label: 'cax.act.out' },
  { value: 'OSDIn', label: 'cax.act.in' },
  { value: 'OSDRestart', label: 'cax.act.restart' },
  { value: 'OSDScrub', label: 'cax.act.scrub' },
  { value: 'OSDDeepScrub', label: 'cax.act.deepScrub' },
  { value: 'SetFlag', label: 'cax.act.flag' },
]

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function isAdmin(): boolean {
  // Hides the controls only: the apiserver is what refuses a non-admin write.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  return typeof token === 'string' ? isAdminToken(token) : false
}

function when(iso: string | null, locale?: string): string {
  return iso ? new Date(iso).toLocaleString(locale) : '—'
}

function phaseBadge(a: CephAction, text: (x: string | Message) => string) {
  const phase = a.phase
  if (phase === 'Done') return <Badge variant="secondary">{text(actionLabel(a))}</Badge>
  if (phase === 'Refused' || phase === 'Failed') return <Badge variant="destructive">{text(actionLabel(a))}</Badge>
  if (phase === 'Active') return <Badge>{text(actionLabel(a))}</Badge>
  return <Badge variant="outline">{text(actionLabel(a))}</Badge>
}

/**
 * The cluster flags Hearthrack set and still holds, each with its end. Shown on
 * the Ceph screens while one is active: a forgotten noout hides failures.
 */
export function ActiveFlagsBanner({ items, admin }: { items: CephAction[]; admin: boolean }) {
  const { t, locale } = useI18n()
  const flags = activeFlags(items)
  if (flags.length === 0) return null
  const now = new Date()
  const late = flags.some((a) => isOverdue(a, now))
  return (
    <div
      role={late ? 'alert' : 'status'}
      className={
        late
          ? 'rounded-md border border-destructive/60 bg-destructive/10 p-3 text-sm space-y-1'
          : 'rounded-md border border-warning/50 bg-warning/10 p-3 text-sm space-y-1'
      }
    >
      {flags.map((a) => (
        <div key={a.name} className="flex flex-wrap items-center gap-2">
          <Flag className={isOverdue(a, now) ? 'text-destructive' : 'text-warning'} />
          <span className="font-mono font-semibold">{a.flag}</span>
          {isOverdue(a, now) ? (
            <span className="text-destructive font-medium">
              {t('cax.overdue', { at: when(a.expiresAt, locale), why: a.message || t('cax.noAnswer') })}{' '}
              <code>ceph osd unset {a.flag}</code>
            </span>
          ) : (
            <span>
              {t('cax.setUntil', { at: when(a.expiresAt, locale) })}
              {a.flagWasSet && ` ${t('cax.alreadySet')}`}
            </span>
          )}
          {admin && !a.release && (
            <Button
              size="sm"
              variant="outline"
              onClick={async () => {
                try {
                  await frame.cephActions.release(a)
                  toast.success(t('cax.releaseSent', { flag: a.flag }))
                } catch (e) {
                  toast.error(t('cax.notReleased', { error: errText(e) }))
                }
              }}
            >
              {t('cax.releaseNow')}
            </Button>
          )}
        </div>
      ))}
    </div>
  )
}

/** The banner on its own, for the other Ceph screens. */
export function LiveActiveFlags() {
  const { state } = useLiveResource<CephAction[]>(
    () => frame.cephActions.list(),
    [],
    [frame.cephActions.watchPath()],
    60_000,
    { keepStaleOnError: true },
  )
  return (
    <>
      <StaleNotice state={state} />
      <ActiveFlagsBanner items={state.phase === 'ready' ? state.data : []} admin={isAdmin()} />
    </>
  )
}

function NewAction() {
  const { t, tm } = useI18n()
  const [action, setAction] = useState<CephActionKind>('OSDOut')
  const [osd, setOsd] = useState('')
  const [flag, setFlag] = useState<SettableFlag>('noout')
  const [minutes, setMinutes] = useState(String(DEFAULT_FLAG_MINUTES))
  const [open, setOpen] = useState(false)
  const error = newActionError(action, osd, minutes)
  const isFlag = action === 'SetFlag'
  const osdNum = isFlag ? null : Number(osd.trim())
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-base">{t('cax.gesture')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          {t('cax.intro')}
        </p>
        <div className="flex flex-col sm:flex-row gap-2 sm:items-end">
          <div className="space-y-1 sm:w-64">
            <Label className="text-xs">{t('cax.gestureLabel')}</Label>
            <Select value={action} onValueChange={(v) => setAction(v as CephActionKind)}>
              <SelectTrigger aria-label={t('cax.gestureLabel')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ACTIONS.map((a) => (
                  <SelectItem key={a.value} value={a.value}>
                    {t(a.label)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {isFlag ? (
            <>
              <div className="space-y-1 sm:w-48">
                <Label className="text-xs">{t('cax.flagLabel')}</Label>
                <Select value={flag} onValueChange={(v) => setFlag(v as SettableFlag)}>
                  <SelectTrigger aria-label={t('cax.flagLabel')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SETTABLE_FLAGS.map((f) => (
                      <SelectItem key={f} value={f}>
                        {f}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1 sm:w-32">
                <Label htmlFor="flag-minutes" className="text-xs">
                  {t('cax.minutes')}
                </Label>
                <Input id="flag-minutes" inputMode="numeric" value={minutes} onChange={(e) => setMinutes(e.target.value)} />
              </div>
            </>
          ) : (
            <div className="space-y-1 sm:w-32">
              <Label htmlFor="act-osd" className="text-xs">
                {t('cax.osdNumber')}
              </Label>
              <Input id="act-osd" inputMode="numeric" placeholder="8" value={osd} onChange={(e) => setOsd(e.target.value)} />
            </div>
          )}
          <Button disabled={error !== null} onClick={() => setOpen(true)}>
            {t('cax.send')}
          </Button>
        </div>
        {(osd !== '' || isFlag) && error && <p className="text-xs text-muted-foreground">{tm(error)}</p>}
        <AlertDialog open={open} onOpenChange={setOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t('cax.confirmTitle', { what: t(ACTIONS.find((a) => a.value === action)!.label) })}</AlertDialogTitle>
              <AlertDialogDescription>{tm(describeAction(action, osdNum, flag, Number(minutes)))}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t('cax.back')}</AlertDialogCancel>
              <AlertDialogAction
                onClick={async () => {
                  try {
                    await frame.cephActions.create(action, osdNum, isFlag ? flag : '', Number(minutes))
                    setOsd('')
                    toast.success(t('cax.sent'))
                  } catch (e) {
                    toast.error(t('cax.notSent', { error: errText(e) }))
                  }
                }}
              >
                {t('cax.send')}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}

function ActionRow({ a }: { a: CephAction }) {
  const { t, tm, locale } = useI18n()
  const text = (x: string | Message) => (typeof x === 'string' ? x : tm(x))
  const what = a.action === 'SetFlag' ? `${a.flag} (${t('cax.min', { n: a.durationMinutes })})` : `osd.${a.osdID}`
  return (
    <li className="border-t border-border py-2 text-sm space-y-0.5">
      <div className="flex flex-wrap items-center gap-2">
        {phaseBadge(a, text)}
        <span className="font-semibold">{a.action}</span>
        <span className="font-mono">{what}</span>
        <span className="text-xs text-muted-foreground">{when(a.createdAt, locale)}</span>
      </div>
      {a.message && <p className="text-xs text-muted-foreground">{a.message}</p>}
    </li>
  )
}

/** Storage → Ceph actions: active flags, the form, and the recent gestures. */
export function CephActionsView() {
  const { t, tn } = useI18n()
  const { state } = useLiveResource<CephAction[]>(
    () => frame.cephActions.list(),
    [],
    [frame.cephActions.watchPath()],
    30_000,
    { keepStaleOnError: true },
  )
  const admin = isAdmin()
  const items = state.phase === 'ready' ? state.data : []
  const pending = items.filter((a) => !isTerminalAction(a.phase) && a.phase !== 'Active' && !a.sentAt)
  const running = items.filter((a) => a.phase === 'Pending' && a.sentAt)
  return (
    <div className="space-y-4">
      <ActiveFlagsBanner items={items} admin={admin} />
      {admin ? (
        <NewAction />
      ) : (
        <p className="text-sm text-muted-foreground">{t('cax.adminOnly')}</p>
      )}
      {pending.length > 0 && <p className="text-sm">{tn('cax.waiting', pending.length)}</p>}
      {running.length > 0 && <p className="text-sm">{tn('cax.running', running.length)}</p>}
      <StaleNotice state={state} />
      <LiveStates state={state} emptyLabel={t('cax.none')} />
      {items.length > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">{t('cax.recent')}</CardTitle>
          </CardHeader>
          <CardContent>
            <ul>
              {items.slice(0, 50).map((a) => (
                <ActionRow key={`${a.namespace}/${a.name}`} a={a} />
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
