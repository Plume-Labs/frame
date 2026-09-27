import { useMemo, useState } from 'react'
import { DiskClaim, createFrameClient } from '@/lib/frame-sdk'
import { describeDivergence, formatIec } from '@/lib/storage'
import { agentFreshness, buildDiskRows, chassisDiskTemp, controllerLines, osdsWithoutMachine } from '@/lib/disk-inventory'
import { useStorageDiagnostic } from '@/components/storage/useStorageDiagnostic'
import { MachineDiskTable } from '@/components/storage/MachineDiskTable'
import { osdDiskLabel } from '@/lib/storage-labels'
import type { Machine } from '@/lib/machines'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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
  const diag = useStorageDiagnostic(null)
  const diskRows = useMemo(
    () => (selected ? buildDiskRows(selected, diag.osds, diag.io, diag.cephDevices, machines) : []),
    [selected, diag.osds, diag.io, diag.cephDevices, machines],
  )
  const vmOsds = osdsWithoutMachine(diag.osds, machines)
  const now = Date.now()

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
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs font-mono">
              <span>node {selected.nodeRef || '—'}</span>
              <span className={selected.reachable ? 'text-accent' : 'text-warning'}>
                BMC {selected.reachable ? 'reachable' : 'unreachable'}
              </span>
              {(() => {
                const f = agentFreshness(selected.storage?.observedAt ?? null, now)
                return (
                  <span className={f.stale ? 'text-warning' : ''}>
                    agent {f.ageS === null ? 'never reported' : `read ${f.ageS} s ago${f.stale ? ' (stale)' : ''}`}
                  </span>
                )
              })()}
              {(() => {
                const t = chassisDiskTemp(selected)
                return <span>{t ? `${t.name} ${t.celsius} °C (chassis)` : 'disk temperature not reported'}</span>
              })()}
              {diag.md
                .filter((a) => a.node === selected.nodeRef)
                .map((a) => (
                  <span key={a.device} className={a.degraded ? 'text-destructive' : ''}>
                    {a.device}: {a.active}/{a.required ?? '?'} active, {a.failed} failed, {a.state}
                  </span>
                ))}
            </div>
            <div className="text-xs font-mono">
              {controllerLines(selected).length === 0 ? (
                <span className="text-muted-foreground">Array controller: not reported by the BMC.</span>
              ) : (
                controllerLines(selected).map((l) => <div key={l}>Controller {l}</div>)
              )}
            </div>
            {diskRows.length === 0 ? (
              <p className="text-xs text-muted-foreground font-mono">No disk reported by the BMC or the node agent.</p>
            ) : (
              <MachineDiskTable rows={diskRows} claims={selectedClaims} />
            )}

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

      {vmOsds.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg">Nodes without a FrameMachine</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-xs font-mono">
            <p className="text-muted-foreground">
              No BMC and no agent publication behind these nodes: their OSD disks are listed from Ceph alone.
            </p>
            <table className="w-full">
              <thead className="text-muted-foreground text-left">
                <tr>
                  <th className="pr-3">node</th>
                  <th className="pr-3">OSD</th>
                  <th className="pr-3">disk</th>
                  <th className="pr-3 text-right">size</th>
                  <th>state</th>
                </tr>
              </thead>
              <tbody>
                {vmOsds.map((o) => (
                  <tr key={o.id} className="border-t border-border">
                    <td className="pr-3">{o.host || '—'}</td>
                    <td className="pr-3">{o.name}</td>
                    <td className="pr-3">{osdDiskLabel(o, diag.zfs, diag.nodeUuids)}</td>
                    <td className="pr-3 text-right">{formatIec(o.bytesTotal)}</td>
                    <td>{o.state}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
