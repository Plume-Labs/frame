import { useState } from 'react'
import { Warning } from '@phosphor-icons/react'
import { toast } from 'sonner'

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
import { Button } from '@/components/ui/button'
import { createFrameClient } from '@/lib/frame-sdk'
import { stalenessLabel, type Machine } from '@/lib/machines'

const frame = createFrameClient()

/**
 * The six power/BMC actions the console offers, mapped 1:1 to
 * `PowerAction` in `api/frame/v1beta1/framemachine_types.go` minus the two
 * the brief says never to surface: `Nmi` (a debugging interrupt, not an
 * operator action) and `PushPowerButton` (what `GracefulShutdown` resolves
 * to on hardware whose Redfish `Actions#ComputerSystem.Reset` doesn't list
 * `GracefulShutdown` itself — see `resolveResetType` in
 * `internal/redfish/client.go`. The button stays labelled as a graceful
 * shutdown because that is what the operator means; the resolution is an
 * implementation detail of talking to this iLO4, not a different action.
 */
type PowerAction =
  | 'On'
  | 'GracefulShutdown'
  | 'ForceOff'
  | 'ForceRestart'
  | 'ClearSEL'
  | 'IndicatorLedOn'
  | 'IndicatorLedOff'

/**
 * Actions that end or interrupt whatever the machine is currently doing —
 * the ones where a cluster node's workload is the consequence, not a
 * detail. `On`, the LED toggle and clearing the log touch nothing that is
 * running, so they get a plain confirmation with no pod count.
 */
const DISRUPTIVE = new Set<PowerAction>(['GracefulShutdown', 'ForceOff', 'ForceRestart'])

type NodeCarries = 'loading' | 'error' | number

interface PendingAction {
  action: PowerAction
  label: string
  /** Only set for a DISRUPTIVE action against a machine with a `nodeRef`. */
  nodeCarries?: NodeCarries
}

/**
 * The power/identify/clear-log buttons for one machine, each behind a
 * confirmation dialog that names what the action *does* rather than
 * restating the button's label as a question.
 *
 * For `GracefulShutdown`/`ForceOff`/`ForceRestart` against a machine that
 * carries a cluster node (`machine.nodeRef` non-empty), the dialog first
 * counts the pods scheduled on that node — via `frame.workloads.tree()`,
 * the same pod list the Workloads screen already fetches and filters by
 * `nodeName` client-side; there is no narrower read (the only
 * `fieldSelector=spec.nodeName` request in the SDK lives inside
 * `NodeClient.drain()`, which evicts pods as a side effect and is not
 * something a power dialog should call just to count) — and blocks the
 * confirm button until that count (or its failure) is known, so nobody
 * approves powering off a machine before being told what runs on it. When
 * `nodeRef` is empty, the dialog says so in place of the count: the
 * absence is information too, not a reason to say nothing.
 *
 * `machine.lastPowerActionError` is rendered here, not just toasted: a
 * toast is gone by the time someone glances back at the screen, and
 * `lastPowerAction`/`lastPowerActionAt` advance whether the action
 * succeeded or failed (deliberately, so a failing action is not retried
 * forever) — meaning a failed force-off looks identical to a successful one
 * unless the error has a permanent home on the panel.
 */
export function MachineActions({ machine, admin }: { machine: Machine; admin: boolean }) {
  const [pending, setPending] = useState<PendingAction | undefined>()
  const [busy, setBusy] = useState(false)

  const openDialog = (action: PowerAction, label: string) => {
    const disruptive = DISRUPTIVE.has(action)
    setPending({ action, label, nodeCarries: disruptive && machine.nodeRef ? 'loading' : undefined })
    if (!disruptive || !machine.nodeRef) return

    void frame.workloads
      .tree()
      .then((tree) => {
        const count = tree.reduce((total, ns) => {
          const controllerPods = ns.controllers.reduce(
            (n, c) => n + c.pods.filter((p) => p.nodeName === machine.nodeRef).length,
            0,
          )
          const barePods = ns.barePods.filter((p) => p.nodeName === machine.nodeRef).length
          return total + controllerPods + barePods
        }, 0)
        // Guards against the dialog having been reassigned to a different
        // action (or closed and reopened) while this request was in
        // flight — a stale count for the wrong action is worse than none.
        setPending((prev) => (prev?.action === action ? { ...prev, nodeCarries: count } : prev))
      })
      .catch(() => {
        setPending((prev) => (prev?.action === action ? { ...prev, nodeCarries: 'error' } : prev))
      })
  }

  const run = async (action: PowerAction, label: string) => {
    setPending(undefined)
    setBusy(true)
    try {
      await frame.machines.power(machine, action)
      toast.success(label)
    } catch (e) {
      toast.error(`Échec : ${label}`, {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(false)
    }
  }

  const ledOn = machine.indicatorLED === 'Lit'
  const disabled = !admin || busy
  // A disruptive dialog whose node count is still in flight (or failed to
  // resolve) cannot be confirmed — confirming before the count is known is
  // exactly the failure this dialog exists to prevent.
  const confirmDisabled =
    disabled || (pending !== undefined && (pending.nodeCarries === 'loading' || pending.nodeCarries === 'error'))

  return (
    <div className="space-y-3">
      {!admin && (
        <div className="font-mono text-[10px] text-muted-foreground">
          Actions réservées aux administrateurs.
        </div>
      )}

      {machine.lastPowerActionError && (
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 font-mono text-xs">
          <Warning className="text-destructive shrink-0 mt-0.5" />
          <div>
            <div className="font-medium text-destructive">
              Échec de la dernière action ({machine.lastPowerAction || 'inconnue'},{' '}
              {stalenessLabel(machine.lastPowerActionAt, new Date())})
            </div>
            <div className="text-muted-foreground mt-0.5">{machine.lastPowerActionError}</div>
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={disabled}
          onClick={() => openDialog('On', 'Allumer')}
        >
          Allumer
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={disabled}
          onClick={() => openDialog('GracefulShutdown', 'Extinction propre')}
        >
          Extinction propre
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono text-destructive"
          disabled={disabled}
          onClick={() => openDialog('ForceOff', 'Extinction forcée')}
        >
          Extinction forcée
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono text-destructive"
          disabled={disabled}
          onClick={() => openDialog('ForceRestart', 'Redémarrage forcé')}
        >
          Redémarrage forcé
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={disabled}
          onClick={() =>
            openDialog(
              ledOn ? 'IndicatorLedOff' : 'IndicatorLedOn',
              ledOn ? 'Éteindre le repère lumineux' : 'Allumer le repère lumineux',
            )
          }
        >
          LED d'identification
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={disabled}
          onClick={() => openDialog('ClearSEL', "Effacer le journal d'événements")}
        >
          Effacer le journal
        </Button>
      </div>

      <AlertDialog open={pending !== undefined} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">
              {pending?.label} de {machine.name}
            </AlertDialogTitle>
            <AlertDialogDescription className="font-mono text-xs space-y-1">
              {pending?.action === 'ClearSEL' && (
                <p>
                  Efface l'historique du journal d'événements conservé par le BMC. Cette action est
                  irréversible.
                </p>
              )}
              {(pending?.action === 'IndicatorLedOn' || pending?.action === 'IndicatorLedOff') && (
                <p>Ne modifie que le repère lumineux du châssis — aucun effet sur l'alimentation.</p>
              )}
              {pending?.action === 'On' && <p>Démarre la machine.</p>}
              {pending?.action === 'ForceOff' && (
                <p>
                  Coupe l'alimentation immédiatement, sans laisser le système d'exploitation s'arrêter
                  proprement — un arrêt brutal, pas un arrêt propre.
                </p>
              )}
              {pending?.action === 'ForceRestart' && (
                <p>
                  Redémarre la machine immédiatement, sans laisser le système d'exploitation s'arrêter
                  proprement.
                </p>
              )}
              {pending && DISRUPTIVE.has(pending.action) && (
                <p>
                  {machine.nodeRef ? (
                    pending.nodeCarries === 'loading' ? (
                      `Vérification du nœud ${machine.nodeRef}…`
                    ) : pending.nodeCarries === 'error' ? (
                      `Impossible de vérifier le nombre de pods sur le nœud ${machine.nodeRef} — vérifiez manuellement avant de continuer.`
                    ) : (
                      <>
                        Cette machine porte le nœud <strong>{machine.nodeRef}</strong>, avec{' '}
                        <strong>{pending.nodeCarries}</strong> pod
                        {pending.nodeCarries === 1 ? '' : 's'}.
                      </>
                    )
                  ) : (
                    'Cette machine ne porte aucun nœud du cluster.'
                  )}
                </p>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Annuler</AlertDialogCancel>
            <AlertDialogAction
              disabled={confirmDisabled}
              onClick={() => pending && void run(pending.action, pending.label)}
            >
              Confirmer
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
