import { StorageEntry, createFrameClient } from '@/lib/frame-sdk'
import { capacityLine, claimGapLine } from '@/lib/storage'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { TONE_TEXT, Tone } from '@/lib/thresholds'
import { Database, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

function phaseTone(phase: string): Tone {
  if (phase === 'Ready') return 'accent'
  if (phase === 'Degraded') return 'destructive'
  return 'muted'
}

/**
 * Every FrameStorage entry — the operator's Proxmox-VE-style declaration of
 * where volumes may live, cluster-scoped. One card per entry: type, class,
 * shared/phase, `capacityLine` (usable before raw, never raw alone) and
 * `claimGapLine` (PVC count vs. labelled count — a gap, not a fault).
 *
 * Also renders where the entry is available (the `Available` condition —
 * "all nodes" or the node list the controller wrote) and the reason behind
 * its phase (the `Healthy` condition's message). A `Degraded` entry with no
 * visible reason reproduces exactly the defect this lot's design calls out
 * in §6a: a state nobody can act on.
 */
export function FrameStorageView() {
  const { state, reload } = useLiveResource<StorageEntry[]>(
    () => frame.storage.list(),
    [],
    [frame.storage.watchPath()],
  )
  const entries = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Database className="text-primary" />
            Storage Entries
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
        <CardContent>
          <p className="text-sm text-muted-foreground">
            FrameStorage — declared places where volumes may live, and what backs them.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No FrameStorage entries declared yet." />

      {entries.length > 0 && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {entries.map((e) => (
            <Card key={e.name}>
              <CardHeader className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <CardTitle className="font-mono text-lg">{e.name}</CardTitle>
                  <Badge variant="outline" className={`font-mono text-[10px] border-current ${TONE_TEXT[phaseTone(e.phase)]}`}>
                    {e.phase}
                  </Badge>
                </div>
                <div className="flex items-center gap-2 flex-wrap">
                  <Badge variant="outline" className="font-mono text-[10px]">
                    {e.type}
                  </Badge>
                  <Badge variant="outline" className="font-mono text-[10px]">
                    {e.storageClassName}
                  </Badge>
                  <Badge variant="outline" className="font-mono text-[10px]">
                    {e.shared ? 'shared' : 'local'}
                  </Badge>
                  {e.adopted && (
                    <Badge variant="outline" className="font-mono text-[10px] text-muted-foreground">
                      adopted
                    </Badge>
                  )}
                </div>
              </CardHeader>
              <CardContent className="space-y-2 text-xs font-mono">
                <div className="text-foreground">{capacityLine(e.capacity)}</div>
                <div className="text-muted-foreground">{claimGapLine(e.storageClassName, e.claims)}</div>
                <div className="text-muted-foreground">
                  Available: {e.available?.message || 'unknown'}
                </div>
                {e.healthy && (
                  <div className={e.healthy.status === 'True' ? 'text-muted-foreground' : TONE_TEXT[phaseTone(e.phase)]}>
                    {e.healthy.message || e.healthy.reason}
                  </div>
                )}
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
