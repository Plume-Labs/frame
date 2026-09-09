/**
 * The console's account-management calls: invitations, and the keys an
 * account holds.
 *
 * Everything here is a function rather than a hook or a component method for
 * one reason: vitest runs with `environment: 'node'` and collects only
 * `.test.ts` files under `src/`, so a `.tsx` spec never executes. Logic that
 * matters lives where a test can reach it, and `AccountsView`,
 * `InviteAcceptView` and `PasskeysDialog` only call it.
 *
 * Errors carry authd's own message rather than a status code. Each of these
 * routes has exactly one refusal a person can act on — invite: "operator or
 * viewer only"; accept: "already been used"; revoke: "would leave the account
 * unreachable" — and rewriting those into "request failed" would throw away
 * the only useful part.
 */

/** One enrolled authenticator, as `GET /auth/credentials` returns it. */
export interface CredentialSummary {
  id: string
  label: string
  addedAt: string
  signCount: number
}

/**
 * The roles an invitation may create.
 *
 * Not `admin`, and not by preference: authd creates the FrameUser under its
 * own ServiceAccount, and the admission webhook refuses a `spec.role: admin`
 * create from anyone who is not already an admin. Promotion happens from the
 * Accounts screen instead, which writes through the apiserver under the
 * signed-in admin's own impersonated identity.
 */
export type InvitableRole = 'operator' | 'viewer'

async function failure(res: Response, fallback: string): Promise<Error> {
  const body = (await res.text()).trim()
  return new Error(body === '' ? `${fallback} (${res.status})` : body)
}

/**
 * The invitation token in the current URL, if this is the invitation page.
 *
 * Takes the location apart rather than reading it, so it is testable under
 * node — and scoped to `/invite`, so a stray `?token=` on any other screen
 * cannot divert the console into spending an invitation.
 */
export function inviteTokenFromLocation(pathname: string, search: string): string | undefined {
  if (pathname !== '/invite') return undefined
  const token = new URLSearchParams(search).get('token')
  return token ? token : undefined
}

/** Create an account with no credential; resolves to the link that gives it one. */
export async function inviteAccount(email: string, role: InvitableRole): Promise<string> {
  const res = await fetch('/auth/invite', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, role }),
  })
  if (!res.ok) throw await failure(res, 'could not create the invitation')
  const body = (await res.json()) as { url: string }
  return body.url
}

/**
 * Spend an invitation. On success the browser holds a fifteen-minute session
 * whose only use is `enrolPasskey` — long enough to enrol, short enough that a
 * forwarded link is not a standing account.
 */
export async function acceptInvitation(token: string): Promise<void> {
  const res = await fetch('/auth/invite/accept', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (res.status === 410) {
    // authd's own wording here is "this account already has a credential" —
    // accurate about the account's state, but it reads like a report, not an
    // explanation of what happened to *this* link. Rewritten to name the
    // thing the invitee actually did: used the link already (or someone else
    // did, on their behalf).
    throw new Error('this invitation link has already been used')
  }
  if (!res.ok) throw await failure(res, 'this invitation could not be accepted')
}

/** The caller's enrolled keys, or another account's when `email` is given and the caller is an admin. */
export async function listCredentials(email?: string): Promise<CredentialSummary[]> {
  const query = email ? `?user=${encodeURIComponent(email)}` : ''
  const res = await fetch(`/auth/credentials${query}`)
  if (!res.ok) throw await failure(res, 'could not read the enrolled keys')
  const body = (await res.json()) as { credentials?: CredentialSummary[] }
  return body.credentials ?? []
}

/** Remove one enrolled key. Refused (409) if it would leave the account unreachable. */
export async function revokeCredential(id: string, email?: string): Promise<void> {
  const query = email ? `?user=${encodeURIComponent(email)}` : ''
  const res = await fetch(`/auth/credentials/${encodeURIComponent(id)}${query}`, { method: 'DELETE' })
  if (!res.ok) throw await failure(res, 'could not remove the key')
}
