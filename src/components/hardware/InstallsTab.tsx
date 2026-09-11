import { useEffect, useMemo, useState } from 'react'
import { HardDrives, Plus, Warning } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LiveStates } from '@/components/LiveStates'
import { InstallDialog } from '@/components/hardware/InstallDialog'
import { useLiveResource } from '@/hooks/useLiveResource'
import { isAdminToken } from '@/lib/auth'
import { createFrameClient } from '@/lib/frame-sdk'
import {
  canCreateInstall,
  elapsedLabel,
  isStalledInPhase,
  phaseTone,
  type Install,
} from '@/lib/installs'
import type { Machine } from '@/lib/machines'
import { TONE_TEXT, type Tone } from '@/lib/thresholds'

const frame = createFrameClient()

const PHASE_TONE_CLASS: Record<ReturnType<typeof phaseTone>, Tone> = {
  pending: 'muted',
  running: 'primary',
  good: 'accent',
  bad: 'destructive',
}

/**
 * Every `FrameInstall` on the cluster: which machine, which phase, which
 * node it produced, and how long it has been sitting there. The admin tier
 * (the RBAC tier `canCreateInstall` keys off — see installs.ts) gets a
 * button to start a new one; nobody else does, because creating one wipes
 * the named disks and the screen should agree with the RBAC rather than
 * offer a write the apiserver will refuse.
 *
 * `now` ticks on its own second, independent of the watch: `elapsedLabel`
 * and `isStalledInPhase` both take it as a parameter rather than reading
 * `new Date()` internally (see installs.ts's doc comment on `elapsedLabel`)
 * precisely so this screen does not repeat the freshness-marker bug
 * `HardwareView` shipped and fixed — a value computed at render under a
 * watch with no poll freezes the moment the last event lands, which is
 * exactly the moment someone watching an install stall needs it to keep
 * advancing.
 */
export function InstallsTab() {
  const { state, reload } = useLiveResource<Install[]>(
    () => frame.installs.list(),
    [],
    [frame.installs.watchPath()],
    30_000,
  )
  const installs = useMemo(() => (state.phase === 'ready' ? state.data : []), [state])

  // The dialog's machine picker needs its own read of FrameMachine — this
  // tab and HardwareView are mounted independently (App.tsx's Tabs renders
  // every tab's content, not just the active one), so there is no shared
  // parent to fetch it once and pass down. `k8sFetch`'s in-flight dedup
  // (frame-sdk.ts) makes two screens reading the same list concurrently
  // free rather than doubling the request.
  const { state: machineState } = useLiveResource<Machine[]>(
    () => frame.machines.list(),
    [],
    [frame.machines.watchPath()],
    30_000,
  )
  const machines = useMemo(() => (machineState.phase === 'ready' ? machineState.data : []), [machineState])

  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1_000)
    return () => clearInterval(id)
  }, [])

  const [dialogOpen, setDialogOpen] = useState(false)

  // The admin check mirrors MachineDetail's: the token is decoded, not
  // verified, so this is a courtesy that hides the button — a create is
  // refused server-side by RBAC for anyone else regardless of what renders.
  const token = (globalThis as Record<string, unknown>).__FRAME_TOKEN__
  const admin = typeof token === 'string' ? isAdminToken(token) : false
  const canCreate = canCreateInstall(admin ? 'admin' : '')

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Installs
            {canCreate && (
              <Button
                size="sm"
                className="ml-auto font-mono gap-1.5"
                onClick={() => setDialogOpen(true)}
              >
                <Plus />
                New install
              </Button>
            )}
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && installs.length > 0 && (
          <CardContent className="space-y-2">
            <div className="border rounded-md divide-y">
              {installs.map((install) => {
                const tone = PHASE_TONE_CLASS[phaseTone(install.phase)]
                const stalled = isStalledInPhase(install.phase, install.phaseSince, now)
                return (
                  <div
                    key={`${install.namespace}/${install.name}`}
                    className="flex flex-col gap-1 px-3 py-2 font-mono text-xs"
                  >
                    <div className="flex items-center gap-3">
                      <span className="flex-1 truncate">{install.machineRef}</span>
                      <span className="text-muted-foreground w-40 truncate">{install.hostname}</span>
                      <Badge variant="outline" className={`text-[10px] ${TONE_TEXT[tone]}`}>
                        {install.phase}
                      </Badge>
                      <span className="text-muted-foreground w-40 truncate">
                        node {install.nodeName || '—'}
                      </span>
                      <span className={`w-28 text-right ${stalled ? 'text-warning' : 'text-muted-foreground'}`}>
                        {elapsedLabel(install.phaseSince, now)}
                      </span>
                    </div>
                    {stalled && (
                      <div className="flex items-center gap-1.5 text-warning">
                        <Warning className="shrink-0" size={12} />
                        Longer than expected in {install.phase} — check the machine directly.
                      </div>
                    )}
                    {install.phase === 'Failed' && install.message && (
                      <div className="flex items-start gap-1.5 text-destructive">
                        <Warning className="shrink-0 mt-0.5" size={12} />
                        <span>
                          {install.failedPhase ? `Failed in ${install.failedPhase}: ` : ''}
                          {install.message}
                        </span>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No installations yet." />

      <InstallDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        machines={machines}
        onCreated={reload}
      />
    </div>
  )
}
