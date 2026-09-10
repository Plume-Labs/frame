import { Warning, X } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { InventoryTab } from '@/components/hardware/InventoryTab'
import { SensorsTab } from '@/components/hardware/SensorsTab'
import { isStale, stalenessLabel, type Machine } from '@/lib/machines'

/**
 * One physical machine: what it is (Inventaire) and what it is reporting
 * right now (Capteurs). Task 10 adds a third tab (event log) alongside
 * these two.
 *
 * Every panel renders `stalenessLabel(machine.lastProbeAt, …)` — the
 * machine's own liveness. `isStale` on that same clock greys the body: a
 * value read from a machine the console hasn't heard from in a while is a
 * value someone reading this during an incident needs to distrust. This is
 * deliberately `lastProbeAt`, not `sensorsValidAt` — the sensors' own age is
 * a separate clock, read only inside `SensorsTab` (see machines.ts's
 * `isStale` doc comment: a machine probed 30s ago can still be carrying
 * sensor readings that are hours stale).
 *
 * When `machine.reachable` is false, `reachableReason`/`reachableMessage`
 * are shown verbatim rather than folded into a generic "unavailable" — a
 * `TLSError` on a freshly registered iLO4 is the expected first state, and
 * the message names exactly what to fix.
 */
export function MachineDetail({
  machine,
  onClose,
}: {
  machine: Machine
  onClose: () => void
}) {
  const now = new Date()
  const stale = isStale(machine.lastProbeAt, now)

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <span className="truncate">{machine.name}</span>
          <Badge variant="outline" className="font-mono text-[10px]">
            {machine.powerState || 'Unknown'}
          </Badge>
          {machine.postState && (
            <Badge variant="outline" className="font-mono text-[10px]">
              {machine.postState}
            </Badge>
          )}
          <Button variant="ghost" size="sm" className="ml-auto" onClick={onClose} aria-label="Close">
            <X />
          </Button>
        </CardTitle>
        <div className="font-mono text-[10px] text-muted-foreground flex flex-wrap gap-x-4">
          <span>address {machine.address || '—'}</span>
          <span>node {machine.nodeRef || '—'}</span>
          <span className={stale ? 'text-warning font-medium' : undefined}>
            last probe {stalenessLabel(machine.lastProbeAt, now)}
          </span>
        </div>
        {!machine.reachable && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs">
            <Warning className="text-destructive shrink-0 mt-0.5" />
            <div>
              <div className="font-mono font-medium text-destructive">
                Not reachable — {machine.reachableReason || 'Unknown reason'}
              </div>
              {machine.reachableMessage && (
                <div className="text-muted-foreground mt-0.5">{machine.reachableMessage}</div>
              )}
            </div>
          </div>
        )}
      </CardHeader>
      <CardContent className={stale ? 'opacity-60' : undefined}>
        <Tabs defaultValue="inventory" className="gap-4">
          <TabsList>
            <TabsTrigger value="inventory" className="font-mono text-xs">
              Inventaire
            </TabsTrigger>
            <TabsTrigger value="sensors" className="font-mono text-xs">
              Capteurs
            </TabsTrigger>
          </TabsList>
          <TabsContent value="inventory">
            <InventoryTab machine={machine} />
          </TabsContent>
          <TabsContent value="sensors">
            <SensorsTab machine={machine} />
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  )
}
