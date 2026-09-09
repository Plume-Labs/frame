import { useState } from 'react'
import { acceptInvitation } from '@/lib/accounts'
import { currentSession, enrolPasskey, PasskeyCancelledError, type Session } from '@/lib/auth'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Cpu, Fingerprint } from '@phosphor-icons/react'

/**
 * Where an invitation is spent.
 *
 * The link's token buys one thing: a fifteen-minute session whose only use is
 * enrolling a key. So this screen does the two steps in one gesture — accept,
 * then immediately run the WebAuthn ceremony — rather than dropping the
 * invitee at a login screen they have no credential for. Once the key exists
 * the invitation is dead by construction (authd refuses it the moment the
 * account holds a credential), which is also why the URL is rewritten on the
 * way out: a reload of /invite?token=… would meet a 410 and read as a bug.
 */
export function InviteAcceptView({
  token,
  onEnrolled,
}: {
  token: string
  onEnrolled: (session: Session) => void
}) {
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)

  async function handleAccept() {
    setBusy(true)
    setError(undefined)
    try {
      await acceptInvitation(token)
      await enrolPasskey(label.trim() || 'Passkey')
      const session = await currentSession()
      if (!session) {
        setError('the key was enrolled but no session was issued — sign in from the console')
        return
      }
      globalThis.history.replaceState({}, '', '/')
      onEnrolled(session)
    } catch (err) {
      if (!(err instanceof PasskeyCancelledError)) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Cpu className="text-primary" weight="bold" />
            FRAME
          </CardTitle>
          <p className="text-xs text-muted-foreground">
            You have been invited. Enrol a passkey to finish — this link works once.
          </p>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="invite-key-label">Name this key</Label>
            <Input
              id="invite-key-label"
              placeholder="YubiKey 5C"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              disabled={busy}
              autoFocus
            />
          </div>
          {error && (
            <p role="alert" className="text-xs font-mono text-destructive break-words">
              {error}
            </p>
          )}
          <Button className="w-full font-mono gap-1.5" disabled={busy} onClick={() => void handleAccept()}>
            <Fingerprint />
            {busy ? 'Waiting for authenticator…' : 'Enrol a passkey'}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
