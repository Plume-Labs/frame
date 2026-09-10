import { useMemo, useState } from 'react'
import { ArrowClockwise, HardDrives } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { LiveStates } from '@/components/LiveStates'
import { MachineDetail } from '@/components/hardware/MachineDetail'
import { useLiveResource } from '@/hooks/useLiveResource'
import { createFrameClient } from '@/lib/frame-sdk'
import { isStale, NEVER_READ_LABEL, stalenessLabel, type Machine } from '@/lib/machines'

const frame = createFrameClient()

/**
 * Every physical machine the operator has discovered over Redfish: a list on
 * the left, an inventory-and-sensors panel on the right.
 *
 * The BMC on this hardware replays cached sensor readings as if they were
 * live (see `src/lib/machines.ts`'s header comment), so the controller only
 * ever hands the console a reading it can vouch for — `machine.sensors` is
 * null whenever the machine is not powered on with POST finished.
 * `MachineDetail` is what turns that null into an actionable statement
 * instead of a blank panel.
 *
 * The freshness marker in each row and the staleness in the panel both read
 * `machine.lastProbeAt` — the machine's own liveness — which is a different
 * clock from `machine.sensorsValidAt` (the sensors' own age), read only
 * inside the Capteurs tab. The two are never mixed here.
 */
export function HardwareView() {
  const { state, reload } = useLiveResource<Machine[]>(
    () => frame.machines.list(),
    [],
    [frame.machines.watchPath()],
    // The watch deliberately ignores BOOKMARKs (see the SDK), and every
    // freshness marker on this screen and its detail panel is computed as
    // `new Date()` at render — HardwareView, MachineDetail and SensorsTab
    // alike. Without a re-render trigger of its own, a controller that
    // stops writing (crash, node down, lost leader election — cmd/main.go
    // documents this happening seventeen times in four days) freezes every
    // one of those "last probe Ns ago" labels at whatever they read the
    // moment the last watch event landed, which is exactly the moment
    // someone reading temperatures during an incident needs them to keep
    // advancing. Slow poll, same precedent and same reasoning as
    // ClusterNodesView.tsx: the watch already covers every real change to
    // the object, this only needs to force a re-render often enough that
    // the clock on screen is never more than 30s stale relative to now.
    30_000,
  )
  const machines = useMemo(() => (state.phase === 'ready' ? state.data : []), [state])

  const [query, setQuery] = useState('')
  const [selectedName, setSelectedName] = useState<string | undefined>()

  const q = query.trim().toLowerCase()
  const visible = useMemo(
    () =>
      machines.filter(
        (m) =>
          !q ||
          m.name.toLowerCase().includes(q) ||
          (m.inventory?.model ?? '').toLowerCase().includes(q),
      ),
    [machines, q],
  )

  // Mirrors WorkloadsView's selection effect: `selectedName` is an identity,
  // re-resolved against the latest `machines` on every render, so a refresh
  // triggered from inside the panel (or the watch firing) never leaves the
  // panel rendering the stale object captured the moment the row was
  // clicked.
  const selected = useMemo(
    () => machines.find((m) => m.name === selectedName),
    [machines, selectedName],
  )

  const now = new Date()

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Hardware
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
        {state.phase === 'ready' && (
          <CardContent className="space-y-3">
            <Input
              placeholder="Filter by name or model…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="max-w-xs font-mono text-xs"
            />
            <div className="border rounded-md divide-y">
              {visible.map((m) => {
                const stale = isStale(m.lastProbeAt, now)
                return (
                  <button
                    key={m.name}
                    type="button"
                    onClick={() => setSelectedName(m.name)}
                    className={`w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 font-mono text-xs ${
                      selectedName === m.name ? 'bg-muted/50' : ''
                    }`}
                  >
                    <span className="flex-1 truncate">{m.name}</span>
                    <span className="text-muted-foreground w-48 truncate">
                      {m.inventory?.model ?? NEVER_READ_LABEL}
                    </span>
                    <Badge variant="outline" className="font-mono text-[10px]">
                      {m.powerState || 'Unknown'}
                    </Badge>
                    <span className={`w-28 text-right ${stale ? 'text-warning' : 'text-muted-foreground'}`}>
                      {stalenessLabel(m.lastProbeAt, now)}
                    </span>
                  </button>
                )
              })}
            </div>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No machines are registered yet." />

      {selected && (
        <MachineDetail
          key={selected.name}
          machine={selected}
          onClose={() => setSelectedName(undefined)}
        />
      )}
    </div>
  )
}
