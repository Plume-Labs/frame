import { useCallback, useEffect, useState } from 'react'
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
import { Textarea } from '@/components/ui/textarea'
import { FrameAPIError, createFrameClient } from '@/lib/frame-sdk'
import { canEditManifest, ownershipWarning, type EditableKind } from '@/lib/workloads'

const frame = createFrameClient()

/**
 * The resource as stored, editable.
 *
 * The text is the apiserver's own YAML (`Accept: application/yaml`) and goes
 * back as the apiserver's own YAML (`Content-Type: application/yaml`), so
 * nothing in this repository parses or emits YAML and nothing rewrites what a
 * person read. It also means the `resourceVersion` in the text is the one that
 * was read — leave it there: it is what turns a concurrent change into a 409
 * instead of a silent overwrite of someone else's work.
 *
 * The editor is bounded twice. By kind, to the five the Workloads tree shows:
 * "edit any resource" would mean granting admin `patch` across the whole
 * cluster, every Secret and ClusterRole included. And by namespace, to the ones
 * enforcing `baseline` Pod Security — because cluster-wide, `update` on a pod
 * template is `privileged: true` plus a `hostPath: /` volume in a namespace
 * where nothing refuses it, which is node root.
 *
 * It is also bounded by tier, and that bound is not optional either
 * (whole-branch review Important 3): the grant behind Apply is
 * `cluster-control-workload-admin`, bound only to `frame:admins`
 * (test/manifests/rbac_workload_operator_test.go asserts `frame:operators` is
 * not a subject). `canOperateWorkloads(namespace)` alone is the *operator*
 * predicate — restart and scale's grant, not this one — so gating Apply on it
 * by itself would enable the button for an operator, who then 403s after
 * typing an edit. `admin` is threaded down from WorkloadsView's own decoded
 * token (a courtesy, not a control — see that file's comment on it) purely so
 * this component can make the same mistake the rest of the panel already
 * avoids.
 *
 * Both bounds apply to **saving**, never to reading. The tab renders
 * everywhere the tree shows a workload, because looking at a DaemonSet's
 * manifest in `kube-system` is one of the more useful things this screen does
 * and the `get` behind it is granted cluster-wide. Only the Save button is
 * disabled, and it says why — a button that 403s after someone has typed an
 * edit is worse than a button that was never offered. The two reasons a
 * person can land on read-only are named separately, because "you are not an
 * administrator" and "writes do not reach this namespace" call for different
 * next steps.
 */
export function YamlTab({
  kind,
  namespace,
  name,
  admin,
  onSaved,
}: {
  kind: EditableKind
  namespace: string
  name: string
  admin: boolean
  onSaved: () => void
}) {
  const [text, setText] = useState('')
  const [before, setBefore] = useState<Record<string, unknown> | undefined>()
  const [warning, setWarning] = useState<string | undefined>()
  const [error, setError] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)
  const [confirming, setConfirming] = useState(false)

  // Saving needs both: the admin tier (the grant's actual subject) and a
  // namespace where Pod Security enforces `baseline` (the grant's actual
  // reach). Reading is not gated at all — see this component's doc comment.
  const writable = canEditManifest(admin, namespace)

  const load = useCallback(async () => {
    setError(undefined)
    try {
      // Sequential, not Promise.all: both are GETs on the same path, and
      // reading them one after the other keeps the SDK's in-flight
      // de-duplication out of the question entirely.
      const object = await frame.workloads.object(kind, namespace, name)
      const yaml = await frame.workloads.manifest(kind, namespace, name)
      setBefore(object)
      setText(yaml)
      const meta = (object.metadata ?? {}) as {
        labels?: Record<string, string>
        annotations?: Record<string, string>
      }
      setWarning(ownershipWarning(meta))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [kind, namespace, name])

  useEffect(() => {
    void load()
  }, [load])

  const save = async () => {
    setConfirming(false)
    setBusy(true)
    try {
      await frame.workloads.applyManifest({ kind, namespace, name, before, yaml: text })
      toast.success(`${name} updated`)
      onSaved()
      await load()
    } catch (e) {
      if (e instanceof FrameAPIError && e.statusCode === 409) {
        // The reason the resourceVersion travels with the edit. Say what
        // happened rather than reporting a status code: the person's text is
        // still on screen and still valid, against a version that no longer
        // exists.
        setError(
          'Someone else changed this object while you were editing it. Nothing was written. ' +
            'Reload to get the current version, then re-apply your change.',
        )
      } else {
        setError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      {warning && (
        <p className="font-mono text-xs text-primary border border-primary/40 rounded-md p-2">
          {warning}
        </p>
      )}
      {error && <p className="font-mono text-xs text-destructive">{error}</p>}

      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        spellCheck={false}
        className="h-96 font-mono text-[11px] leading-relaxed"
      />

      <div className="flex items-center gap-2">
        <Button
          size="sm"
          className="font-mono"
          disabled={busy || !writable}
          onClick={() => setConfirming(true)}
        >
          Apply
        </Button>
        <Button size="sm" variant="outline" className="font-mono" disabled={busy} onClick={() => void load()}>
          Reload
        </Button>
        <span className="font-mono text-[10px] text-muted-foreground max-w-lg">
          {writable ? (
            'Applied as a whole-object update, carrying the version you read.'
          ) : !admin ? (
            <>
              Read-only: saving a manifest is an administrator action. Your account is not an
              admin — restart and scale, if this namespace grants them, are still yours.
            </>
          ) : (
            <>
              Read-only in <strong>{namespace}</strong>. Saving is granted only where Pod Security
              enforces <code>baseline</code>, because rewriting a pod template is root on the node
              anywhere a privileged pod is still admitted. Use <code>kubectl</code> for
              infrastructure workloads.
            </>
          )}
        </span>
      </div>

      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Apply your changes to {kind.toLowerCase()} {namespace}/{name}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              The manifest is validated against the apiserver first, as a dry run that stores
              nothing and is not itself recorded, and only written if it is accepted. One row
              appears on the Tasks screen, naming the fields you changed.
              {warning ? ` ${warning}` : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void save()}>Apply</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
