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
import {
  countPodsOnNode,
  DISRUPTIVE_POWER_ACTIONS,
  stalenessLabel,
  type Machine,
  type PowerAction,
} from '@/lib/machines'

const frame = createFrameClient()

type NodeCarries = 'loading' | 'error' | number

interface PendingAction {
  action: PowerAction
  label: string
  /** Only set for a `DISRUPTIVE_POWER_ACTIONS` member against a machine with a `nodeRef`. */
  nodeCarries?: NodeCarries
}

/**
 * The power/identify/clear-log buttons for one machine, each behind a
 * confirmation dialog that names what the action *does* rather than
 * restating the button's label as a question.
 *
 * For `DISRUPTIVE_POWER_ACTIONS` members (`GracefulShutdown`/`ForceOff`/
 * `ForceRestart`) against a machine that carries a cluster node
 * (`machine.nodeRef` non-empty), the dialog first counts the pods scheduled
 * on that node — `countPodsOnNode` (machines.ts) over `frame.workloads.tree()`,
 * the same pod list the Workloads screen already fetches; there is no
 * narrower read (the only `fieldSelector=spec.nodeName` request in the SDK
 * lives inside `NodeClient.drain()`, which evicts pods as a side effect and
 * is not something a power dialog should call just to count) — and blocks
 * the confirm button until that count (or its failure) is known, so nobody
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
    const disruptive = DISRUPTIVE_POWER_ACTIONS.has(action)
    setPending({ action, label, nodeCarries: disruptive && machine.nodeRef ? 'loading' : undefined })
    if (!disruptive || !machine.nodeRef) return

    void frame.workloads
      .tree()
      .then((tree) => {
        const count = countPodsOnNode(tree, machine.nodeRef)
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
      toast.error(`Failed: ${label}`, {
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
          These actions are for administrators only.
        </div>
      )}

      {machine.lastPowerActionError && (
        <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 font-mono text-xs">
          <Warning className="text-destructive shrink-0 mt-0.5" />
          <div>
            <div className="font-medium text-destructive">
              Last action failed ({machine.lastPowerAction || 'unknown'},{' '}
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
          onClick={() => openDialog('GracefulShutdown', 'Graceful shutdown')}
        >
          Graceful shutdown
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono text-destructive"
          disabled={disabled}
          onClick={() => openDialog('ForceOff', 'Force off')}
        >
          Force off
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono text-destructive"
          disabled={disabled}
          onClick={() => openDialog('ForceRestart', 'Force restart')}
        >
          Force restart
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={disabled}
          onClick={() =>
            openDialog(
              ledOn ? 'IndicatorLedOff' : 'IndicatorLedOn',
              ledOn ? 'Turn the identification LED off' : 'Turn the identification LED on',
            )
          }
        >
          Identification LED
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={disabled}
          onClick={() => openDialog('ClearSEL', 'Clear the event log')}
        >
          Clear the log
        </Button>
      </div>

      <AlertDialog open={pending !== undefined} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="font-mono">
              {pending?.label}: {machine.name}
            </AlertDialogTitle>
            <AlertDialogDescription className="font-mono text-xs space-y-1">
              {pending?.action === 'ClearSEL' && (
                <p>
                  Erases the event-log history kept by the BMC. That log exists to answer "why did
                  this machine restart" — it is the only diagnostic record that survives a power
                  cut. Clearing it destroys the evidence of the very incident you are most likely
                  investigating from this screen. This cannot be undone.
                </p>
              )}
              {(pending?.action === 'IndicatorLedOn' || pending?.action === 'IndicatorLedOff') && (
                <p>Only changes the chassis identification light — no effect on power.</p>
              )}
              {pending?.action === 'On' && <p>Powers the machine on.</p>}
              {pending?.action === 'GracefulShutdown' && (
                <p>
                  Asks the operating system to shut down cleanly — an ACPI signal (the power
                  button), not a guarantee. The operating system has to choose to answer it: a
                  machine whose kernel is hung will ignore it and stay on. If it does not respond,
                  "Force off" is the alternative.
                </p>
              )}
              {pending?.action === 'ForceOff' && (
                <p>
                  Cuts the power immediately, without letting the operating system shut down
                  cleanly — an abrupt stop, not a clean one.
                </p>
              )}
              {pending?.action === 'ForceRestart' && (
                <p>
                  Restarts the machine immediately, without letting the operating system shut down
                  cleanly.
                </p>
              )}
              {pending && DISRUPTIVE_POWER_ACTIONS.has(pending.action) && (
                <p>
                  {machine.nodeRef ? (
                    pending.nodeCarries === 'loading' ? (
                      `Checking node ${machine.nodeRef}…`
                    ) : pending.nodeCarries === 'error' ? (
                      `Could not check how many pods run on node ${machine.nodeRef} — check by hand before continuing.`
                    ) : (
                      <>
                        This machine hosts node <strong>{machine.nodeRef}</strong>, carrying{' '}
                        <strong>{pending.nodeCarries}</strong> pod
                        {pending.nodeCarries === 1 ? '' : 's'}.
                      </>
                    )
                  ) : (
                    "No node recorded (spec.nodeRef is not set) — if this machine does host a " +
                    "cluster node, someone simply forgot to declare it."
                  )}
                </p>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={confirmDisabled}
              onClick={() => pending && void run(pending.action, pending.label)}
            >
              Confirm
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
