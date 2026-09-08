/**
 * The UI's session.
 *
 * authd holds the real session: `POST /auth/login/password` sets an httpOnly
 * `frame_session` cookie (12h), and `POST /auth/token` reads that cookie and
 * mints a short-lived bearer token — deliberately returned in the response
 * body, never as a cookie, so it lives in this tab's memory only and a later
 * XSS cannot lift it from storage. This module is that memory: it holds the
 * one in-flight {@link Session} and mirrors its token onto
 * `globalThis.__FRAME_TOKEN__`, which `bearerToken()` in `frame-sdk.ts` and
 * `authHeaders()` in `k8s-watch.ts` already read.
 *
 * `globalThis`, not `window`: vitest runs this file under `environment:
 * 'node'`, which has no `window`, so touching `window` here would throw in
 * every test. In a browser `window === globalThis`, so the two SDK
 * functions above keep working unchanged.
 *
 * WebAuthn (`loginWithPasskey`, `enrolPasskey`) rides the same `frame_session`
 * cookie: `/auth/login/finish` sets it exactly like `/auth/login/password`
 * does, and `/auth/register/begin`+`/finish` read it to know who is
 * enrolling. The data-shape translation between authd's base64url JSON and
 * `navigator.credentials`' `ArrayBuffer`s lives in `webauthn.ts`, which is
 * the part that is actually unit-tested — this module only sequences the
 * two HTTP calls around the browser ceremony.
 */

import {
  authenticationResponseToJSON,
  registrationResponseToJSON,
  toCredentialCreationOptions,
  toCredentialRequestOptions,
  type CredentialCreationOptionsJSON,
  type CredentialRequestOptionsJSON,
} from './webauthn'

export interface Session {
  token: string
  expiresAt: number
}

/**
 * Refresh this far ahead of expiry rather than waiting for the token to go
 * stale mid-request. Two minutes comfortably covers one slow apiserver round
 * trip without refreshing on every call.
 */
const REFRESH_MARGIN_MS = 120_000

let session: Session | undefined

function publish(token: string | undefined): void {
  const g = globalThis as unknown as Record<string, unknown>
  if (token === undefined) {
    delete g.__FRAME_TOKEN__
  } else {
    g.__FRAME_TOKEN__ = token
  }
}

export async function loginWithPassword(email: string, password: string): Promise<void> {
  const res = await fetch('/auth/login/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  if (!res.ok) {
    throw new Error(`login failed: ${res.status} ${await res.text()}`)
  }
}

/**
 * Thrown when a WebAuthn ceremony ends because the user dismissed or never
 * responded to the browser's prompt — `NotAllowedError`/`AbortError` per
 * spec, or a `get()`/`create()` that resolved `null`. This is the ordinary
 * "changed their mind" outcome, not a bug in the ceremony or a server
 * rejection, so it is a distinct type the UI can render quietly instead of
 * as an alarming failure.
 */
export class PasskeyCancelledError extends Error {
  constructor() {
    super('Passkey action was cancelled.')
    this.name = 'PasskeyCancelledError'
  }
}

function isCancellation(err: unknown): boolean {
  return err instanceof DOMException && (err.name === 'NotAllowedError' || err.name === 'AbortError')
}

/**
 * `navigator.credentials` and `PublicKeyCredential` are absent on browsers
 * without WebAuthn (or without a secure context) — checked up front so
 * callers get one clear message instead of a `TypeError` from deep inside
 * the ceremony, or a wasted round trip to a "begin" endpoint.
 */
function ensurePasskeysSupported(): void {
  if (
    typeof navigator === 'undefined' ||
    !navigator.credentials ||
    typeof PublicKeyCredential === 'undefined'
  ) {
    throw new Error('This browser does not support passkeys (WebAuthn).')
  }
}

/**
 * Sign in with an enrolled passkey.
 *
 * Usernameless: `/auth/login/begin` takes no email and returns a
 * discoverable-credential request (see `BeginDiscoverableLogin` in
 * `internal/authd/webauthn.go`) — the browser offers whichever resident key
 * matches the RP ID, and authd identifies the account from the assertion's
 * `userHandle`.
 *
 * Like `loginWithPassword`, this only carries the ceremony through to
 * `/auth/login/finish`, which sets the `frame_session` cookie. It does not
 * mint a bearer token — call `currentSession()` afterwards, same as after
 * `loginWithPassword`.
 */
export async function loginWithPasskey(): Promise<void> {
  ensurePasskeysSupported()

  const beginRes = await fetch('/auth/login/begin', { method: 'POST' })
  if (!beginRes.ok) {
    throw new Error(`could not start passkey sign-in: ${beginRes.status} ${await beginRes.text()}`)
  }
  const options = (await beginRes.json()) as CredentialRequestOptionsJSON

  let credential: Credential | null
  try {
    credential = await navigator.credentials.get({ publicKey: toCredentialRequestOptions(options) })
  } catch (err) {
    if (isCancellation(err)) throw new PasskeyCancelledError()
    throw err
  }
  if (!credential) throw new PasskeyCancelledError()

  const finishRes = await fetch('/auth/login/finish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(authenticationResponseToJSON(credential as PublicKeyCredential)),
  })
  if (!finishRes.ok) {
    // authd answers every failure here with a bare 401 (see handleLoginFinish)
    // — it cannot say more without telling an attacker which credential ID
    // exists, so neither can this message.
    throw new Error(`passkey sign-in failed: ${finishRes.status} ${await finishRes.text()}`)
  }
}

/**
 * Enrol an additional passkey for whoever is already signed in.
 *
 * Requires an active `frame_session` cookie: `/auth/register/begin` resolves
 * the account from that cookie, never from anything this function sends
 * (see `handleRegisterBegin` in `internal/authd/server_webauthn.go`) — there
 * is no way to enrol a key for anyone but the caller. `label` is a
 * human-readable name for the key ("YubiKey 5C", "Pixel 8") and travels as
 * the `?label=` query parameter `/auth/register/finish` expects.
 */
export async function enrolPasskey(label: string): Promise<void> {
  ensurePasskeysSupported()

  const beginRes = await fetch('/auth/register/begin', { method: 'POST' })
  if (beginRes.status === 401) {
    throw new Error('Enrolling a passkey requires an active session — sign in first.')
  }
  if (!beginRes.ok) {
    throw new Error(`could not start passkey enrolment: ${beginRes.status} ${await beginRes.text()}`)
  }
  const options = (await beginRes.json()) as CredentialCreationOptionsJSON

  let credential: Credential | null
  try {
    credential = await navigator.credentials.create({ publicKey: toCredentialCreationOptions(options) })
  } catch (err) {
    if (isCancellation(err)) throw new PasskeyCancelledError()
    throw err
  }
  if (!credential) throw new PasskeyCancelledError()

  const finishRes = await fetch(`/auth/register/finish?label=${encodeURIComponent(label)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(registrationResponseToJSON(credential as PublicKeyCredential)),
  })
  if (!finishRes.ok) {
    throw new Error(`passkey enrolment failed: ${finishRes.status} ${await finishRes.text()}`)
  }
}

/**
 * Mint a fresh token from the `frame_session` cookie and publish it.
 *
 * A 401 means the cookie is gone or expired — the ordinary "not signed in"
 * state, not a failure — so it resolves to `undefined` rather than throwing.
 * Any other non-2xx status is a real problem (authd unreachable, 500, …) and
 * is thrown, so callers don't mistake an outage for a logged-out user.
 */
export async function currentSession(): Promise<Session | undefined> {
  const res = await fetch('/auth/token', { method: 'POST' })
  if (res.status === 401) {
    session = undefined
    publish(undefined)
    return undefined
  }
  if (!res.ok) {
    throw new Error(`cannot fetch session: ${res.status} ${await res.text()}`)
  }
  const body = (await res.json()) as { id_token: string; expires_in: number }
  session = { token: body.id_token, expiresAt: Date.now() + body.expires_in * 1000 }
  publish(session.token)
  return session
}

/**
 * The current bearer token, refreshing first if it's within
 * {@link REFRESH_MARGIN_MS} of expiry (or there is none cached yet).
 *
 * `now` defaults to `Date.now()` and exists so tests can simulate the clock
 * moving without a real timer.
 */
export async function ensureToken(now: number = Date.now()): Promise<string | undefined> {
  if (session && session.expiresAt - now > REFRESH_MARGIN_MS) {
    return session.token
  }
  const refreshed = await currentSession()
  return refreshed?.token
}

export async function logout(): Promise<void> {
  try {
    await fetch('/auth/logout', { method: 'POST' })
  } finally {
    // Always drop the local session, even if the network call to clear the
    // server-side cookie failed — the user asked to leave, and the tab
    // should stop presenting itself as signed in regardless.
    session = undefined
    publish(undefined)
  }
}

export function __resetForTests(): void {
  session = undefined
  publish(undefined)
}
