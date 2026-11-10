import { useState } from 'react'
import { toast } from 'sonner'
import { isAdminToken } from '@/lib/auth'
import { createFrameClient } from '@/lib/frame-sdk'
import {
  activeFlags,
  DEFAULT_FLAG_MINUTES,
  describeAction,
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
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { Flag } from '@phosphor-icons/react'

const frame = createFrameClient()

const ACTIONS: { value: CephActionKind; label: string }[] = [
  { value: 'OSDOut', label: 'Mark an OSD out' },
  { value: 'OSDIn', label: 'Mark an OSD in' },
  { value: 'OSDRestart', label: 'Restart an OSD' },
  { value: 'OSDScrub', label: 'Scrub an OSD' },
  { value: 'OSDDeepScrub', label: 'Deep-scrub an OSD' },
  { value: 'SetFlag', label: 'Set a cluster flag for a while' },
]

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function isAdmin(): boolean {
  // Hides the controls only: the apiserver is what refuses a non-admin write.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  return typeof token === 'string' ? isAdminToken(token) : false
}

function when(iso: string | null): string {
  return iso ? new Date(iso).toLocaleString() : '—'
}

function phaseBadge(phase: string) {
  if (phase === 'Done') return <Badge variant="secondary">Done</Badge>
  if (phase === 'Refused' || phase === 'Failed') return <Badge variant="destructive">{phase}</Badge>
  if (phase === 'Active') return <Badge>Active</Badge>
  return <Badge variant="outline">{phase}</Badge>
}

/**
 * The cluster flags Frame set and still holds, each with its end. Shown on
 * the Ceph screens while one is active: a forgotten noout hides failures.
 */
export function ActiveFlagsBanner({ items, admin }: { items: CephAction[]; admin: boolean }) {
  const flags = activeFlags(items)
  if (flags.length === 0) return null
  return (
    <div role="status" className="rounded-md border border-warning/50 bg-warning/10 p-3 text-sm space-y-1">
      {flags.map((a) => (
        <div key={a.name} className="flex flex-wrap items-center gap-2">
          <Flag className="text-warning" />
          <span className="font-mono font-semibold">{a.flag}</span>
          <span>
            set until {when(a.expiresAt)}
            {a.flagWasSet && ' (it was already set: Frame leaves it set at the end)'}
          </span>
          {admin && !a.release && (
            <Button
              size="sm"
              variant="outline"
              onClick={async () => {
                try {
                  await frame.cephActions.release(a)
                  toast.success(`${a.flag}: release sent`)
                } catch (e) {
                  toast.error(`Not released: ${errText(e)}`)
                }
              }}
            >
              Release now
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
  )
  return <ActiveFlagsBanner items={state.phase === 'ready' ? state.data : []} admin={isAdmin()} />
}

function NewAction() {
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
        <CardTitle className="text-base">Ceph gesture</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Reversible gestures only; none destroys data. Frame checks Ceph before an out or a restart, and sends each gesture
          once. A flag is unset by Frame at the end of its duration.
        </p>
        <div className="flex flex-col sm:flex-row gap-2 sm:items-end">
          <div className="space-y-1 sm:w-64">
            <Label className="text-xs">Gesture</Label>
            <Select value={action} onValueChange={(v) => setAction(v as CephActionKind)}>
              <SelectTrigger aria-label="Gesture">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ACTIONS.map((a) => (
                  <SelectItem key={a.value} value={a.value}>
                    {a.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {isFlag ? (
            <>
              <div className="space-y-1 sm:w-48">
                <Label className="text-xs">Flag</Label>
                <Select value={flag} onValueChange={(v) => setFlag(v as SettableFlag)}>
                  <SelectTrigger aria-label="Flag">
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
                  Minutes
                </Label>
                <Input id="flag-minutes" inputMode="numeric" value={minutes} onChange={(e) => setMinutes(e.target.value)} />
              </div>
            </>
          ) : (
            <div className="space-y-1 sm:w-32">
              <Label htmlFor="act-osd" className="text-xs">
                OSD number
              </Label>
              <Input id="act-osd" inputMode="numeric" placeholder="8" value={osd} onChange={(e) => setOsd(e.target.value)} />
            </div>
          )}
          <Button disabled={error !== null} onClick={() => setOpen(true)}>
            Send
          </Button>
        </div>
        {(osd !== '' || isFlag) && error && <p className="text-xs text-muted-foreground">{error}</p>}
        <AlertDialog open={open} onOpenChange={setOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{ACTIONS.find((a) => a.value === action)?.label}?</AlertDialogTitle>
              <AlertDialogDescription>{describeAction(action, osdNum, flag, Number(minutes))}</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Back</AlertDialogCancel>
              <AlertDialogAction
                onClick={async () => {
                  try {
                    await frame.cephActions.create(action, osdNum, isFlag ? flag : '', Number(minutes))
                    setOsd('')
                    toast.success('Sent to Frame; the result shows below.')
                  } catch (e) {
                    toast.error(`Not sent: ${errText(e)}`)
                  }
                }}
              >
                Send
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  )
}

function ActionRow({ a }: { a: CephAction }) {
  const what = a.action === 'SetFlag' ? `${a.flag} (${a.durationMinutes} min)` : `osd.${a.osdID}`
  return (
    <li className="border-t border-border py-2 text-sm space-y-0.5">
      <div className="flex flex-wrap items-center gap-2">
        {phaseBadge(a.phase)}
        <span className="font-semibold">{a.action}</span>
        <span className="font-mono">{what}</span>
        <span className="text-xs text-muted-foreground">{when(a.createdAt)}</span>
      </div>
      {a.message && <p className="text-xs text-muted-foreground">{a.message}</p>}
    </li>
  )
}

/** Storage → Ceph actions: active flags, the form, and the recent gestures. */
export function CephActionsView() {
  const { state } = useLiveResource<CephAction[]>(
    () => frame.cephActions.list(),
    [],
    [frame.cephActions.watchPath()],
    30_000,
  )
  const admin = isAdmin()
  const items = state.phase === 'ready' ? state.data : []
  const pending = items.filter((a) => !isTerminalAction(a.phase) && a.phase !== 'Active')
  return (
    <div className="space-y-4">
      <ActiveFlagsBanner items={items} admin={admin} />
      {admin ? (
        <NewAction />
      ) : (
        <p className="text-sm text-muted-foreground">Only an admin can send a Ceph gesture.</p>
      )}
      {pending.length > 0 && <p className="text-sm">{pending.length} gesture(s) waiting to be sent.</p>}
      <LiveStates state={state} emptyLabel="No Ceph gesture yet." />
      {items.length > 0 && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Recent gestures</CardTitle>
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
