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
 * invitee at a login screen they have no credential for.
 *
 * That enrolment cookie is also why `currentSession()` almost never resolves
 * here: it is scoped to the two WebAuthn enrolment routes only, so
 * `/auth/token` — the route `currentSession()` calls — answers 401 for it,
 * the same as if there were no session at all. So the *expected* ending,
 * every time, is `finished` with no session: the invitee holds a working
 * passkey and needs one ordinary sign-in to use it, not a redirect into a
 * console that would just 401. That branch is a success, not an error, and
 * is styled as one — see the `finished` render below.
 *
 * Once the key exists the invitation is dead by construction (authd refuses
 * it the moment the account holds a credential), which is why the URL is
 * rewritten the moment enrolment succeeds, whichever branch follows: a
 * reload of /invite?token=… would otherwise meet a 410 and read as a bug.
 */
export function InviteAcceptView({
  token,
  onEnrolled,
  onFinished,
}: {
  token: string
  onEnrolled: (session: Session) => void
  /** Called once the invitee is done here and should see the sign-in screen. */
  onFinished: () => void
}) {
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | undefined>(undefined)
  const [finished, setFinished] = useState(false)

  async function handleAccept() {
    setBusy(true)
    setError(undefined)
    try {
      await acceptInvitation(token)
      await enrolPasskey(label.trim() || 'Passkey')
      // The invitation is spent the instant enrolment succeeds, regardless
      // of what currentSession() below finds — rewriting the URL here,
      // rather than only in the has-a-session branch, is what keeps a reload
      // from meeting a 410.
      globalThis.history.replaceState({}, '', '/')
      const session = await currentSession()
      if (session) {
        onEnrolled(session)
      } else {
        // The designed ending (see the module doc): say what happened and
        // what to do next, not an error banner for a path that isn't one.
        setFinished(true)
      }
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
          {!finished && (
            <p className="text-xs text-muted-foreground">
              You have been invited. Enrol a passkey to finish — this link works once.
            </p>
          )}
        </CardHeader>
        <CardContent className="space-y-3">
          {finished ? (
            <>
              <p className="text-sm">
                Your passkey is enrolled. This invitation link is now spent — sign in with your new
                passkey to continue.
              </p>
              <Button className="w-full font-mono" onClick={onFinished}>
                Go to sign in
              </Button>
            </>
          ) : (
            <>
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
              {error && (
                // A failure here is not always retryable — a spent or expired
                // link answers the same way every time (see the module doc)
                // — so this screen must not be a dead end the way `finished`
                // used to be before it. Same exit, offered alongside the
                // retry rather than instead of it, since some failures (a
                // dropped connection, a cancelled ceremony that got treated
                // as an error) genuinely are worth trying again first.
                <Button variant="outline" className="w-full font-mono" onClick={onFinished}>
                  Go to sign in
                </Button>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
