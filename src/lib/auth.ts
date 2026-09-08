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
 */

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
