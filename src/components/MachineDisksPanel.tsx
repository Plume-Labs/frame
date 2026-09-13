import { useMemo, useState } from 'react'
import { DiskClaim, createFrameClient } from '@/lib/frame-sdk'
import { describeDivergence } from '@/lib/storage'
import type { Machine } from '@/lib/machines'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ArrowClockwise, HardDrives, Warning } from '@phosphor-icons/react'

const frame = createFrameClient()

/**
 * The two-source disk inventory, one machine at a time: `inventory.drives`
 * (the BMC's list) and `storage.observed` (the node kernel's), side by
 * side, never merged — the gap between them is the datum this lot exists
 * to surface, and merging the two lists on screen would destroy the one
 * fact Proxmox VE cannot state. Divergences render below, through
 * `describeDivergence`, which is deliberately never fault language for a
 * `bmc-only` entry: on the captured hardware the BMC reports that disk
 * Health OK.
 *
 * Any FrameDiskClaim naming the selected machine renders at the bottom —
 * a claim is a request against one of these disks, not a third source of
 * truth about them, so it never enters either list above.
 */
export function MachineDisksPanel() {
  const { state, reload } = useLiveResource<Machine[]>(
    () => frame.machines.list(),
    [],
    [frame.machines.watchPath()],
    30_000,
  )
  const { state: claimsState } = useLiveResource<DiskClaim[]>(
    () => frame.diskClaims.list(),
    [],
    [frame.diskClaims.watchPath()],
  )
  const machines = useMemo(() => (state.phase === 'ready' ? state.data : []), [state])
  const claims = claimsState.phase === 'ready' ? claimsState.data : []

  const [query, setQuery] = useState('')
  const [selectedName, setSelectedName] = useState<string | undefined>()

  const q = query.trim().toLowerCase()
  const visible = useMemo(
    () => machines.filter((m) => !q || m.name.toLowerCase().includes(q)),
    [machines, q],
  )
  const selected = useMemo(() => machines.find((m) => m.name === selectedName), [machines, selectedName])
  const selectedClaims = selected ? claims.filter((c) => c.machineName === selected.name) : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Disks
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
              placeholder="Filter by machine name…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="max-w-xs font-mono text-xs"
            />
            <div className="border rounded-md divide-y">
              {visible.map((m) => (
                <button
                  key={m.name}
                  type="button"
                  onClick={() => setSelectedName(m.name)}
                  className={`w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 font-mono text-xs ${
                    selectedName === m.name ? 'bg-muted/50' : ''
                  }`}
                >
                  <span className="flex-1 truncate">{m.name}</span>
                  <span className="text-muted-foreground">
                    {(m.storage?.divergences.length ?? 0) > 0
                      ? `${m.storage!.divergences.length} divergence(s)`
                      : m.storage
                        ? 'no divergence'
                        : 'never reported'}
                  </span>
                </button>
              ))}
            </div>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No machines are registered yet." />

      {selected && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg">{selected.name}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <div className="space-y-2">
                <div className="text-xs text-muted-foreground uppercase tracking-wide">
                  BMC inventory (inventory.drives)
                </div>
                {(selected.inventory?.drives ?? []).length === 0 ? (
                  <p className="text-xs text-muted-foreground font-mono">No drives reported by the BMC.</p>
                ) : (
                  <div className="space-y-1">
                    {(selected.inventory?.drives ?? []).map((d, i) => (
                      <div
                        key={`${d.serialNumber || d.name}-${i}`}
                        className="p-2 rounded-md border border-border bg-secondary/30 text-xs font-mono space-y-0.5"
                      >
                        <div className="flex items-center justify-between">
                          <span className="truncate">{d.serialNumber || d.name || '—'}</span>
                          <Badge variant="outline" className="text-[10px]">
                            {d.sizeGB ? `${d.sizeGB} GB` : '—'}
                          </Badge>
                        </div>
                        <div className="text-muted-foreground">
                          {d.location || '—'} · {d.protocol || d.mediaType || '—'} · {d.health || 'unknown'}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              <div className="space-y-2">
                <div className="text-xs text-muted-foreground uppercase tracking-wide">
                  Node-reported (storage.observed)
                </div>
                {!selected.storage ? (
                  <p className="text-xs text-muted-foreground font-mono">
                    Never reported by this node's agent.
                  </p>
                ) : selected.storage.observed.length === 0 ? (
                  <p className="text-xs text-muted-foreground font-mono">Agent reports no disks.</p>
                ) : (
                  <div className="space-y-1">
                    {selected.storage.observed.map((d, i) => (
                      <div
                        key={`${d.serialNumber}-${i}`}
                        className="p-2 rounded-md border border-border bg-secondary/30 text-xs font-mono space-y-0.5"
                      >
                        <div className="flex items-center justify-between">
                          <span className="truncate">{d.serialNumber}</span>
                          <Badge variant="outline" className="text-[10px]">
                            {d.sizeGB ? `${d.sizeGB} GB` : '—'}
                          </Badge>
                        </div>
                        <div className="text-muted-foreground truncate">
                          {d.path} · {d.occupancy}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>

            <div className="space-y-2">
              <div className="text-xs text-muted-foreground uppercase tracking-wide flex items-center gap-1">
                <Warning size={12} />
                Divergences
              </div>
              {(selected.storage?.divergences ?? []).length === 0 ? (
                <p className="text-xs text-muted-foreground font-mono">
                  {selected.storage ? 'No divergence between the two sources.' : 'Nothing to compare yet.'}
                </p>
              ) : (
                <ul className="text-xs font-mono space-y-1 list-disc list-inside">
                  {selected.storage!.divergences.map((d, i) => (
                    <li key={`${d.serialNumber}-${i}`}>{describeDivergence(d)}</li>
                  ))}
                </ul>
              )}
            </div>

            {selectedClaims.length > 0 && (
              <div className="space-y-2">
                <div className="text-xs text-muted-foreground uppercase tracking-wide">Disk claims</div>
                <ul className="text-xs font-mono space-y-1">
                  {selectedClaims.map((c) => (
                    <li key={c.name} className="flex items-center justify-between gap-2">
                      <span className="truncate">
                        {c.serial} → {c.destination}
                      </span>
                      <span className="text-muted-foreground truncate">
                        {c.phase}
                        {c.message ? ` — ${c.message}` : ''}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
