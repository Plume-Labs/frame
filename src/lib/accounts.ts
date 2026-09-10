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
 * Errors carry authd's own message rather than a status code, forwarded
 * verbatim — never rewritten here. Each of these routes has exactly one
 * refusal a person can act on: invite refuses a role other than operator or
 * viewer; accept refuses a token whose account already holds a credential;
 * revoke refuses removing an account's last credential. Collapsing any of
 * those into "request failed" would throw away the only useful part, and
 * rewording them client-side would let the message drift from whatever the
 * server actually decided to say — as it already has once (Task 5's review
 * changed accept's wording after this module was first written).
 */

import { createFrameClient } from './frame-sdk'

/** One enrolled authenticator, as `GET /auth/credentials` returns it. */
export interface CredentialSummary {
  id: string
  label: string
  addedAt: string
  signCount: number
}

/**
 * One FrameUser, as the Accounts screen shows it: who they are, what they can
 * do, whether they can sign in, and how many keys stand between them and
 * being locked out.
 */
export interface Account {
  name: string
  email: string
  role: string
  state: string
  keyCount: number
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
 * node — and scoped to `/invite`, so a stray `#token=` on any other screen
 * cannot divert the console into spending an invitation.
 *
 * Reads the *fragment*, not the query string. A fragment is never sent to a
 * server, which keeps this token — a bearer credential good for one
 * enrolment against a named account — out of every access log on the way,
 * and out of the same-origin `Referer` that each asset the `/invite` page
 * loads would otherwise carry. authd builds the link to match
 * (`internal/authd/server_invite.go`), with `url.QueryEscape`, whose output
 * is exactly what `URLSearchParams` decodes; the two are chosen as a pair.
 */
export function inviteTokenFromLocation(pathname: string, hash: string): string | undefined {
  if (pathname !== '/invite') return undefined
  // Fragment or nothing. `location.hash` is either empty or starts with '#',
  // so refusing everything else costs nothing legitimate — and it is the
  // only place the fragment-only rule can actually be enforced. Without it
  // a caller that regressed to passing `location.search` would keep working
  // silently, because `URLSearchParams` strips a leading '?' for you, and
  // the query-string shape this change exists to remove would be back with
  // every test still green.
  if (!hash.startsWith('#')) return undefined
  // The leading '#' is stripped by hand: `URLSearchParams` strips a leading
  // '?' but not a '#', so passing it through would look for a parameter
  // named '#token'.
  const token = new URLSearchParams(hash.slice(1)).get('token')
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
 * Mint a fresh invitation link for an account that already exists — for when
 * the one link `inviteAccount` returned expired (24h) or was never captured
 * before the dialog that showed it once was closed. The only other way back
 * to enrolment is deleting the FrameUser and inviting again.
 *
 * Same shape as `inviteAccount`: authd's own message is forwarded verbatim
 * on a refusal (an unknown email, an account that already holds a
 * credential, or a disabled account) rather than rewritten here.
 */
export async function reissueInvitation(email: string): Promise<string> {
  const res = await fetch('/auth/invite/link', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
  })
  if (!res.ok) throw await failure(res, 'could not create the invitation link')
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

/**
 * Every account, with how many keys each holds.
 *
 * The FrameUser read goes through `createFrameClient().users`, not a bare
 * `fetch`, so a 401 here recovers the same way every other screen does — see
 * `UserClient`'s doc comment in `frame-sdk.ts`. The key count comes from
 * authd rather than from `status.credentials` on the FrameUser, even though
 * the apiserver would return it in the same list this function already
 * makes: `GET /auth/credentials` is the one shape the console is allowed to
 * see of a credential record — no public-key material — and reading the raw
 * status here would put material on screen that the dedicated route exists
 * to withhold. One request per account is affordable: these are humans, not
 * nodes.
 */
export async function listAccounts(): Promise<Account[]> {
  const items = await createFrameClient().users.list()
  return Promise.all(
    items.map(async (i) => ({
      name: i.metadata.name,
      email: i.spec.email,
      role: i.spec.role,
      // An account written before spec.state existed reads back defaulted by
      // the apiserver; the fallback is for a response assembled anywhere
      // else. Matches isEnabled() in frameuser_webhook.go: "" means enabled.
      state: i.spec.state ?? 'enabled',
      // A failure to read one account's keys must not blank the whole table:
      // -1 renders as "?" in the UI, which is honest, where 0 would be a lie
      // about an account that may well hold keys.
      keyCount: await listCredentials(i.spec.email).then((k) => k.length).catch(() => -1),
    })),
  )
}

/** Promote or demote an account. `email` only phrases the audit action — see `UserClient.setRole`. */
export async function setAccountRole(name: string, email: string, role: string): Promise<void> {
  await createFrameClient().users.setRole(name, email, role)
}

/** Enable or disable an account's ability to sign in. `email` only phrases the audit action. */
export async function setAccountState(name: string, email: string, state: 'enabled' | 'disabled'): Promise<void> {
  await createFrameClient().users.setState(name, email, state)
}

/**
 * True when `email` is the one enabled admin standing between `accounts` and
 * a lockout — the exact condition `requireAnotherAdmin` in
 * `frameuser_webhook.go` refuses a demotion or a disable over.
 *
 * Mirrored here, not just left to the server, so the Accounts screen never
 * offers a control it already knows will be refused: the webhook's error
 * would be correct but unreachable-looking ("why won't this dropdown work")
 * without a reason shown next to it. `disabled` and `role !== 'admin'` both
 * count as "not an enabled admin" — a disabled admin cannot sign in to
 * authorize anything, exactly like a non-admin.
 *
 * The check below is `a.state === 'enabled'`, not the server's
 * `isEnabled = state != "disabled"` — those two are not the same test, and
 * they diverge on `""`. They agree here anyway, but only because `state`
 * never arrives as `""` from the apiserver: the CRD declares
 * `enum: [enabled, disabled]` with `default: enabled`, so an explicit `""`
 * is refused at admission and an absent value is defaulted before we ever
 * read it. Note this is NOT what `listAccounts()`'s `i.spec.state ??
 * 'enabled'` buys us -- `??` substitutes only for null and undefined and
 * would pass `""` straight through. The guarantee is the CRD's, so it holds
 * for any `Account` built from an apiserver read and for no other kind.
 */
export function isOnlyEnabledAdmin(accounts: Account[], email: string): boolean {
  const enabledAdmins = accounts.filter((a) => a.role === 'admin' && a.state === 'enabled')
  return enabledAdmins.length === 1 && enabledAdmins[0].email === email
}
