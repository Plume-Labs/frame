import { FormEvent, useState } from 'react'
import { enrolPasskey, PasskeyCancelledError } from '@/lib/auth'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Fingerprint, Plus } from '@phosphor-icons/react'

interface EnrolledThisSession {
  label: string
  addedAt: number
}

/**
 * Where a signed-in user manages their passkeys.
 *
 * Lives behind a "Passkeys" entry in the sidebar footer, next to "Sign
 * out" — the other account-scoped action, and the only place in the app
 * that isn't "cluster wiring". `SettingsView` was the other candidate, but
 * it edits `FrameConfig` (the ConfigMap that tells the UI where cluster
 * components live); a user's own credentials are a different kind of thing
 * entirely and don't belong on that screen.
 *
 * The list below is deliberately labelled "enrolled this session", not
 * "your passkeys": authd exposes no endpoint to list a user's previously
 * enrolled credentials (only `/auth/register/begin`+`/finish`, which create
 * one, and the two login endpoints). Reading `FrameUser.status.credentials`
 * back would need either a new authd route or direct k8s API access this
 * task's scope doesn't cover — see the task report. Showing a full history
 * here would be lying about what this screen can actually see.
 */
export function PasskeysDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [label, setLabel] = useState('')
  const [enrolling, setEnrolling] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)
  const [enrolled, setEnrolled] = useState<EnrolledThisSession[]>([])

  async function handleEnrol(e: FormEvent) {
    e.preventDefault()
    const trimmed = label.trim()
    if (!trimmed) return
    setEnrolling(true)
    setError(undefined)
    try {
      await enrolPasskey(trimmed)
      setEnrolled((prev) => [...prev, { label: trimmed, addedAt: Date.now() }])
      setLabel('')
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

  return (
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
            Enrolled this session
          </p>
          {enrolled.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              No keys added yet in this browser session.
            </p>
          ) : (
            <ul className="space-y-1">
              {enrolled.map((key, i) => (
                <li
                  key={`${key.label}-${key.addedAt}-${i}`}
                  className="flex items-center justify-between rounded border border-border bg-secondary/30 px-2 py-1.5 text-xs font-mono"
                >
                  <span className="truncate">{key.label}</span>
                  <span className="text-muted-foreground shrink-0">
                    {new Date(key.addedAt).toLocaleTimeString()}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <p className="text-[10px] text-muted-foreground">
            Frame does not yet expose a way to list keys enrolled in earlier sessions — only what
            you add here shows up above.
          </p>
        </div>

        <DialogFooter>
          <Button variant="outline" className="font-mono" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
