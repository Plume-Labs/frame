import { useState } from 'react'
import { currentSession, loginWithPasskey, PasskeyCancelledError, type Session } from '@/lib/auth'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Cpu, Fingerprint } from '@phosphor-icons/react'

/**
 * The gate the whole console sits behind. `App` renders this in place of
 * itself whenever `currentSession()` resolves `undefined`.
 *
 * One way in: "Sign in with a passkey", which drives `/auth/login/begin` +
 * `/auth/login/finish` (usernameless — no email field involved). That only
 * sets the `frame_session` cookie; `currentSession()` is what turns it into
 * the bearer token the rest of the SDK reads, so it is called before
 * `onSignedIn`.
 *
 * There is no password form. authd does not serve password sign-in (a
 * password alone is a single factor), and offering one here would only be a
 * form that always fails. It comes back when authd ships password sign-in
 * together with a second factor (TOTP), not before — see
 * `TestPasswordLoginIsNotServed` in `internal/authd/server_test.go` and
 * `LoginView.test.ts`.
 *
 * A cancelled passkey prompt is handled distinctly: it is the user changing
 * their mind, not an error, so it clears silently instead of leaving a
 * message on screen. Any other failure is shown verbatim.
 */
export function LoginView({ onSignedIn }: { onSignedIn: (session: Session) => void }) {
  const [error, setError] = useState<string | undefined>(undefined)
  const [passkeySubmitting, setPasskeySubmitting] = useState(false)

  async function handlePasskeySignIn() {
    setPasskeySubmitting(true)
    setError(undefined)
    try {
      await loginWithPasskey()
      // loginWithPasskey only sets the httpOnly cookie; currentSession() is
      // what actually mints and publishes the bearer token the SDK reads.
      const session = await currentSession()
      if (!session) {
        setError('login succeeded but no session cookie was set')
        return
      }
      onSignedIn(session)
    } catch (err) {
      // A dismissed/cancelled prompt is not a failure worth alarming the
      // user over — leave the screen quiet so they can just try again.
      if (!(err instanceof PasskeyCancelledError)) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      setPasskeySubmitting(false)
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
          <p className="text-xs text-muted-foreground">Sign in to continue.</p>
        </CardHeader>
        <CardContent className="space-y-4">
          {error && (
            <p role="alert" className="text-xs font-mono text-destructive break-words">
              {error}
            </p>
          )}
          <Button
            type="button"
            className="w-full font-mono gap-1.5"
            disabled={passkeySubmitting}
            onClick={() => void handlePasskeySignIn()}
            autoFocus
          >
            <Fingerprint />
            {passkeySubmitting ? 'Waiting for passkey…' : 'Sign in with a passkey'}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
