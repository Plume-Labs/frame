import { FormEvent, useEffect, useState } from 'react'
import { enrolPasskey, PasskeyCancelledError } from '@/lib/auth'
import { listCredentials, revokeCredential, type CredentialSummary } from '@/lib/accounts'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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
import { Label } from '@/components/ui/label'
import { Fingerprint, Plus, Trash } from '@phosphor-icons/react'

/**
 * Where a signed-in user manages their passkeys.
 *
 * The list is the account's real one, read from `GET /auth/credentials` —
 * lot 0c added that route, and this component's previous "enrolled this
 * session" caveat was the visible shape of its absence. Revoking is refused
 * by authd (409) when it would leave a passkey-only account with no way in,
 * so the last key cannot be removed by accident from here — but a revoke
 * still asks first, since even a non-last key is a working sign-in method
 * removed with no undo.
 */
export function PasskeysDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [label, setLabel] = useState('')
  const [enrolling, setEnrolling] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)
  const [keys, setKeys] = useState<CredentialSummary[]>([])
  const [pendingRevoke, setPendingRevoke] = useState<CredentialSummary | undefined>(undefined)

  const refreshKeys = () => {
    listCredentials()
      .then((k) => {
        setKeys(k)
        setError(undefined)
      })
      .catch((err: unknown) => {
        // A failed read must not leave the previous list on screen under a
        // new error: that reads as "here are your keys" when it is actually
        // "the last read failed and this may be stale or wrong".
        setKeys([])
        setError(err instanceof Error ? err.message : String(err))
      })
  }

  useEffect(() => {
    if (open) refreshKeys()
  }, [open])

  async function handleEnrol(e: FormEvent) {
    e.preventDefault()
    const trimmed = label.trim()
    if (!trimmed) return
    setEnrolling(true)
    setError(undefined)
    try {
      await enrolPasskey(trimmed)
      setLabel('')
      refreshKeys()
    } catch (err) {
      // A dismissed prompt is the user changing their mind, not a failure —
      // leave the form quiet so they can just try again.
      if (!(err instanceof PasskeyCancelledError)) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setEnrolling(false)
    }
  }

  async function handleRevoke(id: string) {
    setError(undefined)
    try {
      await revokeCredential(id)
      refreshKeys()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <>
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="font-mono flex items-center gap-2">
            <Fingerprint className="text-primary" />
            Passkeys
          </DialogTitle>
          <DialogDescription>
            Add a hardware key or platform authenticator so you can sign in without a password.
          </DialogDescription>
        </DialogHeader>

        <form className="space-y-3" onSubmit={handleEnrol}>
          <div className="space-y-1.5">
            <Label htmlFor="passkey-label">Label</Label>
            <Input
              id="passkey-label"
              placeholder="YubiKey 5C"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              disabled={enrolling}
              autoFocus
              required
            />
          </div>
          {error && (
            <p role="alert" className="text-xs font-mono text-destructive break-words">
              {error}
            </p>
          )}
          <Button type="submit" className="w-full font-mono gap-1.5" disabled={enrolling || !label.trim()}>
            <Plus />
            {enrolling ? 'Waiting for authenticator…' : 'Add passkey'}
          </Button>
        </form>

        <div className="space-y-1.5 pt-2 border-t border-border">
          <p className="text-[10px] font-mono uppercase tracking-widest text-muted-foreground">
            Your passkeys
          </p>
          {keys.length === 0 ? (
            <p className="text-xs text-muted-foreground">No keys enrolled.</p>
          ) : (
            <ul className="space-y-1">
              {keys.map((key) => (
                <li
                  key={key.id}
                  className="flex items-center justify-between rounded border border-border bg-secondary/30 px-2 py-1.5 text-xs font-mono"
                >
                  <span className="truncate">{key.label || key.id}</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={`Revoke ${key.label || key.id}`}
                    title={`Revoke ${key.label || key.id}`}
                    onClick={() => setPendingRevoke(key)}
                  >
                    <Trash />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" className="font-mono" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <AlertDialog open={!!pendingRevoke} onOpenChange={(next) => !next && setPendingRevoke(undefined)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="font-mono">
            Revoke {pendingRevoke?.label || pendingRevoke?.id}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            You will no longer be able to sign in with this key. This cannot be undone. (authd
            refuses to remove your last key, so this is safe to confirm even if it turns out to be
            your only one.)
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              if (!pendingRevoke) return
              const id = pendingRevoke.id
              setPendingRevoke(undefined)
              void handleRevoke(id)
            }}
          >
            Revoke
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
    </>
  )
}
