import { useState } from 'react'
import { toast } from 'sonner'
import { FrameNode, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { HardDrives, ArrowClockwise, Trash } from '@phosphor-icons/react'

const frame = createFrameClient()

const STATUS_TONE: Record<string, string> = {
  online: 'text-accent',
  provisioning: 'text-warning',
  degraded: 'text-warning',
  offline: 'text-destructive',
}

const CLASS_TONE: Record<string, string> = {
  HIGH: 'text-accent',
  MEDIUM: 'text-foreground',
  LOW: 'text-muted-foreground',
}

/**
 * The FrameNode CRs the operator manages — the provisioning-side view of the
 * fleet, as opposed to the Nodes screen, which shows the Kubernetes nodes that
 * have actually joined. A machine appears here first (Discovered) and shows up
 * there only once it is a cluster member, so the two lists differing is normal.
 */
export function FrameNodesView() {
  const { state, reload } = useLiveResource<{ items: FrameNode[]; total: number }>(() =>
    frame.nodes.list(),
  )
  const [deleteTarget, setDeleteTarget] = useState<FrameNode | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const nodes = state.phase === 'ready' ? state.data.items : []

  async function remove(node: FrameNode) {
    setBusy(node.id)
    try {
      await frame.nodes.delete(node.id)
      toast.success(`${node.id} deleted`)
      reload()
    } catch (e) {
      toast.error(`Failed to delete ${node.id}`, {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(null)
      setDeleteTarget(null)
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Provisioned Nodes — FrameNode CRs
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
            Machines the Frame operator knows about, from discovery through to cluster membership.
            Use <span className="font-mono">Provision Node</span> on the Nodes screen to add one.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No FrameNode resources — nothing provisioned yet." />

      {state.phase === 'ready' &&
        nodes.map((n) => (
          <Card key={n.id}>
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-0">
                  <CardTitle className="font-mono text-lg truncate">{n.name}</CardTitle>
                  <p className="text-xs text-muted-foreground font-mono">
                    {n.id}
                    {n.zone && ` · ${n.zone}`}
                    {n.rackId && ` · rack ${n.rackId}`}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Badge
                    variant="outline"
                    className={`font-mono text-[10px] border-current ${CLASS_TONE[n.serviceClass] ?? 'text-foreground'}`}
                  >
                    {n.serviceClass}
                  </Badge>
                  <Badge
                    variant="outline"
                    className={`font-mono border-current ${STATUS_TONE[n.status] ?? 'text-muted-foreground'}`}
                  >
                    {n.status}
                  </Badge>
                  <Button
                    variant="outline"
                    size="sm"
                    className="font-mono gap-1.5"
                    disabled={busy === n.id}
                    onClick={() => setDeleteTarget(n)}
                  >
                    <Trash />
                    Delete
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4 text-xs font-mono">
              <Field label="CPU" value={n.cpu ? `${n.cpu} cores` : '—'} />
              <Field label="Memory" value={n.memory ? `${n.memory.toFixed(0)} GiB` : '—'} />
              <Field label="GPUs" value={n.gpuCount ? `${n.gpuCount} × ${n.gpuModel}` : 'none'} />
              <Field label="Rack" value={n.rackId || '—'} />
            </CardContent>
          </Card>
        ))}

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {deleteTarget?.id}?</AlertDialogTitle>
            <AlertDialogDescription>
              Removes the FrameNode resource, so the operator stops managing this machine. It does
              not wipe, power off, or remove the node from Kubernetes — if it has already joined,
              cordon and drain it from the Nodes screen first. Re-provisioning re-creates the
              resource from scratch.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => deleteTarget && remove(deleteTarget)}>
              Delete resource
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="space-y-1">
      <div className="text-[10px] text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className="text-foreground">{value}</div>
    </div>
  )
}
