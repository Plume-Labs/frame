import { FormEvent, useState } from 'react'
import {
  currentSession,
  loginWithPasskey,
  loginWithPassword,
  PasskeyCancelledError,
  type Session,
} from '@/lib/auth'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Cpu, Fingerprint, LockSimple } from '@phosphor-icons/react'

/**
 * The gate the whole console sits behind. `App` renders this in place of
 * itself whenever `currentSession()` resolves `undefined`.
 *
 * Two independent ways in, same destination (`onSignedIn`): the password
 * form, and "Sign in with a passkey" below it, which drives
 * `/auth/login/begin` + `/auth/login/finish` (usernameless — no email field
 * involved). Either path only sets the `frame_session` cookie;
 * `currentSession()` is what turns that into the bearer token the rest of
 * the SDK reads, so both handlers call it the same way before calling
 * `onSignedIn`.
 *
 * Errors from authd's password path are rendered verbatim rather than
 * translated: the API only ever answers "unauthorized" (F: password login
 * cannot distinguish an unknown email from a wrong password, on purpose —
 * see authd's handlePasswordLogin), so there is no friendlier copy to invent
 * that wouldn't imply we know which field was wrong when we don't. A
 * cancelled passkey prompt is the one case handled distinctly: it is the
 * user changing their mind, not an error, so it clears silently instead of
 * leaving a message on screen.
 */
export function LoginView({ onSignedIn }: { onSignedIn: (session: Session) => void }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | undefined>(undefined)
  const [submitting, setSubmitting] = useState(false)
  const [passkeySubmitting, setPasskeySubmitting] = useState(false)

  async function afterCookieSet() {
    // Shared by both sign-in paths: loginWithPassword/loginWithPasskey only
    // set the httpOnly cookie; currentSession() is what actually mints and
    // publishes the bearer token the rest of the SDK reads.
    const session = await currentSession()
    if (!session) {
      setError('login succeeded but no session cookie was set')
      return
    }
    onSignedIn(session)
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitting(true)
    setError(undefined)
    try {
      await loginWithPassword(email, password)
      await afterCookieSet()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  async function handlePasskeySignIn() {
    setPasskeySubmitting(true)
    setError(undefined)
    try {
      await loginWithPasskey()
      await afterCookieSet()
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
        <CardContent>
          <form className="space-y-4" onSubmit={handleSubmit}>
            <div className="space-y-1.5">
              <Label htmlFor="login-email">Email</Label>
              <Input
                id="login-email"
                type="email"
                autoComplete="username"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="login-password">Password</Label>
              <Input
                id="login-password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {error && (
              <p role="alert" className="text-xs font-mono text-destructive break-words">
                {error}
              </p>
            )}
            <Button type="submit" className="w-full font-mono gap-1.5" disabled={submitting}>
              <LockSimple />
              {submitting ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>

          <div className="my-4 flex items-center gap-2 text-[10px] font-mono text-muted-foreground">
            <div className="h-px flex-1 bg-border" />
            OR
            <div className="h-px flex-1 bg-border" />
          </div>

          <Button
            type="button"
            variant="outline"
            className="w-full font-mono gap-1.5"
            disabled={passkeySubmitting}
            onClick={() => void handlePasskeySignIn()}
          >
            <Fingerprint />
            {passkeySubmitting ? 'Waiting for passkey…' : 'Sign in with a passkey'}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
