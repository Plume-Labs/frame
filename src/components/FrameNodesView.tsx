import { useState } from 'react'
import { toast } from 'sonner'
import { FrameNode, createFrameClient, frameListPath, coreListPath } from '@/lib/frame-sdk'
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
import { useI18n } from '@/i18n/context'
import { richText } from '@/i18n/rich'

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
 * The FrameNode CRs the operator manages — the classification side of the
 * fleet (role, rack, zone, service class, projected as labels onto the
 * Kubernetes node), as opposed to the Nodes screen, which reads the Kubernetes
 * nodes themselves. A node with no FrameNode (a machine that joined but was
 * never classified) is on the Nodes screen and not here, so the two lists
 * differing is normal.
 */
export function FrameNodesView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<{ items: FrameNode[]; total: number }>(
    () => frame.nodes.list(),
    [],
    // Both sides move: the FrameNode as it is edited, and the Kubernetes
    // Node whose readiness the controller syncs back into its status.
    [frameListPath('framenodes'), coreListPath('nodes')],
  )
  const [deleteTarget, setDeleteTarget] = useState<FrameNode | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const nodes = state.phase === 'ready' ? state.data.items : []

  async function remove(node: FrameNode) {
    setBusy(node.id)
    try {
      await frame.nodes.delete(node.id)
      toast.success(t('fn.deleted', { id: node.id }))
      reload()
    } catch (e) {
      toast.error(t('fn.deleteFailed', { id: node.id }), {
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
            {t('fn.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {richText(t('fn.desc'), {
              machine: <span className="font-mono">FrameMachine</span>,
              install: <span className="font-mono">FrameInstall</span>,
            })}
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel={t('fn.empty')} />

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
                    {n.rackId && ` · ${t('fn.rack', { id: n.rackId })}`}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Badge
                    variant="outline"
                    className={`font-mono text-[10px] border-current ${CLASS_TONE[n.serviceClass] ?? 'text-foreground'}`}
                  >
                    {n.serviceClass || t('fn.unclassified')}
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
                    {t('common.delete')}
                  </Button>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4 text-xs font-mono">
              <Field label={t('fn.cpu')} value={n.cpu ? t('fn.cores', { n: n.cpu }) : '—'} />
              <Field label={t('fn.memory')} value={n.memory ? `${n.memory.toFixed(0)} GiB` : '—'} />
              <Field label={t('fn.gpus')} value={n.gpuCount ? `${n.gpuCount} × ${n.gpuModel}` : t('fn.none')} />
              <Field label={t('fn.rackLabel')} value={n.rackId || '—'} />
            </CardContent>
          </Card>
        ))}

      <AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('fn.confirmTitle', { id: deleteTarget?.id ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('fn.confirmDesc')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={() => deleteTarget && remove(deleteTarget)}>
              {t('fn.deleteResource')}
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
