import { useState } from 'react'
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
import { Input } from '@/components/ui/input'
import type { PodSelection } from '@/components/workloads/PodDetailPanel'
import { createFrameClient } from '@/lib/frame-sdk'
import { canOperateWorkloads } from '@/lib/workloads'

const frame = createFrameClient()

type Pending = 'restart' | 'scale' | 'delete' | undefined

/**
 * Restart, scale and delete-a-pod, each behind a dialog that names the object
 * and the consequence.
 *
 * The delete dialog says up to three different things, because it can be three
 * different actions behind one button. Under a controller this screen
 * resolved — one of the four tracked kinds, placed by `buildWorkloadTree` — the
 * pod is replaced and the dialog names what will replace it. Under an owner
 * this screen could not resolve (an orphaned or hand-created ReplicaSet, or
 * any controller kind the tree does not track), the dialog says an owner
 * exists and the pod will most likely come back, without naming a controller
 * it cannot vouch for — that judgment comes from `pod.owner`, read straight
 * off the pod's own `ownerReferences`, not from tree placement, precisely so
 * an owned-but-unresolved pod is never told apart from a genuinely bare one.
 * On a truly bare pod — no `ownerReferences` at all — the deletion is final.
 * The person clicking has to know which of the three they have.
 *
 * Restart and Scale are not offered outside the namespaces where they are
 * granted, and the screen says why instead of leaving a button that returns
 * 403. The grant is a RoleBinding in each namespace enforcing `baseline` Pod
 * Security (`deploy/kubernetes/base/rbac-workload-operator.yaml`), because
 * patching a pod template is node root wherever a privileged pod is still
 * admitted — so the asymmetry is the security control, not a rollout gap, and
 * it will not go away.
 *
 * Delete pod is not gated: `pods delete` is a cluster-wide operator grant and
 * deleting a pod cannot mint a privileged one.
 */
export function WorkloadActions({
  selection,
  onChanged,
}: {
  selection: PodSelection
  onChanged: () => void
}) {
  const { pod, controller } = selection
  const [pending, setPending] = useState<Pending>()
  const [replicas, setReplicas] = useState(String(controller?.desiredReplicas ?? 1))
  const [busy, setBusy] = useState(false)

  const run = async (what: string, fn: () => Promise<void>) => {
    setPending(undefined)
    setBusy(true)
    try {
      await fn()
      toast.success(what)
      onChanged()
    } catch (e) {
      toast.error(`Failed: ${what}`, {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(false)
    }
  }

  // Whether the grant reaches this namespace at all, decided from the same
  // list the RoleBindings are generated from — not from "is this
  // infrastructure", which would offer the button in a namespace that is
  // merely unlabelled.
  const operable = canOperateWorkloads(pod.namespace)

  // DaemonSet deliberately excluded: cluster-control-workload-operator does
  // not grant `patch` on daemonsets (see the comment on that rule in
  // deploy/kubernetes/base/rbac-workload-operator.yaml — restart is offered
  // only for a Deployment or StatefulSet controller). Offering this button
  // for a DaemonSet would 403 for every operator, and only silently work for
  // an admin, whose tier does hold it — whole-branch review Important 2.
  const restartable =
    operable && controller && (controller.kind === 'Deployment' || controller.kind === 'StatefulSet')

  const target = Number(replicas)
  const targetValid = Number.isInteger(target) && target >= 0

  return (
    <div className="flex flex-wrap items-center gap-2">
      {!operable && controller && (
        <span className="font-mono text-[10px] text-muted-foreground max-w-md">
          Restart and scale are not available in <strong>{pod.namespace}</strong>. They are granted
          only where Pod Security enforces <code>baseline</code>, because changing a pod template is
          root on the node anywhere a privileged pod is still admitted. Use <code>kubectl</code> for
          infrastructure workloads.
        </span>
      )}
      {restartable && (
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={busy}
          onClick={() => setPending('restart')}
        >
          Restart {controller.kind.toLowerCase()}
        </Button>
      )}
      {operable && controller?.scalable && (
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={busy}
          onClick={() => {
            // Seeded fresh at the moment the dialog opens, not left at
            // whatever it was set to the first time this component rendered:
            // `replicas` is local state and does not reinitialise itself when
            // `controller` changes underneath it, so a scale performed
            // earlier in this same panel session would otherwise leave the
            // input defaulted to the pre-scale count on a second open.
            setReplicas(String(controller.desiredReplicas))
            setPending('scale')
          }}
        >
          Scale
        </Button>
      )}
      <Button
        size="sm"
        variant="outline"
        className="font-mono text-destructive"
        disabled={busy}
        onClick={() => setPending('delete')}
      >
        Delete pod
      </Button>

      <AlertDialog open={pending === 'restart'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Restart {controller?.kind.toLowerCase()} {pod.namespace}/{controller?.name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Every pod is replaced, one batch at a time, following this controller's own update
              strategy — the pod template's restart annotation is bumped rather than pods being
              deleted, so surge and maxUnavailable are respected. Expect a brief capacity dip.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                controller &&
                restartable &&
                void run(`${controller.name} restarting`, () =>
                  frame.workloads.restart(
                    controller.kind as 'Deployment' | 'StatefulSet',
                    controller.namespace,
                    controller.name,
                  ),
                )
              }
            >
              Restart
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={pending === 'scale'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Scale {controller?.kind.toLowerCase()} {pod.namespace}/{controller?.name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Currently {controller?.desiredReplicas} replicas.
              {target === 0
                ? ' Scaling to zero stops this workload entirely: every pod is removed and nothing serves until it is scaled back up.'
                : ' The new count is applied through the scale subresource, which cannot change anything but the replica count.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Input
            value={replicas}
            inputMode="numeric"
            onChange={(e) => setReplicas(e.target.value)}
            className="font-mono text-xs w-24"
            aria-label="Replicas"
          />
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              disabled={!targetValid}
              onClick={() =>
                controller?.scalable &&
                targetValid &&
                void run(`${controller.name} scaled to ${target}`, () =>
                  frame.workloads.scale(
                    controller.kind as 'Deployment' | 'StatefulSet',
                    controller.namespace,
                    controller.name,
                    target,
                  ),
                )
              }
            >
              {target === 0 ? 'Stop' : 'Scale'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={pending === 'delete'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Delete pod {pod.namespace}/{pod.name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              {controller
                ? `${controller.kind} ${controller.name} owns this pod, so it will be recreated straight away. This is how you restart one pod without restarting the rest.`
                : pod.owner
                  ? `${pod.owner.kind} ${pod.owner.name} owns this pod, but this screen did not resolve it to a workload it tracks. The pod will most likely be recreated shortly — this screen cannot say by what, or how soon.`
                  : 'Nothing owns this pod. Deleting it is final — no controller will bring it back, and whatever it was doing stops for good.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                void run(`${pod.name} deleted`, () =>
                  frame.workloads.deletePod(pod.namespace, pod.name),
                )
              }
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
